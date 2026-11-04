package workflows

import (
	"context"
	"encoding/json"
	"errors"
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
	findings := []QuestionNotes{{Question: "Population", Notes: "Population 2022 (thousands): India 1,417,173; China 1,412,175 [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "Population 2022 (thousands): India 1,417,173; China 1,412,175"}}}}
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
			draft := reportDraft{Summary: "original", Markdown: "body"}
			revised := reportDraft{Summary: "revised", Markdown: "fixed"}
			env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
			env.OnActivity(a.PlanReportVisuals, mock.Anything, p, findings).Return("", errors.New("visual unavailable")).Once()
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
			env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, mock.Anything, mock.MatchedBy(func(r reportReview) bool { return r.Status == tc.want })).Return(nil).Once()
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
func TestReviewRetriesMalformedOrTruncatedResponse(t *testing.T) {
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
		{"incomplete below cap", llm.Response{Content: `{"status":"rev`, UsageKnown: true, Usage: llm.Usage{OutputTokens: 20}}, llm.Response{Content: `{"status":"pass","issues":[]}`}, 2, false},
		{"content filtered", llm.Response{FinishReason: "content_filter", Content: `{"status":"rev`}, llm.Response{}, 1, true},
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
				if tc.name == "empty json mode response" || tc.name == "incomplete below cap" {
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

func TestVisualRetriesTruncatedWithBoundedThinkingBudget(t *testing.T) {
	s := &sequenceProvider{responses: []llm.Response{{FinishReason: "length", UsageKnown: true, Usage: llm.Usage{OutputTokens: 16000}}, {Content: `{"charts":[]}`}}}
	a := Activities{LLM: s}
	findings := []QuestionNotes{{Question: "Q", Notes: "Evidence [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	_, err := a.PlanReportVisuals(context.Background(), ReportPlan{}, findings)
	require.NoError(t, err)
	require.Len(t, s.requests, 2)
	require.False(t, s.requests[0].DisableThinking)
	require.False(t, s.requests[1].DisableThinking)
	require.Equal(t, 16000, s.requests[0].MaxTokens)
	require.Equal(t, 24000, s.requests[1].MaxTokens)

	s = &sequenceProvider{responses: []llm.Response{{FinishReason: "length"}, {FinishReason: "length"}}}
	a.LLM = s
	_, err = a.PlanReportVisuals(context.Background(), ReportPlan{}, findings)
	require.Error(t, err)
	require.Len(t, s.requests, 2)
	require.Equal(t, 24000, s.requests[1].MaxTokens)
}

func TestVisualEvidenceCorrectionSharesRetryBudget(t *testing.T) {
	const good = `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":25,"source":1},{"label":"邮件","value":10,"source":1}]}]}`
	const badEvidence = `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":99,"source":1},{"label":"邮件","value":10,"source":1}]}]}`
	const badBody = `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":25,"source":1},{"label":"社区","value":80,"source":1}]}]}`
	findings := []QuestionNotes{{Question: "渠道", Notes: "渠道 [1]", Sources: []search.Result{{URL: "https://example.test"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2025 搜索 25% 社区 80% 邮件 10%"}}}}
	body := "2025 搜索 25% 邮件 10% [1]"
	for _, tc := range []struct {
		name, first, second string
		wantErr             bool
	}{
		{"source correction", badEvidence, good, false},
		{"body correction", badBody, good, false},
		{"rejected twice", badEvidence, badEvidence, true},
		{"malformed then rejected", `{"charts":`, badEvidence, true},
		{"rejected then empty", badEvidence, `{"charts":[],"no_chart_reason":"insufficient_comparable_data"}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sequenceProvider{responses: []llm.Response{{Content: tc.first}, {Content: tc.second}}}
			a := Activities{LLM: s}
			got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, "")
			require.Len(t, s.requests, 2)
			require.False(t, s.requests[0].DisableThinking)
			require.False(t, s.requests[1].DisableThinking)
			require.Equal(t, 16000, s.requests[0].MaxTokens)
			require.Equal(t, 16000, s.requests[1].MaxTokens)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Contains(t, got.Markdown, "```zeus-chart")
			require.Equal(t, s.requests[0].Messages[0], s.requests[1].Messages[0])
			require.Contains(t, s.requests[1].Messages[len(s.requests[1].Messages)-1].Content, "不得推算或补造")
		})
	}
}

func TestEmptyVisualRechecksComparableSubsetWithinRetryBudget(t *testing.T) {
	const empty = `{"charts":[],"no_chart_reason":"insufficient_comparable_data"}`
	const missingReason = `{"charts":[]}`
	const explained = `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"正文缺少可核对的第二个数值"}`
	const valid = `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":25,"source":1},{"label":"邮件","value":10,"source":1}]}]}`
	const invalid = `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":99,"source":1},{"label":"邮件","value":10,"source":1}]}]}`
	findings := []QuestionNotes{{Question: "渠道", Notes: "渠道 [1]", Sources: []search.Result{{URL: "https://example.test"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2025 搜索 25% 邮件 10%"}}}}
	body := "2025 搜索 25% 邮件 10% [1]"
	for _, tc := range []struct {
		name, first, second string
		wantChart, wantErr  bool
	}{
		{"empty then supported chart", empty, valid, true, false},
		{"missing reason then supported chart", missingReason, valid, true, false},
		{"missing reason then explained empty", missingReason, explained, false, false},
		{"missing reason twice", missingReason, missingReason, false, true},
		{"missing reason then empty without explanation", missingReason, empty, false, true},
		{"empty twice with explanation", empty, explained, false, false},
		{"empty twice without explanation", empty, empty, false, true},
		{"empty then invented value", empty, invalid, false, true},
		{"malformed then empty without explanation", `{"charts":`, empty, false, true},
		{"malformed then empty without reason", `{"charts":`, missingReason, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sequenceProvider{responses: []llm.Response{{Content: tc.first}, {Content: tc.second}}}
			a := Activities{LLM: s}
			got, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
			require.Len(t, s.requests, 2)
			require.Equal(t, s.requests[0].Messages[0], s.requests[1].Messages[0])
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.wantChart, strings.Contains(got, "```zeus-chart"))
		})
	}
}

func TestMissingNoChartReasonRechecksWithoutExcerptsAndHonorsCancellation(t *testing.T) {
	const missingReason = `{"charts":[]}`
	const explained = `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"缺少原文数值"}`
	findings := []QuestionNotes{{Question: "Q", Notes: "Evidence [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	s := &sequenceProvider{responses: []llm.Response{{Content: missingReason}, {Content: explained}}}
	a := Activities{LLM: s}
	_, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, "正文 [1]")
	require.NoError(t, err)
	require.Len(t, s.requests, 2)
	s = &sequenceProvider{responses: []llm.Response{{Content: missingReason}, {Content: `{"charts":[],"no_chart_reason":"insufficient_comparable_data"}`}}}
	a.LLM = s
	_, err = a.planReportVisuals(context.Background(), ReportPlan{}, findings, "正文 [1]")
	require.ErrorContains(t, err, "missing explanation")
	require.Len(t, s.requests, 2)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s = &sequenceProvider{responses: []llm.Response{{Content: missingReason}}}
	a.LLM = s
	_, err = a.planReportVisuals(ctx, ReportPlan{}, findings, "正文 [1]")
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, s.requests, 1)
}

func TestVisualPlanIncludesUserTopicAndFocus(t *testing.T) {
	const chart = `{"charts":[{"type":"bar","title":"渠道","unit":"%","period":"2025","points":[{"label":"搜索","value":25,"source":1},{"label":"邮件","value":10,"source":1}]}]}`
	findings := []QuestionNotes{{Question: "渠道", Notes: "渠道 [1]", Sources: []search.Result{{URL: "https://example.test"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2025 搜索 25% 邮件 10%"}}}}
	s := &sequenceProvider{responses: []llm.Response{{Content: chart}}}
	a := Activities{LLM: s}
	p := ReportPlan{Topic: "渠道趋势", Focuses: []string{"分别展示各项指标"}}
	_, err := a.planReportVisuals(context.Background(), p, findings, "2025 搜索 25% 邮件 10% [1]")
	require.NoError(t, err)
	var payload struct {
		Topic   string   `json:"topic"`
		Focuses []string `json:"focuses"`
	}
	require.NoError(t, json.Unmarshal([]byte(s.requests[0].Messages[0].Content), &payload))
	require.Equal(t, p.Topic, payload.Topic)
	require.Equal(t, p.Focuses, payload.Focuses)
}

func TestInternalEvidenceStatusRequiresRevision(t *testing.T) {
	for _, body := range []string{"来源 readStatus 为 unknown", "| 来源 | read/unknown |", "状态 read／unknown"} {
		a := Activities{LLM: qualityProvider{result: `{"status":"pass","issues":[]}`}}
		review, err := a.ReviewReport(context.Background(), ReportPlan{Topic: "旅游"}, nil, reportDraft{Markdown: body})
		require.NoError(t, err)
		require.Equal(t, "revise", review.Status)
		require.Equal(t, "high", review.Issues[0].Severity)
	}
	// Reports explicitly about a technical field may name it.
	a := Activities{LLM: qualityProvider{result: `{"status":"pass","issues":[]}`}}
	review, err := a.ReviewReport(context.Background(), ReportPlan{Topic: "解释 readStatus 字段"}, nil, reportDraft{Markdown: "readStatus 字段"})
	require.NoError(t, err)
	require.Equal(t, "pass", review.Status)
	review, err = a.ReviewReport(context.Background(), ReportPlan{Topic: "未知的数据"}, nil, reportDraft{Markdown: "| 数据 | unknown |"})
	require.NoError(t, err)
	require.Equal(t, "pass", review.Status)
	// The actionable metadata issue must survive the five-issue cap even
	// when the model has already returned five high-priority findings.
	a.LLM = qualityProvider{result: `{"status":"revise","issues":[{"severity":"high","evidence":"1","suggestion":"a"},{"severity":"high","evidence":"2","suggestion":"b"},{"severity":"high","evidence":"3","suggestion":"c"},{"severity":"high","evidence":"4","suggestion":"d"},{"severity":"high","evidence":"5","suggestion":"e"}]}`}
	review, err = a.ReviewReport(context.Background(), ReportPlan{Topic: "旅游"}, nil, reportDraft{Markdown: "来源 readStatus 为 unknown"})
	require.NoError(t, err)
	require.Len(t, review.Issues, 5)
	require.Contains(t, review.Issues[0].Evidence, "内部字段")
}

func TestEvidenceIsPassedThroughWriteReviewAndRevision(t *testing.T) {
	stamp := time.Date(2025, 1, 21, 16, 0, 0, 0, time.UTC)
	evidence := "2024 城镇 16.3% 农村 9.9%"
	findings := []QuestionNotes{{Question: "Q", Notes: "Notes [1]", Sources: []search.Result{{URL: "https://example.test/official", PublishedAt: &stamp}}, SourceReadStatus: []string{"read"}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: evidence}}}}
	s := &sequenceProvider{responses: []llm.Response{{Content: `{"status":"pass","issues":[]}`}, {Content: `{"summary":"ok","markdown":"2024 城镇 16.3% 农村 9.9% [1]"}`}, {Content: `{"charts":[],"no_chart_reason":"insufficient_comparable_data"}`}, {Content: `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"只有一年数据"}`}}}
	a := Activities{LLM: s}
	writeRequest, err := reportDraftRequest(ReportPlan{}, findings, nil)
	require.NoError(t, err)
	require.NotContains(t, writeRequest.System, "图表用独立 Markdown 代码块")
	require.Contains(t, writeRequest.System, "只写正文")
	legacyPrompt, err := render(ReportPlan{}.Lang, "write.md", map[string]any{"VisualPipeline": false})
	require.NoError(t, err)
	require.Contains(t, legacyPrompt, "图表用独立 Markdown 代码块")
	draft := reportDraft{Summary: "ok", Markdown: "2024 城镇 16.3% 农村 9.9% [1]"}
	_, err = a.ReviewReport(context.Background(), ReportPlan{}, findings, draft)
	require.NoError(t, err)
	_, err = a.ReviseReport(context.Background(), ReportPlan{}, findings, draft, reportReview{Status: "revise"})
	require.NoError(t, err)
	_, err = a.planReportVisuals(context.Background(), ReportPlan{}, findings, draft.Markdown)
	require.NoError(t, err)
	requests := append([]llm.Request{writeRequest}, s.requests...)
	for _, request := range requests {
		var payload map[string]any
		require.NoError(t, json.Unmarshal([]byte(request.Messages[0].Content), &payload))
		require.Contains(t, payload, "source_excerpts")
		require.Contains(t, payload["source_excerpts"], evidence)
		raw, _ := json.Marshal(payload)
		require.Contains(t, string(raw), "https://example.test/official")
		require.Contains(t, string(raw), "2025-01-21T16:00:00Z")
		require.Contains(t, string(raw), "readStatus")
		require.Contains(t, request.System, "原文")
	}
	require.Contains(t, requests[1].System, "至少两个数据点")
}

func TestReviewedChartsArePreservedOrPublicationFails(t *testing.T) {
	const evidence = "Population 2022 (thousands): India 1,417,173; China 1,412,175"
	chart := "```zeus-chart\n{\"type\":\"bar\",\"title\":\"Population\",\"unit\":\"thousands\",\"period\":\"2022\",\"points\":[{\"label\":\"India\",\"value\":1417173,\"source\":1},{\"label\":\"China\",\"value\":1412175,\"source\":1}]}\n```\n"
	findings := []QuestionNotes{{Question: "Population", Sources: []search.Result{{URL: "https://example.test"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: evidence}}}}
	draft := reportDraft{Markdown: evidence + " [1]\n\n" + chart}
	require.NoError(t, validateReviewedVisuals(ReportPlan{}, findings, draft, 1))
	draft.Markdown = strings.Replace(draft.Markdown, "1412175", "9999999", 1)
	require.ErrorContains(t, validateReviewedVisuals(ReportPlan{}, findings, draft, 1), "publication validation")
}

func TestRevisionRetriesTruncatedAndDisablesThinking(t *testing.T) {
	s := &sequenceProvider{responses: []llm.Response{{FinishReason: "length", UsageKnown: true, Usage: llm.Usage{OutputTokens: 8192}}, {Content: `{"summary":"ok","markdown":"Evidence [1]"}`}}}
	a := Activities{LLM: s}
	_, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{}, reportReview{})
	require.NoError(t, err)
	require.Len(t, s.requests, 2)
	require.True(t, s.requests[0].DisableThinking)
	require.True(t, s.requests[1].DisableThinking)
	require.Equal(t, 8192, s.requests[0].MaxTokens)
	require.Equal(t, 12000, s.requests[1].MaxTokens)
}

func TestPrepareVisualsProducesSupportedGraphicAndExplainsAbsence(t *testing.T) {
	findings := []QuestionNotes{{Question: "Population", Notes: "Population 2022 (thousands): India 1,417,173; China 1,412,175 [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "Population 2022 (thousands): India 1,417,173; China 1,412,175"}}}}
	draft := reportDraft{Markdown: "## Population\n\nPopulation 2022 (thousands): India 1,417,173; China 1,412,175 [1]."}
	a := Activities{LLM: qualityProvider{result: `{"charts":[{"type":"bar","title":"Population","unit":"thousands","period":"2022","points":[{"label":"India","value":1417173,"source":1},{"label":"China","value":1412175,"source":1}]}]}`}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, draft, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "```zeus-chart")
	a.LLM = qualityProvider{result: `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"正文没有可核对的数值"}`}
	got, err = a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "如下图所示，资料不足。"}, "")
	require.NoError(t, err)
	require.NotContains(t, got.Markdown, "下图")
	require.Contains(t, got.Markdown, "未找到同时可核对来源和正文的可比较原始数值")
	findings = append(findings, QuestionNotes{Question: "Demand", Notes: "Second source [1]", Sources: []search.Result{{URL: "https://example.org"}}})
	draft.Markdown = "## Supply\n\nProduction is constrained [1].\n\n## Demand\n\nDemand depends on adoption [2]."
	got, err = a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, draft, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "```zeus-structure")
}

func TestVisualPipelineRechecksAfterRevision(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	var notes []string
	draft := reportDraft{Markdown: "draft"}
	visual := reportDraft{Markdown: "draft with visual"}
	revised := reportDraft{Markdown: "revised"}
	final := reportDraft{Markdown: "revised with visual"}
	first := reportReview{Status: "revise", Issues: []reviewIssue{{Severity: "high", Evidence: "mismatch", Suggestion: "correct"}}}
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
	env.OnActivity(a.PrepareReportVisuals, mock.Anything, p, findings, draft, "").Return(visual, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, visual).Return(first, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, p, findings, visual, first).Return(revised, nil).Once()
	env.OnActivity(a.PrepareReportVisuals, mock.Anything, p, findings, revised, visual.Markdown).Return(final, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, final).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishVisualReport, mock.Anything, p, findings, final, mock.MatchedBy(func(r reportReview) bool { return r.Status == "pass" })).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportVisualPipeline(ctx, p, findings, notes)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestVisualPipelineReplaysLegacyPublication(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	var notes []string
	draft := reportDraft{Markdown: "draft"}
	env.OnGetVersion("report-visual-publish-v3", workflow.DefaultVersion, workflow.Version(1)).Return(workflow.DefaultVersion).Once()
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
	env.OnActivity(a.PrepareReportVisuals, mock.Anything, p, findings, draft, "").Return(draft, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, draft).Return(reportReview{Status: "pass"}, nil).Once()
	env.OnActivity(a.PublishReviewedReport, mock.Anything, p, findings, draft, mock.Anything).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportVisualPipeline(ctx, p, findings, notes)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}

func TestVisualMustMatchReportBody(t *testing.T) {
	a := Activities{LLM: qualityProvider{result: `{"charts":[{"type":"bar","title":"Population","unit":"thousands","period":"2022","points":[{"label":"India","value":1417173,"source":1},{"label":"China","value":1412175,"source":1}]}]}`}}
	findings := []QuestionNotes{{Question: "Population", Notes: "Population 2022 (thousands): India 1,417,173; China 1,412,175 [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "Population 2022 (thousands): India 1,417,173; China 1,412,175"}}}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "## Population\n\nThe report omits the original values [1]."}, "")
	require.ErrorContains(t, err, "do not match report body")
	require.Empty(t, got.Markdown)
}
func TestReviewRejectsVisualThatCannotRender(t *testing.T) {
	a := Activities{LLM: qualityProvider{result: `{"status":"pass","issues":[]}`}}
	_, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "```zeus-chart\n{}\n```\n"})
	require.ErrorContains(t, err, "could not be rendered")
}

func TestNumericSourceExcerptsPreserveAndRenumber(t *testing.T) {
	raw := "Population 2022 (thousands): India 1,417,173; China 1,412,175.\n\nOther incomplete 2023 data"
	excerpts := numericSourceExcerpts(raw, 1, true)
	require.Len(t, excerpts, 1)
	require.Equal(t, "Population 2022 (thousands): India 1,417,173; China 1,412,175.", excerpts[0].Text)
	merged, sources := mergeNotes([]QuestionNotes{{Question: "Q", Notes: "Summary [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}, NumericExcerpts: excerpts}})
	require.Len(t, sources, 1)
	require.Contains(t, merged, excerpts[0].Text+" [1]")
}
func TestRevisionReusesStillSupportedChart(t *testing.T) {
	findings := []QuestionNotes{{Question: "Population", Notes: "Population 2022 (thousands): India 1,417,173; China 1,412,175 [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "Population 2022 (thousands): India 1,417,173; China 1,412,175"}}}}
	chart := "```zeus-chart\n{\"type\":\"bar\",\"title\":\"Population\",\"unit\":\"thousands\",\"period\":\"2022\",\"points\":[{\"label\":\"India\",\"value\":1417173,\"source\":1},{\"label\":\"China\",\"value\":1412175,\"source\":1}]}\n```\n"
	revised := reportDraft{Markdown: "## Population\n\nPopulation 2022 (thousands): India 1,417,173; China 1,412,175 [1]."}
	a := Activities{LLM: qualityProvider{err: errors.New("must not be called")}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, revised, chart)
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "```zeus-chart")
	revised.Markdown = "## Population\n\nThe figures are omitted [1]."
	_, err = a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, revised, chart)
	require.ErrorContains(t, err, "must not be called")
}
func TestVisualPipelineStopsOnTechnicalFailure(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	var notes []string
	draft := reportDraft{Markdown: "draft"}
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
	env.OnActivity(a.PrepareReportVisuals, mock.Anything, p, findings, draft, "").Return(reportDraft{}, errors.New("visual unavailable")).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportVisualPipeline(ctx, p, findings, notes)
	})
	require.ErrorContains(t, env.GetWorkflowError(), "visual preparation failed")
	env.AssertNotCalled(t, "PublishVisualReport", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestReviewReceivesRenderedChartAndOriginalEvidence(t *testing.T) {
	s := &sequenceProvider{responses: []llm.Response{{Content: `{"status":"pass","issues":[]}`}}}
	a := Activities{LLM: s}
	evidence := "Population 2022 (thousands): India 1,417,173; China 1,412,175."
	findings := []QuestionNotes{{Question: "Population", Notes: "Population [1]", Sources: []search.Result{{URL: "https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: evidence}}}}
	chart := "```zeus-chart\n{\"type\":\"bar\",\"title\":\"Population\",\"unit\":\"thousands\",\"period\":\"2022\",\"points\":[{\"label\":\"India\",\"value\":1417173,\"source\":1},{\"label\":\"China\",\"value\":1412175,\"source\":1}]}\n```\n"
	_, err := a.ReviewReport(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: evidence + " [1]\n\n" + chart})
	require.NoError(t, err)
	require.Len(t, s.requests, 1)
	payload := s.requests[0].Messages[0].Content
	require.Contains(t, payload, evidence)
	require.Contains(t, payload, `\"value\":1417173`)
	require.Contains(t, payload, `"charts":1`)
}

func TestVisualPreparationRequiresReasonForEmptyPlan(t *testing.T) {
	a := Activities{LLM: qualityProvider{result: `{"charts":[]}`}}
	findings := []QuestionNotes{{Question: "Q", Notes: "Evidence [1]", Sources: []search.Result{{URL: "https://example.org"}}}}
	_, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "## A\n\nEvidence [1]."}, "")
	require.ErrorContains(t, err, "missing reason for no chart")
}
func TestVisualPipelineStopsWhenReviewRemainsUnresolved(t *testing.T) {
	var suite testsuite.WorkflowTestSuite
	env := suite.NewTestWorkflowEnvironment()
	a := &Activities{}
	env.RegisterActivity(a)
	p := ReportPlan{}
	var findings []QuestionNotes
	var notes []string
	draft := reportDraft{Markdown: "draft"}
	visual := reportDraft{Markdown: "visual"}
	revised := reportDraft{Markdown: "revised"}
	prepared := reportDraft{Markdown: "prepared"}
	issue := reportReview{Status: "revise", Issues: []reviewIssue{{Severity: "high", Evidence: "mismatch", Suggestion: "correct"}}}
	env.OnActivity(a.DraftReport, mock.Anything, p, findings, notes).Return(draft, nil).Once()
	env.OnActivity(a.PrepareReportVisuals, mock.Anything, p, findings, draft, "").Return(visual, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, visual).Return(issue, nil).Once()
	env.OnActivity(a.ReviseReport, mock.Anything, p, findings, visual, issue).Return(revised, nil).Once()
	env.OnActivity(a.PrepareReportVisuals, mock.Anything, p, findings, revised, visual.Markdown).Return(prepared, nil).Once()
	env.OnActivity(a.ReviewReport, mock.Anything, p, findings, prepared).Return(issue, nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{StartToCloseTimeout: time.Minute})
		return reportVisualPipeline(ctx, p, findings, notes)
	})
	require.ErrorContains(t, env.GetWorkflowError(), "unresolved issues")
	env.AssertNotCalled(t, "PublishVisualReport", mock.Anything, mock.Anything, mock.Anything, mock.Anything, mock.Anything)
}

func TestAnnualOfficialEvidenceFlowsIntoRenderedChart(t *testing.T) {
	// ONS time series GBPOP, revised figures as published 1 October 2026.
	// https://www.ons.gov.uk/peoplepopulationandcommunity/populationandmigration/populationestimates/timeseries/gbpop/pop
	evidence := "Great Britain population estimate, people: 2022 65,769,400\n\nGreat Britain population estimate, people: 2023 66,667,700\n\nGreat Britain population estimate, people: 2024 67,328,400"
	// The actual table has one row per year; context here repeats the table heading.
	excerpts := []NumericExcerpt{{Source: 1, Text: "Great Britain population estimate, people: 2022 65,769,400"}, {Source: 1, Text: "Great Britain population estimate, people: 2023 66,667,700"}, {Source: 1, Text: "Great Britain population estimate, people: 2024 67,328,400"}}
	findings := []QuestionNotes{{Question: "Population", Notes: "Population estimates [1]", Sources: []search.Result{{URL: "https://www.ons.gov.uk/peoplepopulationandcommunity/populationandmigration/populationestimates/timeseries/gbpop/pop"}}, NumericExcerpts: excerpts}}
	body := "## Population\n\nGreat Britain population estimate, people: 2022 65,769,400 [1]\n\nGreat Britain population estimate, people: 2023 66,667,700 [1]\n\nGreat Britain population estimate, people: 2024 67,328,400 [1]"
	a := Activities{LLM: qualityProvider{result: `{"charts":[{"type":"line","title":"population","unit":"people","period":"2022—2024","scope":"Great Britain","qualifier":"estimate","points":[{"label":"2022","value":65769400,"source":1},{"label":"2023","value":66667700,"source":1},{"label":"2024","value":67328400,"source":1}]}]}`}}
	require.NotEmpty(t, evidence)
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "```zeus-chart")
	out, err := reporttemplate.Render(reporttemplate.Data{Markdown: got.Markdown, Lang: "en", Sources: []reporttemplate.Source{{Index: 1, URL: findings[0].Sources[0].URL, Title: "ONS"}}})
	require.NoError(t, err)
	require.Contains(t, string(out), `<figure class="report-chart"`)
	require.Contains(t, string(out), `<polyline`)
}

func TestOfficialAnnualReleaseExcerptAndChart(t *testing.T) {
	// Verbatim lines from the ONS mid-2025 England and Wales population release:
	// https://www.ons.gov.uk/peoplepopulationandcommunity/populationandmigration/populationestimates/bulletins/populationestimatesforenglandandwales/mid2025
	source := "An unrelated statistic is 123.\nOther heading\nUnrelated statistic 456.\nAnother heading\nUnrelated number 789.\nThe total population estimates for England and Wales have been revised to:\n60,322,400 in mid-2022, an increase of 43,800 (0.07%)\n61,155,900 in mid-2023, an increase of 56,000 (0.09%)\n61,785,000 in mid-2024, a decrease of 21,600 (negative 0.03%)\nOther text."
	excerpts := numericSourceExcerptsForQuery(source, 1, false, "England and Wales population estimates 2022 2023 2024")
	require.NotEmpty(t, excerpts)
	var selected string
	for _, e := range excerpts {
		if strings.Contains(e.Text, "60,322,400") {
			selected = e.Text
		}
	}
	require.NotEmpty(t, selected)
	require.Contains(t, source, selected)
	findings := []QuestionNotes{{Question: "Population", Notes: "Population estimates [1]", Sources: []search.Result{{URL: "https://www.ons.gov.uk/peoplepopulationandcommunity/populationandmigration/populationestimates/bulletins/populationestimatesforenglandandwales/mid2025"}}, NumericExcerpts: excerpts}}
	model := qualityProvider{result: `{"charts":[{"type":"line","title":"population","unit":"population","period":"2022—2024","scope":"England and Wales","qualifier":"estimate","points":[{"label":"2022","value":60322400,"source":1},{"label":"2023","value":61155900,"source":1},{"label":"2024","value":61785000,"source":1}]}]}`}
	a := Activities{LLM: model}
	draft := reportDraft{Markdown: "## Population\n\n" + selected + " [1]"}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, draft, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "```zeus-chart")
	rendered, err := reporttemplate.Render(reporttemplate.Data{Lang: "en", Markdown: got.Markdown, Sources: []reporttemplate.Source{{Index: 1, URL: findings[0].Sources[0].URL, Title: "ONS"}}})
	require.NoError(t, err)
	require.Contains(t, string(rendered), `<polyline`)
}

func TestVisualNoteExplainsMissingSources(t *testing.T) {
	a := Activities{LLM: qualityProvider{err: errors.New("unexpected model call")}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "正文"}, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "没有可核验的来源")
	require.NotContains(t, got.Markdown, "结构图")
}

func TestVisualPreparationDistinguishesSourceReadFailure(t *testing.T) {
	findings := []QuestionNotes{{Question: "Q", Notes: "A source [1]", Sources: []search.Result{{URL: "https://example.org"}}, SourceReadsAttempted: 1}}
	a := Activities{LLM: qualityProvider{result: `{"charts":[],"no_chart_reason":"insufficient_comparable_data"}`}}
	_, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "## Topic\n\nA source [1]."}, "")
	require.ErrorContains(t, err, "source pages unavailable")
	findings[0].SourceReadsAttempted = 0
	findings[0].SourceReaderUnavailable = true
	_, err = a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "## Topic\n\nA source [1]."}, "")
	require.ErrorContains(t, err, "source pages unavailable")
	findings[0].SourceReaderUnavailable = false
	findings[0].SourceReadsAttempted = 1
	findings[0].SourceReadsSucceeded = 1
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "## Topic\n\nA source [1]."}, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "未找到同时可核对来源和正文的可比较原始数值")
}

func TestRevisionRetriesMalformedJSONAtSameLimit(t *testing.T) {
	for _, tc := range []struct {
		name     string
		second   string
		succeeds bool
	}{
		{"recovered", `{"summary":"ok","markdown":"revised [1]"}`, true},
		{"still invalid", `{"summary":"again"æ}`, false},
		{"empty revision", `{"summary":"ok","markdown":""}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &sequenceProvider{responses: []llm.Response{{Content: `{"summary":"first","markdown":"broken"æ}`, UsageKnown: true, Usage: llm.Usage{OutputTokens: 2292}}, {Content: tc.second}}}
			a := Activities{LLM: s}
			_, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{}, reportReview{})
			if tc.succeeds {
				require.NoError(t, err)
			} else {
				require.Error(t, err)
			}
			require.Len(t, s.requests, 2)
			require.Equal(t, 8192, s.requests[0].MaxTokens)
			require.Equal(t, 8192, s.requests[1].MaxTokens)
			require.True(t, s.requests[0].JSON)
			require.True(t, s.requests[1].JSON)
			require.Contains(t, s.requests[1].System, "JSON 语法有误")
		})
	}
}

func TestRetryStructuredDoesNotRetryMalformedEnvelope(t *testing.T) {
	var decoded any
	err := json.Unmarshal([]byte(`{"bad"æ}`), &decoded)
	require.Error(t, err)
	require.False(t, retryStructured(llm.Response{}, err, 8192))
	require.True(t, retryStructured(llm.Response{Content: `{"bad"æ}`}, err, 8192))
}
func TestRevisionMalformedJSONHonorsCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	s := &sequenceProvider{responses: []llm.Response{{Content: `{"summary":"broken"æ}`}}}
	a := Activities{LLM: s}
	_, err := a.ReviseReport(ctx, ReportPlan{}, nil, reportDraft{}, reportReview{})
	require.ErrorIs(t, err, context.Canceled)
	require.Len(t, s.requests, 1)
}
