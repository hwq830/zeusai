package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/search"
)

const FollowupWorkflowName = "FollowupWorkflow"

// depthBudget sizes a report: sub-questions, sources per question and target length.
type depthBudget struct {
	Questions, Sources int
	LengthHint         string
}

var budgets = map[string]depthBudget{
	"quick":    {3, 4, "1500 字左右"},
	"standard": {5, 6, "3000 字左右"},
	"deep":     {7, 8, "5000 字左右"},
}

type Question struct {
	Question string   `json:"question"`
	Queries  []string `json:"queries"`
}

type ReportPlan struct {
	UserID    uuid.UUID
	TaskID    uuid.UUID
	ReportID  uuid.UUID
	Topic     string
	Depth     string
	Focuses   []string
	Title     string
	Questions []Question
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
		StartToCloseTimeout: 5 * time.Minute,
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
	return nil
}

// NotifyReport tells the user a report is ready; reports take minutes and users leave.
func (a *Activities) NotifyReport(ctx context.Context, plan ReportPlan) error {
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: plan.ReportID, UserID: plan.UserID})
	if err != nil {
		return err
	}
	return a.Push.Notify(ctx, plan.UserID, push.Notification{
		Title: "报告已完成", Body: r.Title, DeepLink: "zeus://report/" + plan.ReportID.String(),
	})
}

var reportPrompts = texttemplate.Must(texttemplate.New("report").Funcs(texttemplate.FuncMap{"join": strings.Join}).
	ParseFS(prompts.FS, "report/*.md"))

func render(name string, data any) (string, error) {
	var b bytes.Buffer
	err := reportPrompts.ExecuteTemplate(&b, name, data)
	return b.String(), err
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
	interests, _ := a.Q.ListInterestMemories(ctx, db.ListInterestMemoriesParams{UserID: run.UserID, Limit: 10})
	sys, err := render("plan.md", map[string]any{"Questions": b.Questions, "Focuses": r.Focuses, "Interests": interests})
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
	plan := ReportPlan{UserID: run.UserID, TaskID: run.TaskID, ReportID: r.ID, Topic: r.Topic, Depth: r.Depth, Focuses: r.Focuses,
		Title: clip(strings.TrimSpace(out.Title), 30), Questions: out.Questions}
	if plan.Title == "" {
		plan.Title = r.Title
	}
	steps := make([]events.Step, 0, len(plan.Questions)+1)
	for i, q := range plan.Questions {
		steps = append(steps, events.Step{Index: i + 1, Title: "调研：" + clip(q.Question, 40), Tool: "search"})
	}
	steps = append(steps, events.Step{Index: len(steps) + 1, Title: "撰写报告", Tool: "write"})
	return plan, a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypePlan, Steps: steps})
}

func (a *Activities) ResearchQuestion(ctx context.Context, plan ReportPlan, i int, notes []string) (QuestionNotes, error) {
	q := plan.Questions[i]
	step := events.Step{Index: i + 1, Title: "调研：" + clip(q.Question, 40), Tool: "search"}
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
	for _, text := range queries[:min(3, len(queries))] {
		res, err := a.Search.Search(ctx, search.Query{Q: clip(text, 60), Limit: b.Sources})
		if err != nil {
			return QuestionNotes{}, err
		}
		for _, r := range res {
			if !seen[r.URL] && len(sources) < b.Sources {
				seen[r.URL] = true
				sources = append(sources, r)
			}
		}
	}
	for _, r := range sources {
		if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeSourceReading, Source: &events.Source{URL: r.URL, Title: r.Title, SiteName: r.SiteName, PublishedAt: r.PublishedAt}}); err != nil {
			return QuestionNotes{}, err
		}
	}
	out := QuestionNotes{Question: q.Question, Sources: sources}
	if len(sources) > 0 {
		sys, err := render("research.md", map[string]any{"Title": plan.Title, "Question": q.Question, "Notes": notes})
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
		for j, r := range sources {
			list[j] = ps{Index: j + 1, Title: r.Title, Site: r.SiteName, Content: clip(r.Snippet, 2000)}
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
	step.Detail = fmt.Sprintf("读了 %d 个来源", len(sources))
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
	step := events.Step{Index: len(plan.Questions) + 1, Title: "撰写报告", Tool: "write"}
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepStarted, Step: &step}); err != nil {
		return err
	}
	merged, sources := mergeNotes(results)
	sys, err := render("write.md", map[string]any{
		"Title": plan.Title, "Topic": plan.Topic, "Focuses": plan.Focuses, "Notes": notes, "LengthHint": budgets[plan.Depth].LengthHint,
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
	if err := a.streamText(ctx, plan.UserID, plan.TaskID, out.Summary); err != nil {
		return err
	}
	srcJSON, _ := json.Marshal(orEmptySources(sources))
	if _, err := a.Q.CompleteReport(ctx, db.CompleteReportParams{
		ID: plan.ReportID, UserID: plan.UserID, Title: plan.Title, Summary: out.Summary, Markdown: out.Markdown,
		Sources: srcJSON, SourceCount: int32(len(sources)), WordCount: int32(utf8.RuneCountInString(out.Markdown)),
	}); err != nil {
		return err
	}
	step.Detail = fmt.Sprintf("%d 字，%d 个来源", utf8.RuneCountInString(out.Markdown), len(sources))
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
	return a.emit(ctx, run.UserID, run.TaskID, events.Event{Type: events.TypeFailed,
		Error: &events.Error{Code: "internal", Message: "报告生成失败，本次不计入额度，请稍后重试", Retryable: true}})
}

// FollowupWorkflow answers a question about a finished report and appends the answer to it.
func FollowupWorkflow(ctx workflow.Context, run orch.FollowupRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	err := workflow.ExecuteActivity(ctx, a.AnswerFollowup, run).Get(ctx, nil)
	if err != nil {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		ev := events.Event{Type: events.TypeFailed, Error: &events.Error{Code: "internal", Message: "追问失败，请稍后重试", Retryable: true}}
		if temporal.IsCanceledError(err) {
			ev = events.Event{Type: events.TypeCancelled}
		}
		_ = workflow.ExecuteActivity(dctx, a.EmitEvent, run.UserID, run.TaskID, ev).Get(dctx, nil)
	}
	return err
}

func (a *Activities) AnswerFollowup(ctx context.Context, run orch.FollowupRun) error {
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: run.ReportID, UserID: run.UserID})
	if err != nil {
		return err
	}
	step := events.Step{Index: 1, Title: "回答追问", Tool: "write"}
	for _, ev := range []events.Event{
		{Type: events.TypeStatus, Status: "running"},
		{Type: events.TypePlan, Steps: []events.Step{step}},
		{Type: events.TypeStepStarted, Step: &step},
	} {
		if err := a.emit(ctx, run.UserID, run.TaskID, ev); err != nil {
			return err
		}
	}
	sys, err := render("followup.md", map[string]any{"Markdown": clip(r.Markdown, 30000)})
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
	if err := a.streamText(ctx, run.UserID, run.TaskID, out.Answer); err != nil {
		return err
	}
	if err := a.Q.AppendReportMarkdown(ctx, db.AppendReportMarkdownParams{
		ID: r.ID, UserID: run.UserID, Markdown: "\n\n## 追问：" + run.Question + "\n\n" + out.Answer + "\n",
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
