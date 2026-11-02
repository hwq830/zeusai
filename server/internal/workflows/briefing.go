package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	texttemplate "text/template"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/browse"
	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/market"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/pdf"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/schedule"
	"github.com/hwq830/zeusai/server/internal/search"
	tpl "github.com/hwq830/zeusai/server/internal/template"
)

const (
	BriefingWorkflowName = "BriefingWorkflow"
	ReportWorkflowName   = "ReportWorkflow"
	// inactiveAfter pauses a schedule whose issues nobody has opened for this long.
	inactiveAfter = 7 * 24 * time.Hour
)

// Activities holds the dependencies of every activity. Register a *Activities with the worker.
type Activities struct {
	Q      *db.Queries
	Bus    *events.Bus
	LLM    llm.Provider
	Search search.Searcher
	Browse browse.Reader // nil: research uses search snippets only
	Tools  Tools         // nil: no connector tools
	PDF    pdf.Renderer  // nil: PDF export fails with a clear error
	Blob   blob.Store
	Push   push.Notifier
	// Notify sends operator campaigns; nil fails campaign activities.
	Notify *notify.Service
	// Quota sends token usage reminders; nil sends none.
	Quota *quota.Service
	Now   func() time.Time
}

// Tools is the read-only connector toolbox (connector.Toolbox in production).
type Tools interface {
	Available(ctx context.Context, userID uuid.UUID) ([]connector.ToolSpec, error)
	Call(ctx context.Context, userID uuid.UUID, connectorID, tool string, args map[string]any) (string, error)
}

func (a *Activities) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

// SectionPlan is one column of the issue and the searches that feed it.
type SectionPlan struct {
	Key         string
	Title       string
	Instruction string
	MaxItems    int
	Private     bool
	Queries     []search.Query
}

type BriefingPlan struct {
	Skip        bool
	UserID      uuid.UUID
	TaskID      uuid.UUID
	BriefingID  uuid.UUID
	SubID       uuid.UUID
	Title       string
	Length      string
	FreshDays   int
	Interests   []string
	Sections    []SectionPlan
	PushEnabled bool
	PushAt      time.Time
	// Lang is the user's display language when the issue started; briefings follow it.
	Lang i18n.Lang
}

type SectionResult struct {
	Key     string
	Title   string
	Private bool
	Items   []briefing.Item
	Sources int
}

// BriefingWorkflow produces one issue: plan → research each section in parallel → write the
// headline and save → (scheduled runs) wait for the delivery time and push.
func BriefingWorkflow(ctx workflow.Context, run orch.BriefingRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 3 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})

	var plan BriefingPlan
	if err := workflow.ExecuteActivity(ctx, a.PrepareBriefing, run).Get(ctx, &plan); err != nil {
		return err
	}
	if plan.Skip {
		return nil
	}

	results, err := researchAll(ctx, plan)
	if err == nil {
		err = workflow.ExecuteActivity(ctx, a.FinalizeBriefing, plan, results).Get(ctx, nil)
	}
	if err != nil {
		dctx, _ := workflow.NewDisconnectedContext(ctx)
		kind := "failed"
		if temporal.IsCanceledError(err) {
			kind = "cancelled"
		}
		_ = workflow.ExecuteActivity(dctx, a.AbortBriefing, plan, kind, err.Error()).Get(dctx, nil)
		return err
	}
	_ = workflow.ExecuteActivity(ctx, a.NotifyQuota, plan.UserID).Get(ctx, nil)

	if run.Scheduled && plan.PushEnabled {
		if wait := plan.PushAt.Sub(workflow.Now(ctx)); wait > 0 {
			if err := workflow.Sleep(ctx, wait); err != nil {
				return err
			}
		}
		return workflow.ExecuteActivity(ctx, a.NotifyBriefing, plan).Get(ctx, nil)
	}
	if run.Push && plan.PushEnabled {
		return workflow.ExecuteActivity(ctx, a.NotifyBriefing, plan).Get(ctx, nil)
	}
	return nil
}

