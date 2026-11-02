package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"sync"
	texttemplate "text/template"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/calllog"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/market"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/search"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

const FollowupWorkflowName = "FollowupWorkflow"

// depthBudget sizes a report: sub-questions, sources per question and target length.
type depthBudget struct {
	Questions, Sources int
	LengthHint         string
	// FullPages is how many top sources per sub-question are read in full, not just their snippet.
	FullPages int
}

var budgets = map[string]depthBudget{
	"quick":    {3, 4, "1500 字左右", 0},
	"standard": {5, 6, "3000 字左右", 2},
	"deep":     {7, 8, "5000 字左右", 3},
}

var lengthHintsEN = map[string]string{"quick": "about 1,000 words", "standard": "about 2,000 words", "deep": "about 3,500 words"}

func reportLengthHint(l i18n.Lang, depth string) string {
	return l.Pick(budgets[depth].LengthHint, lengthHintsEN[depth])
}

// wordCount is characters for Chinese and words for English, as the App labels it.
func wordCount(l i18n.Lang, s string) int {
	if l == i18n.EN {
		return len(strings.Fields(s))
	}
	return utf8.RuneCountInString(s)
}

func researchStep(l i18n.Lang, i int, q string) events.Step {
	return events.Step{Index: i + 1, Title: l.Pick("调研：", "Research: ") + clip(q, l.ClipLen(40)), Tool: "search"}
}

func writeStep(l i18n.Lang, n int) events.Step {
	return events.Step{Index: n, Title: l.Pick("撰写报告", "Writing the report"), Tool: "write"}
}

type Question struct {
	Regions  []string `json:"regions,omitempty"`
	Question string   `json:"question"`
	Queries  []string `json:"queries"`
}

type ReportPlan struct {
	Market    string
	UserID    uuid.UUID
	TaskID    uuid.UUID
	ReportID  uuid.UUID
	Topic     string
	Depth     string
	Focuses   []string
	Title     string
	Questions []Question
	// Lang is the report's language; UILang the App's (progress steps, push).
	Lang   i18n.Lang
	UILang i18n.Lang
}

// QuestionNotes are one sub-question's findings; [n] in Notes indexes Sources from 1.
type QuestionNotes struct {
	Question string
	Notes    string
	Sources  []search.Result
}

// ReportWorkflow: plan sub-questions → research them in parallel → write the report.
// Messages sent while it runs ("补充要求") are passed to the research and writing steps that
// start afterwards. This is the MVP pipeline; the free-form tool-using agent loop arrives with
// the sandbox.
func ReportWorkflow(ctx workflow.Context, run orch.ReportRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: modelActivityTimeout,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})

	var notes []string
	msgs := workflow.GetSignalChannel(ctx, orch.SignalMessage)
	workflow.Go(ctx, func(ctx workflow.Context) {
		for {
			var text string
			if !msgs.Receive(ctx, &text) {
				return
			}
			notes = append(notes, text)
		}
	})

	var plan ReportPlan
	err := workflow.ExecuteActivity(ctx, a.PlanReport, run).Get(ctx, &plan)
	if err == nil {
		futures := make([]workflow.Future, len(plan.Questions))
		for i := range plan.Questions {
			futures[i] = workflow.ExecuteActivity(ctx, a.ResearchQuestion, plan, i, notes)
		}
		results := make([]QuestionNotes, len(futures))
		for i, f := range futures {
			if ferr := f.Get(ctx, &results[i]); ferr != nil && err == nil {
				err = ferr
			}
		}
		if err == nil {
			err = workflow.ExecuteActivity(ctx, a.WriteReport, plan, results, notes).Get(ctx, nil)
		}
	}
	if err != nil {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		kind := "failed"
		if temporal.IsCanceledError(err) {
			kind = "cancelled"
		}
		_ = workflow.ExecuteActivity(dctx, a.AbortReport, run, kind).Get(dctx, nil)
		return err
	}
	// The report is already saved; a failed push must not fail the task.
	_ = workflow.ExecuteActivity(ctx, a.NotifyReport, plan).Get(ctx, nil)
	_ = workflow.ExecuteActivity(ctx, a.NotifyQuota, plan.UserID).Get(ctx, nil)
	return nil
}

// NotifyReport tells the user a report is ready; reports take minutes and users leave.
func (a *Activities) NotifyReport(ctx context.Context, plan ReportPlan) error {
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: plan.ReportID, UserID: plan.UserID})
	if err != nil {
		return err
	}
	return a.Push.Notify(ctx, plan.UserID, push.Notification{
		Category: push.CategoryReport, Title: "报告已完成", TitleEn: "Your report is ready", Body: r.Title,
		DeepLink: "zeus://report/" + plan.ReportID.String(),
	})
}

