package workflows

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/search"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	enums "go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"
)

type qualityProvider struct {
	result string
	err    error
}

func (p qualityProvider) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Content: p.result}, p.err
}

func TestVisualRequiresEvidence(t *testing.T) {
	p := ReportPlan{}
	findings := []QuestionNotes{{Question: "Q", Notes: "2025 搜索 25% [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	a := &Activities{LLM: qualityProvider{result: `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":25,"source":1},{"label":"社区","value":80,"source":1}]}]}`}}
	got, err := a.PlanReportVisuals(context.Background(), p, findings)
	require.NoError(t, err)
	require.Empty(t, got)
	findings[0].Notes = "2025 搜索 25% 社区 80% [1]"
	got, err = a.PlanReportVisuals(context.Background(), p, findings)
	require.NoError(t, err)
	require.Contains(t, got, "zeus-chart")
}

func TestVisualRequiresChartsArray(t *testing.T) {
	a := &Activities{LLM: qualityProvider{result: `{}`}}
	_, err := a.PlanReportVisuals(context.Background(), ReportPlan{}, []QuestionNotes{{Question: "Q", Notes: "事实 [1]", Sources: []search.Result{{URL: "https://example.test"}}}})
	require.ErrorContains(t, err, "missing charts array")
}

func TestVisualAcceptsWorldBankGroupedValues(t *testing.T) {
	// Public WDI 2022 population table, in thousands. This is a deterministic
	// evidence fixture, not a live search or a claim that the model always charts it.
	a := &Activities{LLM: qualityProvider{result: `{"charts":[{"type":"bar","title":"Population","unit":"thousands","period":"2022","points":[{"label":"India","value":1417173,"source":1},{"label":"China","value":1412175,"source":1}]}]}`}}
	findings := []QuestionNotes{{Question: "Population", Notes: "Population 2022 (thousands): India 1,417,173; China 1,412,175 [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}}}
	got, err := a.PlanReportVisuals(context.Background(), ReportPlan{}, findings)
	require.NoError(t, err)
	require.Contains(t, got, "```zeus-chart")
}

func TestReviewValidation(t *testing.T) {
	cases := []struct {
		body string
		want string
		bad  bool
	}{
		{`{"status":"pass","issues":[]}`, "pass", false},
		{`{"status":"pass","issues":[{"severity":"high","evidence":"冲突","suggestion":"修正"}]}`, "revise", false},
		{`{"status":"pass","issues":[{"severity":"mystery","evidence":"x","suggestion":"y"}]}`, "", true},
		{`{"status":"revise","issues":[]}`, "", true},
		{`{"status":"pass"}`, "", true},
		{`{}`, "", true},
	}
	for _, tc := range cases {
		a := &Activities{LLM: qualityProvider{result: tc.body}}
		got, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{})
		if tc.bad {
			require.Error(t, err)
		} else {
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Status)
		}
	}
}

func TestQualityPipelineOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name                              string
		first                             reportReview
		reviewErr, revisionErr, secondErr bool
		timeout                           bool
		want                              string
		revise, second, stillOpen         bool
	}{
		{name: "pass", first: reportReview{Status: "pass"}, want: "pass"},
		{name: "review unavailable", reviewErr: true, want: "unavailable"},
		{name: "review timeout", reviewErr: true, timeout: true, want: "unavailable"},
		{name: "one revision passes", first: reportReview{Status: "revise"}, revise: true, second: true, want: "pass"},
		{name: "revision fails", first: reportReview{Status: "revise"}, revise: true, revisionErr: true, want: "revise"},
		{name: "second review fails", first: reportReview{Status: "revise"}, revise: true, second: true, secondErr: true, want: "unavailable"},
		{name: "revision limit reached", first: reportReview{Status: "revise"}, revise: true, second: true, stillOpen: true, want: "revise"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			a := &Activities{}
			env.RegisterActivity(a)
			p := ReportPlan{}
			var findings []QuestionNotes
			var notes []string
			draft := reportDraft{Summary: "original", Markdown: "## 一、规模\n\nbody"}
			revised := reportDraft{Summary: "revised", Markdown: "## 一、规模\n\nfixed"}
			env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
			env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func([]string) bool { return true })).Return([]reporttemplate.VisualSpec(nil), nil).Once()
			if tc.reviewErr {
				failure := error(errors.New("review unavailable"))
				if tc.timeout {
					failure = temporal.NewTimeoutError(enums.TIMEOUT_TYPE_START_TO_CLOSE, nil)
				}
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, draft).Return(reportReview{}, failure).Once()
			} else {
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, draft).Return(tc.first, nil).Once()
			}
			if tc.revise {
				if tc.revisionErr {
					env.OnActivity(a.ReviseReport, mock.Anything, p, findings, draft, tc.first).Return(reportDraft{}, errors.New("revision failed")).Once()
				} else {
					env.OnActivity(a.ReviseReport, mock.Anything, p, findings, draft, tc.first).Return(revised, nil).Once()
				}
			}
			if tc.second {
				if tc.secondErr {
					env.OnActivity(a.ReviewReport, mock.Anything, p, findings, revised).Return(reportReview{}, errors.New("review unavailable")).Once()
				} else if tc.stillOpen {
					env.OnActivity(a.ReviewReport, mock.Anything, p, findings, revised).Return(reportReview{Status: "revise"}, nil).Once()
				} else {
					env.OnActivity(a.ReviewReport, mock.Anything, p, findings, revised).Return(reportReview{Status: "pass"}, nil).Once()
				}
			}
			env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.MatchedBy(func(r reportReview) bool { return r.Status == tc.want }), mock.Anything).Return(nil).Once()
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
				return reportQualityPipeline(ctx, p, findings, notes)
			})
			require.NoError(t, env.GetWorkflowError())
			env.AssertExpectations(t)
		})
	}
}

type sequenceProvider struct {
	responses []llm.Response
	requests  []llm.Request
}

func (s *sequenceProvider) Complete(_ context.Context, r llm.Request) (llm.Response, error) {
	s.requests = append(s.requests, r)
	if len(s.responses) == 0 {
		return llm.Response{}, errors.New("unexpected call")
	}
	out := s.responses[0]
	s.responses = s.responses[1:]
	return out, nil
}
func TestReviewRetriesOnlyTruncatedResponse(t *testing.T) {
	for _, tc := range []struct {
		name   string
		first  llm.Response
		second llm.Response
		calls  int
		failed bool
	}{
		{"provider length", llm.Response{Content: `{"status":"pass","issues":[]}`, FinishReason: "length"}, llm.Response{Content: `{"status":"pass","issues":[]}`}, 2, false},
		{"incomplete at cap", llm.Response{Content: `{"status":"rev`, UsageKnown: true, Usage: llm.Usage{OutputTokens: 2400}}, llm.Response{Content: `{"status":"pass","issues":[]}`}, 2, false},
		{"empty json mode response", llm.Response{Provider: "deepseek", FinishReason: "stop", Content: "  "}, llm.Response{Content: `{"status":"pass","issues":[]}`}, 2, false},
		{"incomplete below cap", llm.Response{Content: `{"status":"rev`, UsageKnown: true, Usage: llm.Usage{OutputTokens: 20}}, llm.Response{}, 1, true},
		{"invalid schema", llm.Response{Content: `{"status":"pass"}`}, llm.Response{}, 1, true},
		{"second truncation", llm.Response{FinishReason: "length"}, llm.Response{FinishReason: "length"}, 2, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sequenceProvider{responses: []llm.Response{tc.first, tc.second}}
			a := Activities{LLM: s}
			got, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{})
			require.Len(t, s.requests, tc.calls)
			require.Equal(t, 2400, s.requests[0].MaxTokens)
			require.True(t, s.requests[0].DisableThinking)
			if tc.calls == 2 {
				wantTokens := 4800
				if tc.name == "empty json mode response" {
					wantTokens = 2400
				}
				require.Equal(t, wantTokens, s.requests[1].MaxTokens)
				require.True(t, s.requests[1].DisableThinking)
			}
			if tc.failed {
				require.Error(t, err)
				require.NotEqual(t, "pass", got.Status)
			} else {
				require.NoError(t, err)
				require.Equal(t, "pass", got.Status)
			}
		})
	}
}
func TestReviewTruncationHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &sequenceProvider{responses: []llm.Response{{FinishReason: "length"}}}
	a := Activities{LLM: s}
	_, err := a.ReviewReport(ctx, ReportPlan{}, nil, reportDraft{})
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, s.requests, 1)
}