func researchAll(ctx workflow.Context, plan BriefingPlan) ([]SectionResult, error) {
	var a *Activities
	futures := make([]workflow.Future, len(plan.Sections))
	for i := range plan.Sections {
		futures[i] = workflow.ExecuteActivity(ctx, a.ResearchSection, plan, i)
	}
	results := make([]SectionResult, len(plan.Sections))
	var firstErr error
	for i, f := range futures {
		if err := f.Get(ctx, &results[i]); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return results, firstErr
}

func freshDays(freq string) int {
	switch freq {
	case "weekly":
		return 8
	case "monthly":
		return 32
	}
	return 2
}

func clip(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// buildSections turns a subscription (and its template, if any) into section plans. mkt is the
// subscriber's market: it picks the template's sources and the search region; Traditional
// Chinese markets search in Traditional Chinese.
func buildSections(sub briefing.Subscription, t *tpl.Template, fresh int, lang i18n.Lang, mkt string) []SectionPlan {
	q := func(text, site string) search.Query {
		text = strings.TrimSpace(text)
		if market.Traditional(mkt) {
			text = i18n.Hant(text)
		}
		return search.Query{Q: clip(text, 60), Site: site, FreshDays: fresh, Limit: 6,
			Country: market.Country(mkt), Lang: market.SearchLang(mkt)}
	}
	if t == nil {
		var out []SectionPlan
		for i, topic := range sub.Topics {
			if i == 6 {
				break
			}
			out = append(out, SectionPlan{
				Key: fmt.Sprintf("t%d", i+1), Title: topic, MaxItems: 4,
				Queries: []search.Query{q(topic+lang.Pick(" 最新", " latest"), "")},
			})
		}
		return out
	}
	out := make([]SectionPlan, 0, len(t.Sections))
	for _, s := range t.Sections {
		instr := tpl.Expand(s.Instruction, sub.FieldValues)
		text := instr
		if strings.TrimSpace(strings.Trim(text, "、 ")) == "" {
			text = s.Title
		}
		out = append(out, SectionPlan{Key: s.Key, Title: s.Title, Instruction: instr, MaxItems: s.MaxItems, Private: s.Private, Queries: []search.Query{q(text, "")}})
	}
	// Template-level sources are spread over the sections.
	i := 0
	for _, src := range t.SourcesFor(mkt) {
		var query search.Query
		switch src.Type {
		case "search":
			text := tpl.Expand(src.Value, sub.FieldValues)
			if strings.TrimSpace(text) == "" {
				continue
			}
			query = q(text, "")
		case "site":
			query = q(tpl.Expand(out[i%len(out)].Instruction, sub.FieldValues), src.Value)
		default: // rss and connector sources are read by the report agent, not the briefing path
			continue
		}
		out[i%len(out)].Queries = append(out[i%len(out)].Queries, query)
		i++
	}
	if len(sub.Topics) > 0 {
		out[0].Queries = append(out[0].Queries, q(strings.Join(sub.Topics, " "), ""))
	}
	return out
}

func (a *Activities) PrepareBriefing(ctx context.Context, run orch.BriefingRun) (BriefingPlan, error) {
	row, err := a.Q.GetSubscription(ctx, db.GetSubscriptionParams{ID: run.SubscriptionID, UserID: run.UserID})
	if errors.Is(err, pgx.ErrNoRows) {
		return BriefingPlan{Skip: true}, nil
	}
	if err != nil {
		return BriefingPlan{}, err
	}
	sub := briefing.FromRow(row)
	now := a.now()
	lang := a.uiLang(ctx, run.UserID)
	mkt := market.Other
	if u, err := a.Q.GetUser(ctx, run.UserID); err == nil {
		mkt = market.Normalize(u.Market)
	}

	taskID := run.TaskID
	if run.Scheduled {
		if row.Status != "active" {
			return BriefingPlan{Skip: true}, nil
		}
		if a.inactive(row, now) {
			return BriefingPlan{Skip: true}, a.pauseInactive(ctx, row)
		}
		t, err := a.Q.CreateTask(ctx, db.CreateTaskParams{
			UserID: run.UserID, Kind: "briefing", Title: row.Name, Subtitle: lang.Pick("定时简报", "Scheduled briefing"), Input: json.RawMessage(`{}`),
			SubscriptionID: &row.ID, PromptVersion: prompts.Version,
		})
		if err != nil {
			return BriefingPlan{}, err
		}
		taskID = t.ID
	}

	var t *tpl.Template
	if row.TemplateID != nil {
		tr, err := a.Q.GetTemplateForUser(ctx, db.GetTemplateForUserParams{ID: *row.TemplateID, OwnerID: &run.UserID})
		if err == nil {
			tt, err := tpl.FromRow(tr)
			if err != nil {
				return BriefingPlan{}, err
			}
			tt = tt.Localize(lang)
			t = &tt
		}
	}
	if t == nil && len(sub.Topics) == 0 {
		sub.Topics = []string{sub.Name}
	}

	loc := sub.Schedule.Location()
	date := schedule.LocalDate(now, loc)
	b, err := a.Q.StartBriefing(ctx, db.StartBriefingParams{UserID: run.UserID, SubscriptionID: row.ID, TaskID: &taskID, Title: row.Name, Date: date, Language: string(lang)})
	if err != nil {
		return BriefingPlan{}, err
	}
	if err := a.Q.SetTaskLinks(ctx, db.SetTaskLinksParams{ID: taskID, UserID: run.UserID, BriefingID: &b.ID}); err != nil {
		return BriefingPlan{}, err
	}
	interests, _ := a.Q.ListInterestMemories(ctx, db.ListInterestMemoriesParams{UserID: run.UserID, Limit: 10})

	fresh := freshDays(sub.Schedule.Freq)
	plan := BriefingPlan{
		UserID: run.UserID, TaskID: taskID, BriefingID: b.ID, SubID: row.ID, Title: row.Name, Length: row.Length,
		FreshDays: fresh, Interests: interests, Sections: buildSections(sub, t, fresh, lang, mkt), PushEnabled: row.PushEnabled, PushAt: now, Lang: lang,
	}
	h, m := sub.Schedule.Clock()
	l := now.In(loc)
	if at := time.Date(l.Year(), l.Month(), l.Day(), h, m, 0, 0, loc); at.After(now) && at.Sub(now) < 2*time.Hour {
		plan.PushAt = at
	}

	steps := make([]events.Step, 0, len(plan.Sections)+1)
	for i, s := range plan.Sections {
		steps = append(steps, sectionStep(lang, i, s.Title))
	}
	steps = append(steps, finalizeStep(lang, len(steps)+1))
	if err := a.emit(ctx, run.UserID, taskID, events.Event{Type: events.TypeStatus, Status: "running"}); err != nil {
		return BriefingPlan{}, err
	}
	if err := a.emit(ctx, run.UserID, taskID, events.Event{Type: events.TypePlan, Steps: steps}); err != nil {
		return BriefingPlan{}, err
	}
	return plan, nil
}

func sectionStep(l i18n.Lang, i int, title string) events.Step {
	return events.Step{Index: i + 1, Title: l.Pick("整理「"+title+"」", "Gathering “"+title+"”"), Tool: "search"}
}

func finalizeStep(l i18n.Lang, n int) events.Step {
	return events.Step{Index: n, Title: l.Pick("汇总成稿", "Putting it together"), Tool: "write"}
}

func (a *Activities) inactive(row db.BriefingSubscription, now time.Time) bool {
	if row.LastBriefingID == nil || now.Sub(row.CreatedAt) < inactiveAfter {
		return false
	}
	return row.LastOpenedAt == nil || now.Sub(*row.LastOpenedAt) > inactiveAfter
}

func (a *Activities) pauseInactive(ctx context.Context, row db.BriefingSubscription) error {
	reason := "inactive"
	if _, err := a.Q.UpdateSubscription(ctx, db.UpdateSubscriptionParams{
		ID: row.ID, UserID: row.UserID, Name: row.Name, FieldValues: row.FieldValues, Topics: row.Topics, Schedule: row.Schedule,
		Length: row.Length, PushEnabled: row.PushEnabled, Status: "paused", PausedReason: &reason,
	}); err != nil {
		return err
	}
	// The Temporal schedule keeps firing; PrepareBriefing skips paused subscriptions.
	return a.Push.Notify(ctx, row.UserID, push.Notification{
		Category: push.CategoryService, Title: row.Name + " 已暂停", Body: "你已经一周没看这份简报了，我们先暂停推送。想恢复可以在 App 里打开。",
		TitleEn: row.Name + " is paused", BodyEn: "You haven't opened this briefing for a week, so we've paused it. You can turn it back on in the app.",
		DeepLink: "zeus://briefing-subscription/" + row.ID.String(),
	})
}

var sectionPrompt = texttemplate.Must(texttemplate.New("section").Funcs(texttemplate.FuncMap{"join": strings.Join}).
	ParseFS(prompts.FS, "briefing/section.md")).Lookup("section.md")

var lengthHints = map[string]string{"short": "不超过 40 字", "standard": "60–100 字", "long": "120–200 字"}

var lengthHintsBriefingEN = map[string]string{"short": "under 25 words", "standard": "40–60 words", "long": "80–120 words"}

type promptSource struct {
	Title       string `json:"title"`
	Site        string `json:"site,omitempty"`
	PublishedAt string `json:"publishedAt,omitempty"`
	Snippet     string `json:"snippet"`
}

func (a *Activities) ResearchSection(ctx context.Context, plan BriefingPlan, i int) (SectionResult, error) {
	sec := plan.Sections[i]
	step := sectionStep(plan.Lang, i, sec.Title)
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepStarted, Step: &step}); err != nil {
		return SectionResult{}, err
	}

	seen := map[string]bool{}
	var sources []search.Result
	for _, q := range sec.Queries {
		res, err := a.Search.Search(ctx, q)
		if err != nil {
			return SectionResult{}, err
		}
		for _, r := range res {
			if seen[r.URL] || len(sources) >= 12 {
				continue
			}
			seen[r.URL] = true
			sources = append(sources, r)
		}
	}
	for _, r := range sources[:min(3, len(sources))] {
		if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeSourceReading, Source: &events.Source{URL: r.URL, Title: r.Title, SiteName: r.SiteName, PublishedAt: r.PublishedAt}}); err != nil {
			return SectionResult{}, err
		}
	}

	result := SectionResult{Key: sec.Key, Title: sec.Title, Private: sec.Private, Items: []briefing.Item{}}
	if len(sources) > 0 {
		var sys bytes.Buffer
		if err := sectionPrompt.Execute(&sys, map[string]any{
			"Section": sec, "MaxItems": sec.MaxItems, "LengthHint": plan.Lang.Pick(lengthHints[plan.Length], lengthHintsBriefingEN[plan.Length]),
			"FreshDays": plan.FreshDays, "Interests": plan.Interests,
		}); err != nil {
			return SectionResult{}, err
		}
		sys.WriteString(i18n.OutputInstruction(plan.Lang))
		ps := make([]promptSource, len(sources))
		for j, r := range sources {
			ps[j] = promptSource{Title: r.Title, Site: r.SiteName, Snippet: clip(r.Snippet, 400)}
			if r.PublishedAt != nil {
				ps[j].PublishedAt = r.PublishedAt.Format(time.DateOnly)
			}
		}
		srcJSON, _ := json.Marshal(ps)
		var out struct {
			Items []struct {
				Title         string `json:"title"`
				Summary       string `json:"summary"`
				SourceIndexes []int  `json:"sourceIndexes"`
			} `json:"items"`
		}
		if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
			Purpose: "briefing.section", Tier: llm.TierFast, System: sys.String(),
			Messages: []llm.Message{{Role: "user", Content: "<sources>" + string(srcJSON) + "</sources>"}},
			UserID:   &plan.UserID, TaskID: &plan.TaskID,
		}, &out); err != nil {
			return SectionResult{}, err
		}
		used := map[int]bool{}
		for n, it := range out.Items {
			if n >= sec.MaxItems || strings.TrimSpace(it.Title) == "" {
				break
			}
			item := briefing.Item{ID: fmt.Sprintf("%s-%d", sec.Key, n+1), Title: plan.Lang.Model(it.Title), Summary: plan.Lang.Model(it.Summary), Sources: []events.Source{}, DeepDivable: true}
			for _, idx := range it.SourceIndexes {
				if idx >= 0 && idx < len(sources) {
					r := sources[idx]
					item.Sources = append(item.Sources, events.Source{URL: r.URL, Title: r.Title, SiteName: r.SiteName, PublishedAt: r.PublishedAt})
					used[idx] = true
				}
			}
			if len(item.Sources) == 0 {
				continue // unsourced items are dropped rather than shown
			}
			result.Items = append(result.Items, item)
		}
		result.Sources = len(used)
	}

	step.Detail = fmt.Sprintf(plan.Lang.Pick("%d 条", "%d items"), len(result.Items))
	return result, a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepDone, Step: &step})
}