var reportPrompts = texttemplate.Must(texttemplate.New("report").Funcs(texttemplate.FuncMap{"join": strings.Join}).
	ParseFS(prompts.FS, "report/*.md"))

func render(l i18n.Lang, name string, data any) (string, error) {
	var b bytes.Buffer
	err := reportPrompts.ExecuteTemplate(&b, name, data)
	return b.String() + i18n.OutputInstruction(l), err
}

// uiLang is the user's display language; a lookup failure falls back to Chinese.
func (a *Activities) uiLang(ctx context.Context, userID uuid.UUID) i18n.Lang {
	u, err := a.Q.GetUser(ctx, userID)
	if err != nil {
		return i18n.ZH
	}
	return i18n.Parse(u.UiLanguage)
}

func (a *Activities) PlanReport(ctx context.Context, run orch.ReportRun) (ReportPlan, error) {
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: run.ReportID, UserID: run.UserID})
	if err != nil {
		return ReportPlan{}, err
	}
	if err := a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeStatus, Status: "running"}); err != nil {
		return ReportPlan{}, err
	}
	b := budgets[r.Depth]
	lang, ui := i18n.Parse(r.Language), a.uiLang(ctx, run.UserID)
	userMarket := market.Other
	if u, err := a.Q.GetUser(ctx, run.UserID); err == nil {
		userMarket = market.Normalize(u.Market)
	}
	interests, _ := a.Q.ListInterestMemories(ctx, db.ListInterestMemoriesParams{UserID: run.UserID, Limit: 10})
	sys, err := render(lang, "plan.md", map[string]any{"Questions": b.Questions, "Focuses": r.Focuses, "Interests": interests, "Lang": string(lang), "Market": userMarket})
	if err != nil {
		return ReportPlan{}, err
	}
	var out struct {
		Title     string     `json:"title"`
		Questions []Question `json:"questions"`
	}
	if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
		Purpose: "report.plan", Tier: llm.TierStrong, System: sys,
		Messages: []llm.Message{{Role: "user", Content: r.Topic}}, UserID: &run.UserID, TaskID: &run.TaskID,
	}, &out); err != nil {
		return ReportPlan{}, err
	}
	if len(out.Questions) == 0 {
		out.Questions = []Question{{Question: r.Topic, Queries: []string{r.Topic}}}
	}
	if len(out.Questions) > b.Questions {
		out.Questions = out.Questions[:b.Questions]
	}
	for i := range out.Questions {
		out.Questions[i].Regions = questionRegions(out.Questions[i].Regions, userMarket)
	}
	plan := ReportPlan{Market: userMarket, UserID: run.UserID, TaskID: run.TaskID, ReportID: r.ID, Topic: r.Topic, Depth: r.Depth, Focuses: r.Focuses,
		Title: clip(strings.TrimSpace(lang.Model(out.Title)), lang.ClipLen(30)), Questions: out.Questions, Lang: lang, UILang: ui}
	if plan.Title == "" {
		plan.Title = r.Title
	}
	steps := make([]events.Step, 0, len(plan.Questions)+1)
	for i, q := range plan.Questions {
		steps = append(steps, researchStep(ui, i, q.Question))
	}
	steps = append(steps, writeStep(ui, len(steps)+1))
	return plan, a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypePlan, Steps: steps})
}

func questionRegions(values []string, fallback string) []string {
	seen := map[string]bool{}
	var out []string
	for _, value := range values {
		region := market.SearchRegion(value, fallback)
		if !seen[region] {
			seen[region] = true
			out = append(out, region)
		}
		if len(out) == 3 {
			break
		}
	}
	if len(out) == 0 {
		out = []string{market.SearchRegion("", fallback)}
	}
	return out
}