func TestReportVisualAgentPipeline(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	draft := reportDraft{Summary: "original", Markdown: "# Report\n\n## 一、规模\n\n正文一\n\n## 五、未来趋势研判（2026-2030）\n\n正文二\n"}
	specs := []reporttemplate.VisualSpec{
		agentVisualSpec("一、规模", "L1", "bar"),
		agentVisualSpec("五、未来趋势研判（2026-2030）", "L3", "line"),
	}
	var published reportDraft
	var publishedState string
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func(headings []string) bool {
		return len(headings) == 2 && headings[0] == "一、规模" && headings[1] == "五、未来趋势研判（2026-2030）"
	})).Return(specs, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		published = args.Get(3).(reportDraft)
		publishedState = args.Get(5).(string)
	}).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportQualityPipeline(ctx, p, findings, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	require.Equal(t, 2, strings.Count(published.Markdown, "```zeus-chart"))
	require.Equal(t, reportReviewResolved, publishedState)
	firstHeading := strings.Index(published.Markdown, "## 一、规模")
	firstChart := strings.Index(published.Markdown, `"anchor":"一、规模"`)
	secondHeading := strings.Index(published.Markdown, "## 五、未来趋势研判（2026-2030）")
	secondChart := strings.Index(published.Markdown, `"anchor":"五、未来趋势研判（2026-2030）"`)
	require.Less(t, firstHeading, firstChart)
	require.Less(t, firstChart, secondHeading)
	require.Less(t, secondHeading, secondChart)
}

func TestReportVisualAgentReinsertsAfterRevise(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	draft := reportDraft{Summary: "original", Markdown: "# Report\n\n## 一、规模\n\n正文一\n\n## 五、未来趋势研判（2026-2030）\n\n正文二\n"}
	revised := reportDraft{Summary: "revised", Markdown: "# Report\n\n## 一、规模\n\n修订一\n\n## 五、未来趋势研判（2026-2030）\n\n修订二\n"}
	specs := []reporttemplate.VisualSpec{
		agentVisualSpec("一、规模", "L1", "bar"),
		agentVisualSpec("五、未来趋势研判（2026-2030）", "L3", "line"),
	}
	var published reportDraft
	var publishedState string
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func([]string) bool { return true })).Return(specs, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "revise"}, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, p, findings, mock.Anything, mock.Anything).Return(revised, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		published = args.Get(3).(reportDraft)
		publishedState = args.Get(5).(string)
	}).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportQualityPipeline(ctx, p, findings, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	require.Equal(t, 2, strings.Count(published.Markdown, "```zeus-chart"), "revise must reinsert charts")
	require.Equal(t, reportReviewResolved, publishedState)
	require.Contains(t, published.Markdown, "修订一")
	require.Contains(t, published.Markdown, `"anchor":"一、规模"`)
}

func TestReportVisualAgentFailureIsolated(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	draft := reportDraft{Summary: "original", Markdown: "# Report\n\n## 一、规模\n\n正文\n"}
	var published reportDraft
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func([]string) bool { return true })).Return([]reporttemplate.VisualSpec(nil), errors.New("plan failed")).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		published = args.Get(3).(reportDraft)
	}).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportQualityPipeline(ctx, p, findings, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	require.NotContains(t, published.Markdown, "zeus-chart")
	require.Contains(t, published.Markdown, "正文")
}