var headlinePrompt = texttemplate.Must(texttemplate.ParseFS(prompts.FS, "briefing/headline.md")).Lookup("headline.md")

func (a *Activities) FinalizeBriefing(ctx context.Context, plan BriefingPlan, results []SectionResult) error {
	step := finalizeStep(plan.Lang, len(plan.Sections)+1)
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepStarted, Step: &step}); err != nil {
		return err
	}
	sections := make([]briefing.Section, 0, len(results))
	var titles []string
	items, sources := 0, 0
	for _, r := range results {
		if len(r.Items) == 0 {
			continue
		}
		sections = append(sections, briefing.Section{Key: r.Key, Title: r.Title, Private: r.Private, Items: r.Items})
		for _, it := range r.Items {
			titles = append(titles, "- "+it.Title)
		}
		items += len(r.Items)
		sources += r.Sources
	}

	headline := ""
	if len(titles) > 0 {
		var sys bytes.Buffer
		_ = headlinePrompt.Execute(&sys, map[string]any{"Title": plan.Title})
		sys.WriteString(i18n.OutputInstruction(plan.Lang))
		var out struct {
			Headline string `json:"headline"`
		}
		if _, err := llm.CompleteJSON(ctx, a.LLM, llm.Request{
			Purpose: "briefing.headline", Tier: llm.TierFast, System: sys.String(),
			Messages: []llm.Message{{Role: "user", Content: strings.Join(titles, "\n")}},
			UserID:   &plan.UserID, TaskID: &plan.TaskID,
		}, &out); err != nil {
			return err
		}
		headline = clip(plan.Lang.Model(out.Headline), plan.Lang.ClipLen(60))
	} else {
		headline = plan.Lang.Pick("这期没有找到值得推送的新内容", "Nothing new worth sending this time")
	}

	secJSON, _ := json.Marshal(sections)
	if _, err := a.Q.CompleteBriefing(ctx, db.CompleteBriefingParams{
		ID: plan.BriefingID, UserID: plan.UserID, Headline: headline, Sections: secJSON, ItemCount: int32(items), SourceCount: int32(sources),
	}); err != nil {
		return err
	}
	row, err := a.Q.GetSubscription(ctx, db.GetSubscriptionParams{ID: plan.SubID, UserID: plan.UserID})
	if err == nil {
		sub := briefing.FromRow(row)
		next := sub.Schedule.Next(plan.PushAt)
		if err := a.Q.SetSubscriptionLastBriefing(ctx, db.SetSubscriptionLastBriefingParams{ID: plan.SubID, UserID: plan.UserID, LastBriefingID: &plan.BriefingID, NextRunAt: &next}); err != nil {
			return err
		}
	}
	step.Detail = fmt.Sprintf(plan.Lang.Pick("%d 条，%d 个来源", "%d items, %d sources"), items, sources)
	if err := a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeStepDone, Step: &step}); err != nil {
		return err
	}
	return a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeCompleted, BriefingID: plan.BriefingID.String()})
}

