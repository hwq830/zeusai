package workflows

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/search"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func TestReportStepsCoverEveryStage(t *testing.T) {
	questions := []Question{{Question: "市场规模"}, {Question: "竞争格局"}, {Question: "政策"}}
	steps := reportSteps(i18n.ZH, questions)
	require.Len(t, steps, len(questions)+7)
	for i, step := range steps {
		require.Equal(t, i+1, step.Index, "step %d is not contiguous", i)
		require.NotEmpty(t, step.Title)
	}
	require.Equal(t, "制定调研计划", steps[0].Title)
	require.Equal(t, "调研：市场规模", steps[1].Title)
	require.Equal(t, "调研：政策", steps[3].Title)
	require.Equal(t, "撰写报告", steps[4].Title)
	require.Equal(t, "编排图表", steps[5].Title)
	require.Equal(t, "质量审查", steps[6].Title)
	require.Equal(t, "修订", steps[7].Title)
	require.Equal(t, "复审", steps[8].Title)
	require.Equal(t, "发布", steps[9].Title)
}

func TestReportStageDetails(t *testing.T) {
	require.Equal(t, "拆成 7 个问题", planDetail(i18n.ZH, 7))
	require.Equal(t, "Split into 7 questions", planDetail(i18n.EN, 7))
	require.Equal(t, "规划了 3 张图", visualsDetail(i18n.ZH, 3))
	require.Equal(t, "未发现适合配图的段落", visualsDetail(i18n.ZH, 0))
	require.Equal(t, "No paragraphs worth a chart", visualsDetail(i18n.EN, 0))
	require.Equal(t, "未发现问题", reviewDetail(i18n.ZH, reportReview{Status: "pass"}))
	require.Equal(t, "发现 2 个问题（1 个高优先）", reviewDetail(i18n.ZH, reportReview{Status: "revise", Issues: []reviewIssue{
		{Severity: "high"}, {Severity: "medium"},
	}}))
	require.Equal(t, "审查暂不可用", reviewDetail(i18n.ZH, reportReview{Status: "unavailable"}))
	require.Equal(t, "审查通过，无需修订", reviseDetail(i18n.ZH, "skipped"))
	require.Equal(t, "修订完成", reviseDetail(i18n.ZH, "done"))
	require.Equal(t, "复审通过", recheckDetail(i18n.ZH, true))
	require.Equal(t, "复审未通过（已按现有策略发布）", recheckDetail(i18n.ZH, false))
	require.Equal(t, "已发布", publishDetail(i18n.ZH))
	require.Equal(t, "Read 2 sources · 3 new", researchStepDetail(i18n.EN, 2, 3))
}

type capturedStep struct {
	typ  string
	step events.Step
}

func newProgressEnv(t *testing.T) (*testsuite.TestWorkflowEnvironment, *Activities, *[]capturedStep, *[]events.Event) {
	t.Helper()
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	captured := &[]capturedStep{}
	all := &[]events.Event{}
	env.OnActivity(a.EmitEvent, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		ev := args.Get(3).(events.Event)
		*all = append(*all, ev)
		if ev.Step != nil {
			*captured = append(*captured, capturedStep{typ: ev.Type, step: *ev.Step})
		}
	}).Return(nil)
	env.OnActivity(a.NotifyReport, mock.Anything, mock.Anything).Return(nil).Maybe()
	env.OnActivity(a.NotifyQuota, mock.Anything, mock.Anything).Return(nil).Maybe()
	return env, a, captured, all
}

func runReportWorkflow(t *testing.T, env *testsuite.TestWorkflowEnvironment, plan ReportPlan) {
	t.Helper()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		return ReportWorkflow(ctx, orch.ReportRun{UserID: plan.UserID, TaskID: plan.TaskID, ReportID: plan.ReportID})
	})
	require.True(t, env.IsWorkflowCompleted())
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func progressPlan(t *testing.T, questions ...Question) ReportPlan {
	t.Helper()
	return ReportPlan{
		UserID: uuid.New(), TaskID: uuid.New(), ReportID: uuid.New(),
		Lang: i18n.ZH, UILang: i18n.ZH, Questions: questions, ProgressStages: true,
	}
}

// stepsByIndex groups the captured events so a test can assert both the pair and its order.
func stepsByIndex(evs []capturedStep) map[int][]capturedStep {
	out := map[int][]capturedStep{}
	for _, ev := range evs {
		out[ev.step.Index] = append(out[ev.step.Index], ev)
	}
	return out
}

func requireStartedAndDone(t *testing.T, events []capturedStep, index int) {
	t.Helper()
	pair := stepsByIndex(events)[index]
	require.Len(t, pair, 2, "step %d must have exactly step_started and step_done", index)
	require.Equal(t, "step_started", pair[0].typ)
	require.Equal(t, "step_done", pair[1].typ)
}

func stepDetail(t *testing.T, events []capturedStep, index int) string {
	t.Helper()
	pair := stepsByIndex(events)[index]
	require.Len(t, pair, 2)
	return pair[1].step.Detail
}