func (a *Activities) ResearchQuestion(ctx context.Context, plan ReportPlan, i int, notes []string) (QuestionNotes, error) {
	ctx = calllog.WithScope(ctx, calllog.Scope{UserID: &plan.UserID, TaskID: &plan.TaskID, Purpose: "report.research"})
	q := plan.Questions[i]
	step := researchStep(plan.UILang, i, q.Question)
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepStarted, Step: &step}); err != nil {
		return QuestionNotes{}, err
	}
	b := budgets[plan.Depth]
	seen := map[string]bool{}
	var sources []search.Result
	queries := q.Queries
	if len(queries) == 0 {
		queries = []string{q.Question}
	}
	// One query per region first, then extra query variants; at most three searches total.
	regions := questionRegions(q.Regions, plan.Market)
	count := 0
	var batches [][]search.Result
	for variant := 0; variant < len(queries) && count < 3; variant++ {
		for _, region := range regions {
			if count >= 3 {
				break
			}
			count++
			text := queries[variant]
			if market.Traditional(region) {
				text = i18n.Hant(text)
			}
			res, err := a.Search.Search(ctx, search.Query{Q: clip(text, 400), Limit: b.Sources,
				Region: region, Country: market.Country(region), Lang: market.SearchLang(region)})
			if err != nil {
				return QuestionNotes{}, err
			}
			batches = append(batches, res)
		}
	}
	// Interleave sources so a full first region does not crowd out other regions.
	for row := 0; len(sources) < b.Sources; row++ {
		added := false
		for _, batch := range batches {
			if row >= len(batch) {
				continue
			}
			added = true
			result := batch[row]
			if !seen[result.URL] && len(sources) < b.Sources {
				seen[result.URL] = true
				sources = append(sources, result)
			}
		}
		if !added {
			break
		}
	}
	for _, r := range sources {
		if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeSourceReading, Source: &events.Source{URL: r.URL, Title: r.Title, SiteName: r.SiteName, PublishedAt: r.PublishedAt}}); err != nil {
			return QuestionNotes{}, err
		}
	}
	sources = append(sources, a.callTools(ctx, plan, q)...)
	out := QuestionNotes{Question: q.Question, Sources: sources}
	if len(sources) > 0 {
		sys, err := render(plan.Lang, "research.md", map[string]any{"Title": plan.Title, "Question": q.Question, "Notes": notes})
		if err != nil {
			return QuestionNotes{}, err
		}
		type ps struct {
			Index   int    `json:"index"`
			Title   string `json:"title"`
			Site    string `json:"site,omitempty"`
			Date    string `json:"publishedAt,omitempty"`
			Content string `json:"content"`
		}
		list := make([]ps, len(sources))
		full := a.readPages(ctx, sources, b.FullPages)
		for j, r := range sources {
			list[j] = ps{Index: j + 1, Title: r.Title, Site: r.SiteName, Content: clip(r.Snippet, 2000)}
			if t := full[j]; len(t) > len(r.Snippet) {
				list[j].Content = clip(t, 6000)
			}
			if r.PublishedAt != nil {
				list[j].Date = r.PublishedAt.Format(time.DateOnly)
			}
		}
		raw, _ := json.Marshal(list)
		var res struct {
			Notes string `json:"notes"`
		}
		if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
			Purpose: "report.research", Tier: llm.TierFast, System: sys,
			Messages: []llm.Message{{Role: "user", Content: "<sources>" + string(raw) + "</sources>"}},
			UserID:   &plan.UserID, TaskID: &plan.TaskID,
		}, &res); err != nil {
			return QuestionNotes{}, err
		}
		out.Notes = res.Notes
	}
	step.Detail = fmt.Sprintf(plan.UILang.Pick("读了 %d 个来源", "Read %d sources"), len(sources))
	return out, a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepDone, Step: &step})
}

var citeRe = regexp.MustCompile(`\[(\d{1,3})\]`)

// mergeNotes renumbers each question's local [n] citations into one global source list.
func mergeNotes(results []QuestionNotes) (string, []events.Source) {
	var b strings.Builder
	var all []events.Source
	index := map[string]int{}
	for _, r := range results {
		local := map[int]int{}
		for j, s := range r.Sources {
			g, ok := index[s.URL]
			if !ok {
				all = append(all, events.Source{URL: s.URL, Title: s.Title, SiteName: s.SiteName, PublishedAt: s.PublishedAt})
				g = len(all)
				index[s.URL] = g
			}
			local[j+1] = g
		}
		notes := citeRe.ReplaceAllStringFunc(r.Notes, func(m string) string {
			n, _ := strconv.Atoi(m[1 : len(m)-1])
			if g, ok := local[n]; ok {
				return "[" + strconv.Itoa(g) + "]"
			}
			return ""
		})
		fmt.Fprintf(&b, "### %s\n\n%s\n\n", r.Question, strings.TrimSpace(notes))
	}
	return b.String(), all
}