func TestReviseTruncationRetriesOnceWithoutThinking(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{
		{FinishReason: "length"},
		{Content: `{"summary":"revised","markdown":"## 一、规模\n\n修订正文"}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、规模\n\n正文"}, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Contains(t, out.Markdown, "修订正文")
	require.Len(t, provider.requests, 2)
	require.Equal(t, 16384, provider.requests[0].MaxTokens)
	require.False(t, provider.requests[0].DisableThinking)
	require.Equal(t, 24576, provider.requests[1].MaxTokens)
	require.True(t, provider.requests[1].DisableThinking)
}

func TestReviseTruncationTwiceFails(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{FinishReason: "length"}, {FinishReason: "length"}}}
	a := Activities{LLM: provider}
	_, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、规模\n\n正文"}, reportReview{Status: "revise"})
	require.Error(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 24576, provider.requests[1].MaxTokens)
	require.Equal(t, "output_truncated", reviseFailureReason(err))
}

func TestReviseEmptyOutputIsClassified(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"summary":"s","markdown":"  "}`}}}
	a := Activities{LLM: provider}
	_, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、规模\n\n正文"}, reportReview{Status: "revise"})
	require.ErrorIs(t, err, errReviseEmptyOutput)
	require.Len(t, provider.requests, 1)
	require.Equal(t, "empty_output", reviseFailureReason(err))
	require.Equal(t, "model_error", reviseFailureReason(errors.New("provider down")))
}

func TestReportVisualAgentReviseUnresolvedPublishesCharts(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	draft := reportDraft{Summary: "original", Markdown: "# Report\n\n## 一、规模\n\n正文一\n\n## 五、未来趋势研判（2026-2030）\n\n正文二\n"}
	specs := []reporttemplate.VisualSpec{
		agentVisualSpec("一、规模", "L1", "bar"),
		agentVisualSpec("五、未来趋势研判（2026-2030）", "L3", "line"),
	}
	var published reportDraft
	var publishedState string
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func([]string) bool { return true })).Return(specs, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "revise"}, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, p, findings, mock.Anything, mock.Anything).Return(reportDraft{}, llm.ErrOutputTruncated).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		published = args.Get(3).(reportDraft)
		publishedState = args.Get(5).(string)
	}).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportQualityPipeline(ctx, p, findings, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	require.Equal(t, reportReviewUnresolved, publishedState)
	require.Equal(t, 2, strings.Count(published.Markdown, "```zeus-chart"), "charts must survive a failed revision")
	require.Contains(t, logs.String(), "report revise unresolved")
	require.Contains(t, logs.String(), "reason=output_truncated")
}

func TestRevisePayloadStripsCharts(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"summary":"s","markdown":"## 一、规模\n\n修订"}`}}}
	a := Activities{LLM: provider}
	draft := reportDraft{Markdown: "## 一、规模\n\n正文\n\n```zeus-chart\n{\"title\":\"图A\",\"level\":\"L1\"}\n```\n"}
	_, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, draft, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, provider.requests, 1)
	content := provider.requests[0].Messages[0].Content
	require.NotContains(t, content, "zeus-chart")
	require.NotContains(t, content, "图A")
	require.Contains(t, provider.requests[0].System, "图表会由系统在修订后自动回填")
}

func TestReviewPayloadKeepsCharts(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"status":"pass","issues":[]}`}}}
	a := Activities{LLM: provider}
	draft := reportDraft{Markdown: "## 一、规模\n\n正文\n\n```zeus-chart\n{\"title\":\"图A\",\"level\":\"L1\"}\n```\n"}
	_, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, draft)
	require.NoError(t, err)
	require.Contains(t, provider.requests[0].Messages[0].Content, "zeus-chart")
}