func TestReportWorkflowEmitsAllStagesWhenReviewPasses(t *testing.T) {
	plan := progressPlan(t, Question{Question: "市场规模"}, Question{Question: "竞争格局"})
	env, a, captured, all := newProgressEnv(t)
	first := QuestionNotes{Question: "市场规模", Sources: []search.Result{{URL: "https://a"}, {URL: "https://b"}}}
	second := QuestionNotes{Question: "竞争格局", Sources: []search.Result{{URL: "https://b"}, {URL: "https://c"}}}
	findings := []QuestionNotes{first, second}
	draft := reportDraft{Summary: "s", Markdown: "## 一、市场\n\n正文"}

	env.OnActivity(a.PlanReport, mock.Anything, mock.Anything).Return(plan, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 0, mock.Anything).Return(first, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 1, mock.Anything).Return(second, nil).Once()
	env.OnActivity(a.DraftReport, mock.Anything, plan, findings, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, plan, findings, mock.Anything).
		Return([]reporttemplate.VisualSpec{{}, {}}, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, plan, findings, draft).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, plan, findings, mock.Anything, mock.MatchedBy(func(r reportReview) bool {
		return r.Status == "pass"
	}), reportReviewResolved).Return(nil).Once()
	runReportWorkflow(t, env, plan)

	// N+7 steps, each with a real started/done pair.
	require.Len(t, plan.Questions, 2)
	for i := 1; i <= len(plan.Questions)+7; i++ {
		requireStartedAndDone(t, *captured, i)
	}
	require.Equal(t, "拆成 2 个问题", stepDetail(t, *captured, 1))
	require.Equal(t, "读了 2 个来源 · 新增 2 个", stepDetail(t, *captured, 2))
	require.Equal(t, "读了 2 个来源 · 新增 1 个", stepDetail(t, *captured, 3))
	require.Regexp(t, `^\d+ 字，3 个来源$`, stepDetail(t, *captured, 4))
	require.Equal(t, "规划了 2 张图", stepDetail(t, *captured, 5))
	require.Equal(t, "未发现问题", stepDetail(t, *captured, 6))
	require.Equal(t, "审查通过，无需修订", stepDetail(t, *captured, 7))
	require.Equal(t, "复审通过", stepDetail(t, *captured, 8))
	require.Equal(t, "已发布", stepDetail(t, *captured, 9))
	// Completion is emitted after the last step so the task never flips back to running.
	last := (*all)[len(*all)-1]
	require.Equal(t, events.TypeCompleted, last.Type)
	require.Equal(t, plan.ReportID.String(), last.ReportID)
}

func TestReportWorkflowEmitsRevisionStagesWhenReviewFails(t *testing.T) {
	plan := progressPlan(t, Question{Question: "市场规模"})
	env, a, captured, _ := newProgressEnv(t)
	first := QuestionNotes{Question: "市场规模", Sources: []search.Result{{URL: "https://a"}}}
	draft := reportDraft{Summary: "s", Markdown: "## 一、市场\n\n初稿"}
	revised := reportDraft{Summary: "s", Markdown: "## 一、市场\n\n修订稿"}
	review := reportReview{Status: "revise", Issues: []reviewIssue{
		{Severity: "high", Evidence: "证据", Suggestion: "建议"},
		{Severity: "medium", Evidence: "证据", Suggestion: "建议"},
	}}

	env.OnActivity(a.PlanReport, mock.Anything, mock.Anything).Return(plan, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 0, mock.Anything).Return(first, nil).Once()
	env.OnActivity(a.DraftReport, mock.Anything, plan, []QuestionNotes{first}, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, plan, mock.Anything, mock.Anything).Return([]reporttemplate.VisualSpec(nil), nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, plan, mock.Anything, draft).Return(review, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, plan, mock.Anything, draft, review).Return(revised, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, plan, mock.Anything, revised).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, plan, mock.Anything, mock.Anything, mock.Anything, reportReviewResolved).Return(nil).Once()
	runReportWorkflow(t, env, plan)

	for i := 1; i <= len(plan.Questions)+7; i++ {
		requireStartedAndDone(t, *captured, i)
	}
	require.Equal(t, "发现 2 个问题（1 个高优先）", stepDetail(t, *captured, 5))
	require.Equal(t, "修订完成", stepDetail(t, *captured, 6))
	require.Equal(t, "复审通过", stepDetail(t, *captured, 7))
	require.Equal(t, "已发布", stepDetail(t, *captured, 8))
	// The chart step still completes, and says so when nothing is worth a chart.
	require.Equal(t, "未发现适合配图的段落", stepDetail(t, *captured, 4))
}