func (a *Activities) WriteReport(ctx context.Context, plan ReportPlan, results []QuestionNotes, notes []string) error {
	step := writeStep(plan.UILang, len(plan.Questions)+1)
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepStarted, Step: &step}); err != nil {
		return err
	}
	merged, sources := mergeNotes(results)
	sys, err := render(plan.Lang, "write.md", map[string]any{
		"Title": plan.Title, "Topic": plan.Topic, "Focuses": plan.Focuses, "Notes": notes, "LengthHint": reportLengthHint(plan.Lang, plan.Depth),
	})
	if err != nil {
		return err
	}
	var out struct {
		Summary  string `json:"summary"`
		Markdown string `json:"markdown"`
	}
	if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
		Purpose: "report.write", Tier: llm.TierStrong, System: sys,
		Messages: []llm.Message{{Role: "user", Content: "<notes>\n" + merged + "</notes>"}},
		UserID:   &plan.UserID, TaskID: &plan.TaskID,
	}, &out); err != nil {
		return err
	}
	out.Summary, out.Markdown = plan.Lang.Model(out.Summary), plan.Lang.Model(out.Markdown)
	out.Markdown = reporttemplate.FilterCharts(out.Markdown, plan.Lang.Model(merged), len(sources))
	if err := a.streamText(ctx, plan.UserID, plan.TaskID, out.Summary); err != nil {
		return err
	}
	srcJSON, _ := json.Marshal(orEmptySources(sources))
	if _, err := a.Q.CompleteReport(ctx, db.CompleteReportParams{
		ID: plan.ReportID, UserID: plan.UserID, Title: plan.Title, Summary: out.Summary, Markdown: out.Markdown,
		Sources: srcJSON, SourceCount: int32(len(sources)), WordCount: int32(wordCount(plan.Lang, reporttemplate.WithoutCharts(out.Markdown))),
	}); err != nil {
		return err
	}
	step.Detail = fmt.Sprintf(plan.UILang.Pick("%d 字，%d 个来源", "%d words, %d sources"), wordCount(plan.Lang, reporttemplate.WithoutCharts(out.Markdown)), len(sources))
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepDone, Step: &step}); err != nil {
		return err
	}
	return a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeCompleted, ReportID: plan.ReportID.String()})
}

func orEmptySources(s []events.Source) []events.Source {
	if s == nil {
		return []events.Source{}
	}
	return s
}

// streamText sends text as text_delta events in short chunks. The model gateway does not stream
// yet, so chunks are cut from the finished answer.
func (a *Activities) streamText(ctx context.Context, userID, taskID uuid.UUID, text string) error {
	r := []rune(text)
	const chunk = 40
	for i := 0; i < len(r); i += chunk {
		if err := a.emit(ctx, userID, taskID, events.Event{Type: events.TypeTextDelta, Delta: string(r[i:min(i+chunk, len(r))])}); err != nil {
			return err
		}
	}
	return nil
}

func (a *Activities) AbortReport(ctx context.Context, run orch.ReportRun, kind string) error {
	status := "failed"
	if kind == "cancelled" {
		status = "cancelled"
	}
	if err := a.Q.SetReportStatus(ctx, db.SetReportStatusParams{ID: run.ReportID, UserID: run.UserID, Status: status}); err != nil {
		return err
	}
	if kind == "cancelled" {
		return a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeCancelled})
	}
	msg := a.uiLang(ctx, run.UserID).Pick("报告生成失败，本次不计入额度，请稍后重试",
		"The report couldn't be generated. This one doesn't count toward your quota. Please try again later")
	return a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeFailed,
		Error: &events.Error{Code: "internal", Message: msg, Retryable: true}})
}

// FollowupWorkflow answers a question about a finished report and appends the answer to it.
func FollowupWorkflow(ctx workflow.Context, run orch.FollowupRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: modelActivityTimeout,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	err := workflow.ExecuteActivity(ctx, a.AnswerFollowup, run).Get(ctx, nil)
	if err != nil {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		if temporal.IsCanceledError(err) {
			_ = workflow.ExecuteActivity(dctx, a.EmitEvent, run.UserID, run.TaskID, events.Event{Type: events.TypeCancelled}).Get(dctx, nil)
		} else {
			_ = workflow.ExecuteActivity(dctx, a.FailFollowup, run).Get(dctx, nil)
		}
		return err
	}
	_ = workflow.ExecuteActivity(ctx, a.NotifyQuota, run.UserID).Get(ctx, nil)
	return nil
}

// FailFollowup reports a failed follow-up in the user's display language.
func (a *Activities) FailFollowup(ctx context.Context, run orch.FollowupRun) error {
	msg := a.uiLang(ctx, run.UserID).Pick("追问失败，请稍后重试", "The follow-up failed. Please try again later")
	return a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeFailed, Error: &events.Error{Code: "internal", Message: msg, Retryable: true}})
}