// AbortBriefing records a failed or cancelled issue; kind is "failed" or "cancelled".
func (a *Activities) AbortBriefing(ctx context.Context, plan BriefingPlan, kind, reason string) error {
	if err := a.Q.FailBriefing(ctx, db.FailBriefingParams{ID: plan.BriefingID, UserID: plan.UserID}); err != nil {
		return err
	}
	if kind == "cancelled" {
		return a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeCancelled})
	}
	return a.emit(ctx, plan.UserID, plan.TaskID, events.Event{Type: events.TypeFailed,
		Error: &events.Error{Code: "internal", Message: plan.Lang.Pick("简报生成失败，我们会在下一期重试",
			"This briefing couldn't be generated. We'll try again with the next one"), Retryable: true}})
}

func (a *Activities) NotifyBriefing(ctx context.Context, plan BriefingPlan) error {
	b, err := a.Q.GetBriefing(ctx, db.GetBriefingParams{ID: plan.BriefingID, UserID: plan.UserID})
	if err != nil {
		return err
	}
	if b.ItemCount == 0 {
		return nil
	}
	return a.Push.Notify(ctx, plan.UserID, push.Notification{
		Category: push.CategoryBriefing, Title: plan.Title, Body: b.Headline, DeepLink: "zeus://briefing/" + plan.BriefingID.String(),
	})
}