func TestReviewVerdictLogsCountsWithoutIssueText(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"status":"revise","issues":[{"severity":"high","evidence":"SECRET_EVIDENCE","suggestion":"SECRET_SUGGESTION"},{"severity":"low","evidence":"low-evidence","suggestion":"low-suggestion"}]}`}}}
	a := Activities{LLM: provider}
	out, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、规模\n\n正文"})
	require.NoError(t, err)
	require.Equal(t, "revise", out.Status)
	got := logs.String()
	for _, want := range []string{"report review verdict", "attempt=1", "status=revise", "issue_count=2", "severity_high=1", "severity_medium=0", "severity_low=1"} {
		require.Contains(t, got, want)
	}
	require.NotContains(t, got, "SECRET_EVIDENCE")
	require.NotContains(t, got, "SECRET_SUGGESTION")
}

func TestReportVisualAgentReviseStaleChartsCleared(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	draft := reportDraft{Summary: "original", Markdown: "# Report\n\n## 一、规模\n\n正文一\n\n## 五、未来趋势研判（2026-2030）\n\n正文二\n"}
	revised := reportDraft{Summary: "revised", Markdown: "# Report\n\n## 一、规模\n\n修订一\n\n```zeus-chart\n{\"anchor\":\"一、规模\",\"title\":\"旧标题一\",\"level\":\"L1\"}\n```\n\n## 五、未来趋势研判（2026-2030）\n\n修订二\n\n```zeus-chart\n{\"anchor\":\"五、未来趋势研判（2026-2030）\",\"title\":\"旧标题二\",\"level\":\"L3\"}\n```\n"}
	specs := []reporttemplate.VisualSpec{
		agentVisualSpec("一、规模", "L1", "bar"),
		agentVisualSpec("五、未来趋势研判（2026-2030）", "L3", "line"),
	}
	var published reportDraft
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, mock.Anything).Return(draft, nil).Once()
	env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func([]string) bool { return true })).Return(specs, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "revise"}, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, p, findings, mock.Anything, mock.Anything).Return(revised, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		published = args.Get(3).(reportDraft)
	}).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportQualityPipeline(ctx, p, findings, nil)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
	require.Equal(t, 2, strings.Count(published.Markdown, "```zeus-chart"), "stale model charts must be cleared before reinserting")
	require.NotContains(t, published.Markdown, "旧标题一")
	require.NotContains(t, published.Markdown, "旧标题二")
}

func TestReviewStateFinalVerdict(t *testing.T) {
	cases := []struct {
		name            string
		pass            bool
		reviseSuccess   bool
		secondRevise    bool
		reviewErr       bool
		wantState       string
		wantUnavailable bool
	}{
		{name: "final pass", pass: true, wantState: reportReviewResolved},
		{name: "second review revise", reviseSuccess: true, secondRevise: true, wantState: reportReviewUnresolved},
		{name: "revision failed", reviseSuccess: false, wantState: reportReviewUnresolved},
		{name: "review unavailable", reviewErr: true, wantState: reportReviewUnresolved, wantUnavailable: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var logs bytes.Buffer
			old := slog.Default()
			slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
			defer slog.SetDefault(old)

			var suite testsuite.WorkflowTestSuite
			env := suite.NewTestWorkflowEnvironment()
			a := &Activities{}
			env.RegisterActivity(a)
			p := ReportPlan{}
			var findings []QuestionNotes
			draft := reportDraft{Summary: "original", Markdown: "## 一、规模\n\n正文"}
			revised := reportDraft{Summary: "revised", Markdown: "## 一、规模\n\n修订正文"}
			env.OnActivity(a.DraftReport, mock.Anything, p, findings, mock.Anything).Return(draft, nil).Once()
			env.OnActivity(a.PlanVisuals, mock.Anything, p, findings, mock.MatchedBy(func([]string) bool { return true })).Return([]reporttemplate.VisualSpec(nil), nil).Once()
			switch {
			case tc.reviewErr:
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{}, errors.New("review down")).Once()
			case tc.pass:
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
			default:
				env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "revise"}, nil).Once()
				if tc.reviseSuccess {
					env.OnActivity(a.ReviseReport, mock.Anything, p, findings, mock.Anything, mock.Anything).Return(revised, nil).Once()
					if tc.secondRevise {
						env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "revise"}, nil).Once()
					} else {
						env.OnActivity(a.ReviewReport, mock.Anything, p, findings, mock.Anything).Return(reportReview{Status: "pass"}, nil).Once()
					}
				} else {
					env.OnActivity(a.ReviseReport, mock.Anything, p, findings, mock.Anything, mock.Anything).Return(reportDraft{}, errors.New("revise down")).Once()
				}
			}
			published := 0
			publishedState := ""
			env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
				published++
				publishedState = args.Get(5).(string)
			}).Return(nil).Once()
			env.ExecuteWorkflow(func(ctx workflow.Context) error {
				ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
				return reportQualityPipeline(ctx, p, findings, nil)
			})
			require.NoError(t, env.GetWorkflowError())
			env.AssertExpectations(t)
			require.Equal(t, 1, published, "publish must happen in every verdict case")
			require.Equal(t, tc.wantState, publishedState)
			if tc.wantUnavailable {
				require.Contains(t, logs.String(), "report review unavailable")
				require.Contains(t, logs.String(), "reason=review_unavailable")
			}
		})
	}
}