func (a *Activities) AnswerFollowup(ctx context.Context, run orch.FollowupRun) error {
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: run.ReportID, UserID: run.UserID})
	if err != nil {
		return err
	}
	lang, ui := i18n.Parse(r.Language), a.uiLang(ctx, run.UserID)
	step := events.Step{Index: 1, Title: ui.Pick("回答追问", "Answering your follow-up"), Tool: "write"}
	for _, ev := range []events.Event{
		{Type: events.TypeStatus, Status: "running"},
		{Type: events.TypePlan, Steps: []events.Step{step}},
		{Type: events.TypeStepStarted, Step: &step},
	} {
		if err := a.emit(ctx, run.UserID, run.TaskID, ev); err != nil {
			return err
		}
	}
	sys, err := render(lang, "followup.md", map[string]any{"Markdown": clip(r.Markdown, 30000)})
	if err != nil {
		return err
	}
	var out struct {
		Answer string `json:"answer"`
	}
	if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
		Purpose: "report.followup", Tier: llm.TierStrong, System: sys,
		Messages: []llm.Message{{Role: "user", Content: run.Question}}, UserID: &run.UserID, TaskID: &run.TaskID,
	}, &out); err != nil {
		return err
	}
	out.Answer = reporttemplate.FilterCharts(lang.Model(out.Answer), "", 0)
	if err := a.streamText(ctx, run.UserID, run.TaskID, out.Answer); err != nil {
		return err
	}
	if err := a.Q.AppendReportMarkdown(ctx, db.AppendReportMarkdownParams{
		ID: r.ID, UserID: run.UserID, Markdown: "\n\n## " + lang.Pick("追问：", "Follow-up: ") + run.Question + "\n\n" + out.Answer + "\n",
	}); err != nil {
		return err
	}
	if err := a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeStepDone, Step: &step}); err != nil {
		return err
	}
	return a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeCompleted, ReportID: r.ID.String()})
}

// EmitEvent publishes one event; used by workflows for terminal events after a failure.
func (a *Activities) EmitEvent(ctx context.Context, userID, taskID uuid.UUID, ev events.Event) error {
	return a.emit(ctx, userID, taskID, ev)
}

// readPages fetches the full text of the first n sources; a page that fails keeps its snippet.
func (a *Activities) readPages(ctx context.Context, sources []search.Result, n int) []string {
	out := make([]string, len(sources))
	if a.Browse == nil {
		return out
	}
	var wg sync.WaitGroup
	for i := range sources[:min(n, len(sources))] {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if p, err := a.Browse.Read(ctx, sources[i].URL); err == nil {
				out[i] = p.Text
			}
		}()
	}
	wg.Wait()
	return out
}

const maxToolCalls = 2

// callTools lets the model pick up to two read-only connector calls for a sub-question and
// returns their results as extra sources. Any failure just means no extra sources.
func (a *Activities) callTools(ctx context.Context, plan ReportPlan, q Question) []search.Result {
	if a.Tools == nil {
		return nil
	}
	specs, err := a.Tools.Available(ctx, plan.UserID)
	if err != nil || len(specs) == 0 {
		return nil
	}
	sys, err := render(plan.Lang, "tools.md", map[string]any{"Title": plan.Title, "Question": q.Question})
	if err != nil {
		return nil
	}
	in, _ := json.Marshal(map[string]any{"question": q.Question, "tools": specs})
	var pick struct {
		Calls []struct {
			Connector string         `json:"connector"`
			Tool      string         `json:"tool"`
			Arguments map[string]any `json:"arguments"`
		} `json:"calls"`
	}
	if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
		Purpose: "report.tools", Tier: llm.TierFast, System: sys,
		Messages: []llm.Message{{Role: "user", Content: "<tools>" + string(in) + "</tools>"}},
		UserID:   &plan.UserID, TaskID: &plan.TaskID,
	}, &pick); err != nil {
		return nil
	}
	names := map[string]string{}
	for _, s := range specs {
		names[s.Connector] = s.ConnectorName
	}
	var out []search.Result
	for _, c := range pick.Calls[:min(maxToolCalls, len(pick.Calls))] {
		text, err := a.Tools.Call(ctx, plan.UserID, c.Connector, c.Tool, c.Arguments)
		if err != nil || strings.TrimSpace(text) == "" {
			continue
		}
		r := search.Result{
			Title: names[c.Connector] + "：" + c.Tool, URL: "mcp://" + c.Connector + "/" + c.Tool,
			Snippet: clip(text, 2000), SiteName: names[c.Connector],
		}
		_ = a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeSourceReading,
			Source: &events.Source{URL: r.URL, Title: r.Title, SiteName: r.SiteName}})
		out = append(out, r)
	}
	return out
}