func TestReportWorkflowReportsFailedRecheck(t *testing.T) {
	plan := progressPlan(t, Question{Question: "市场规模"})
	env, a, captured, _ := newProgressEnv(t)
	first := QuestionNotes{Question: "市场规模", Sources: []search.Result{{URL: "https://a"}}}
	draft := reportDraft{Summary: "s", Markdown: "## 一、市场\n\n初稿"}
	review := reportReview{Status: "revise", Issues: []reviewIssue{{Severity: "high", Evidence: "e", Suggestion: "s"}}}

	env.OnActivity(a.PlanReport, mock.Anything, mock.Anything).Return(plan, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 0, mock.Anything).Return(first, nil).Once()
	env.OnActivity(a.DraftReport, mock.Anything, plan, []QuestionNotes{first}, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, plan, mock.Anything, mock.Anything).Return([]reporttemplate.VisualSpec(nil), nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, plan, mock.Anything, draft).Return(review, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, plan, mock.Anything, draft, review).Return(draft, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, plan, mock.Anything, mock.Anything).Return(review, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, plan, mock.Anything, mock.Anything, mock.Anything, reportReviewUnresolved).Return(nil).Once()
	runReportWorkflow(t, env, plan)

	require.Equal(t, "复审未通过（已按现有策略发布）", stepDetail(t, *captured, 7))
	require.Equal(t, "已发布", stepDetail(t, *captured, 8))
}

func TestReportWorkflowParallelResearchNeverCompletesAhead(t *testing.T) {
	plan := progressPlan(t, Question{Question: "Q1"}, Question{Question: "Q2"}, Question{Question: "Q3"})
	env, a, captured, _ := newProgressEnv(t)
	// Two sources each; the third question only repeats URLs the earlier steps already read.
	sources := func(urls ...string) []search.Result {
		out := make([]search.Result, len(urls))
		for i, u := range urls {
			out[i] = search.Result{URL: u}
		}
		return out
	}
	env.OnActivity(a.PlanReport, mock.Anything, mock.Anything).Return(plan, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 0, mock.Anything).
		Return(QuestionNotes{Question: "Q1", Sources: sources("https://a", "https://b")}, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 1, mock.Anything).
		Return(QuestionNotes{Question: "Q2", Sources: sources("https://b", "https://c")}, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 2, mock.Anything).
		Return(QuestionNotes{Question: "Q3", Sources: sources("https://a", "https://c")}, nil).Once()
	env.OnActivity(a.DraftReport, mock.Anything, plan, mock.Anything, mock.Anything).
		Return(reportDraft{Summary: "s", Markdown: "## 一、市场\n\n正文"}, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, plan, mock.Anything, mock.Anything).Return([]reporttemplate.VisualSpec(nil), nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, plan, mock.Anything, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, plan, mock.Anything, mock.Anything, mock.Anything, mock.Anything).Return(nil).Once()
	runReportWorkflow(t, env, plan)

	// The plan step finishes before research starts, and every research step starts before
	// any of them completes: parallel steps are running together, not inferred from progress.
	firstResearchStart := -1
	lastResearchStart := -1
	firstResearchDone := -1
	planDone := -1
	for i, ev := range *captured {
		switch {
		case ev.step.Index == 1 && ev.typ == "step_done":
			planDone = i
		case ev.step.Index >= 2 && ev.step.Index <= 4 && ev.typ == "step_started":
			if firstResearchStart < 0 {
				firstResearchStart = i
			}
			lastResearchStart = i
		case ev.step.Index >= 2 && ev.step.Index <= 4 && ev.typ == "step_done":
			if firstResearchDone < 0 {
				firstResearchDone = i
			}
		}
	}
	require.GreaterOrEqual(t, planDone, 0)
	require.Less(t, planDone, firstResearchStart)
	require.GreaterOrEqual(t, lastResearchStart, 0)
	require.Greater(t, firstResearchDone, lastResearchStart, "a research step completed while another was still starting")

	// Each research step completes exactly once, and shared URLs only count once.
	require.Equal(t, "读了 2 个来源 · 新增 2 个", stepDetail(t, *captured, 2))
	require.Equal(t, "读了 2 个来源 · 新增 1 个", stepDetail(t, *captured, 3))
	require.Equal(t, "读了 2 个来源 · 均为已读", stepDetail(t, *captured, 4))
}

func TestReportWorkflowAbortsWhenResearchFails(t *testing.T) {
	plan := progressPlan(t, Question{Question: "Q1"})
	env, a, _, _ := newProgressEnv(t)
	env.OnActivity(a.PlanReport, mock.Anything, mock.Anything).Return(plan, nil).Once()
	env.OnActivity(a.ResearchQuestion, mock.Anything, plan, 0, mock.Anything).
		Return(QuestionNotes{}, temporal.NewNonRetryableApplicationError("research failed", "test", nil)).Once()
	env.OnActivity(a.AbortReport, mock.Anything, mock.Anything, "failed").Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		return ReportWorkflow(ctx, orch.ReportRun{UserID: plan.UserID, TaskID: plan.TaskID, ReportID: plan.ReportID})
	})
	require.True(t, env.IsWorkflowCompleted())
	require.Error(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
