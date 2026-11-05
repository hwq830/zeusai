package workflows

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

func revisionEvidence() ([]QuestionNotes, string) {
	findings := []QuestionNotes{
		{Sources: []search.Result{{URL: "https://example.test/a"}}, SourceReadStatus: []string{"read"}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2023年，国内出游人次48.91亿。"}}},
		{Sources: []search.Result{{URL: "https://example.test/b", Snippet: "不能用的搜索摘要 999"}}, SourceReadStatus: []string{"read"}, SourceExcerpts: []SourceExcerpt{{Source: 1, Text: "2024年，国内出游人次56.15亿。"}, {Source: 1, Text: "2024年，国内游客出游总花费5.75万亿元。"}}},
		{Sources: []search.Result{{URL: "https://example.test/c"}}, SourceReadStatus: []string{"read"}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2025年国内出游：城镇居民50.0亿人次，农村居民15.3亿人次。"}}},
	}
	body := "2023年，国内出游人次48.91亿 [1]。\n\n2024年，国内出游人次56.15亿 [2]。\n\n2025年国内出游：城镇居民50.0亿人次，农村居民15.3亿人次 [3]。"
	return findings, body
}

// A real history can be supplied locally without committing its payloads.
func TestReplayReportHistoryFromEnvironment(t *testing.T) {
	path := os.Getenv("ZEUS_REPORT_REPLAY_HISTORY")
	if path == "" {
		t.Skip("ZEUS_REPORT_REPLAY_HISTORY not supplied")
	}
	replayer := worker.NewWorkflowReplayer()
	replayer.RegisterWorkflowWithOptions(ReportWorkflow, workflow.RegisterOptions{Name: ReportWorkflowName})
	require.NoError(t, replayer.ReplayWorkflowHistoryFromJSONFile(nil, path))
}

const revisionOldChart = "```zeus-chart\n{\"type\":\"bar\",\"title\":\"城乡出游\",\"scope\":\"国内出游\",\"unit\":\"亿人次\",\"period\":\"2025\",\"points\":[{\"label\":\"城镇居民\",\"value\":50.0,\"source\":3},{\"label\":\"农村居民\",\"value\":15.3,\"source\":3}]}\n```"
const revisionNewChart = `{"type":"bar","title":"年度国内出游人次","scope":"国内出游","unit":"亿","period":"2023-2024","points":[{"label":"2023","value":48.91,"source":1},{"label":"2024","value":56.15,"source":2}]}`

func TestChartEvidenceUsesGeneralAndLegacyNumericWithoutMetadata(t *testing.T) {
	findings, body := revisionEvidence()
	findings[1].Notes = "模型摘要不可作为依据 888"
	findings = append(findings, QuestionNotes{Sources: []search.Result{{URL: "https://example.test/unread"}}, SourceReadStatus: []string{"not_attempted"}, SourceExcerpts: []SourceExcerpt{{Source: 1, Text: "未读文本 2024 年 999"}}})
	evidence := mergeChartEvidence(findings)
	require.Contains(t, evidence, "56.15亿。 [2]")
	require.NotContains(t, evidence, "888")
	require.NotContains(t, evidence, "999")
	hints := visualCandidateHints(findings, body, 4)
	require.Len(t, hints, 2)
	require.Equal(t, 2, hints[1].Source)
	chart := "```zeus-chart\n" + revisionNewChart + "\n```"
	require.NoError(t, validateReviewedVisuals(ReportPlan{}, findings, reportDraft{Markdown: body + "\n\n" + chart}, 4))
	require.Error(t, validateReviewedVisuals(ReportPlan{}, findings, reportDraft{Markdown: strings.Replace(body, "56.15", "56.16", 1) + "\n\n" + chart}, 4))
	findings[0].NumericExcerpts = append(findings[0].NumericExcerpts, NumericExcerpt{Source: 1, Text: "2023年，国内游客出游总花费4.91万亿元。"})
	expenseBody := body + "\n\n2023年，国内游客出游总花费4.91万亿元 [1]。\n\n2024年，国内游客出游总花费5.75万亿元 [2]。"
	expense := "```zeus-chart\n" + `{"type":"bar","title":"年度国内游客出游总花费","scope":"国内游客","unit":"万亿元","period":"2023-2024","points":[{"label":"2023","value":4.91,"source":1},{"label":"2024","value":5.75,"source":2}]}` + "\n```"
	require.NoError(t, validateReviewedVisuals(ReportPlan{}, findings, reportDraft{Markdown: expenseBody + "\n\n" + expense}, 4))

	legacy := []QuestionNotes{{Sources: []search.Result{{URL: "https://example.test/legacy"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "legacy passage 123"}}}}
	require.Contains(t, mergeChartEvidence(legacy), "legacy passage 123 [1]")
}

func TestRevisedVisualsReplanAndKeepCorrectedTitle(t *testing.T) {
	findings, body := revisionEvidence()
	revisedChart := strings.Replace(revisionOldChart, "城乡出游", "城乡出游（转载，原文未核对）", 1)
	prior := body + "\n\n" + revisionOldChart
	revised := reportDraft{Markdown: body + "\n\n" + revisedChart}
	review := reportReview{Status: "revise", Issues: []reviewIssue{{Severity: "medium", Evidence: "缺少年度趋势图", Suggestion: "补充年度趋势图"}}}
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"charts":[` + revisionNewChart + `]}`}}}
	a := Activities{LLM: provider}
	got, err := a.PrepareRevisedReportVisuals(context.Background(), ReportPlan{}, findings, revised, prior, review)
	require.NoError(t, err)
	require.Len(t, provider.requests, 1)
	require.Contains(t, provider.requests[0].Messages[0].Content, "缺少年度趋势图")
	require.Contains(t, provider.requests[0].Messages[0].Content, "existing_charts")
	var payload struct {
		ExistingCharts string `json:"existing_charts"`
	}
	require.NoError(t, json.Unmarshal([]byte(provider.requests[0].Messages[0].Content), &payload))
	require.Equal(t, 1, strings.Count(payload.ExistingCharts, "```zeus-chart"))
	require.Contains(t, payload.ExistingCharts, "城乡出游（转载，原文未核对）")
	require.Equal(t, 2, strings.Count(got.Markdown, "```zeus-chart"))
	require.Contains(t, got.Markdown, "城乡出游（转载，原文未核对）")
	require.NotContains(t, got.Markdown, `"title":"城乡出游"`)
	require.Contains(t, got.Markdown, "年度国内出游人次")
	require.NoError(t, validateReviewedVisuals(ReportPlan{}, findings, got, 3))
}

func TestReplannedChartCorrectsTitleOfUnchangedRevisedChart(t *testing.T) {
	findings, body := revisionEvidence()
	prior := body + "\n\n" + revisionOldChart
	correctedJSON := strings.TrimSuffix(strings.TrimPrefix(revisionOldChart, "```zeus-chart\n"), "\n```")
	correctedJSON = strings.Replace(correctedJSON, "城乡出游", "城乡出游（转载，原文未核对）", 1)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"charts":[` + correctedJSON + `]}`}}}
	a := Activities{LLM: provider}
	review := reportReview{Status: "revise", Issues: []reviewIssue{{Severity: "medium", Evidence: "图表标题缺少来源说明", Suggestion: "注明转载且原文未核对"}}}
	got, err := a.PrepareRevisedReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: prior}, prior, review)
	require.NoError(t, err)
	require.Len(t, provider.requests, 1)
	require.Contains(t, provider.requests[0].System, "同数据返回项会替换现有图")
	require.Contains(t, provider.requests[0].System, "未返回的有效图表仍会保留")
	var payload struct {
		ExistingCharts string `json:"existing_charts"`
	}
	require.NoError(t, json.Unmarshal([]byte(provider.requests[0].Messages[0].Content), &payload))
	require.Equal(t, 1, strings.Count(payload.ExistingCharts, "```zeus-chart"))
	require.Equal(t, 1, strings.Count(got.Markdown, "```zeus-chart"))
	require.Contains(t, got.Markdown, "城乡出游（转载，原文未核对）")
	require.NoError(t, validateReviewedVisuals(ReportPlan{}, findings, got, 3))
}

func TestRevisedVisualsRejectUnsupportedCandidates(t *testing.T) {
	findings, body := revisionEvidence()
	bad := strings.Replace(revisionNewChart, "56.15", "99.15", 1)
	response := `{"charts":[` + bad + `]}`
	provider := &sequenceProvider{responses: []llm.Response{{Content: response}, {Content: response}}}
	a := Activities{LLM: provider}
	_, err := a.PrepareRevisedReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, body+"\n\n"+revisionOldChart, reportReview{Status: "revise"})
	require.ErrorContains(t, err, "visual replanning")
	require.Len(t, provider.requests, 2)
}

func TestRevisedVisualsKeepOldChartWhenNoAdditionalComparableData(t *testing.T) {
	findings, body := revisionEvidence()
	response := `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"没有更多可比较数据"}`
	provider := &sequenceProvider{responses: []llm.Response{{Content: response}, {Content: response}}}
	a := Activities{LLM: provider}
	got, err := a.PrepareRevisedReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, body+"\n\n"+revisionOldChart, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 1, strings.Count(got.Markdown, "```zeus-chart"))
	require.Contains(t, got.Markdown, "城乡出游")
}

func TestRevisedVisualsStopIfSourceReaderUnavailable(t *testing.T) {
	findings := []QuestionNotes{{Sources: []search.Result{{URL: "https://example.test/unread"}}, SourceReadsAttempted: 1}}
	provider := &sequenceProvider{}
	a := Activities{LLM: provider}
	_, err := a.PrepareRevisedReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: "body"}, "", reportReview{Status: "revise"})
	require.ErrorContains(t, err, "source pages unavailable")
	require.Empty(t, provider.requests)
}

func TestPartialVisualRejectionCanBeCorrectedWithinBudget(t *testing.T) {
	findings, body := revisionEvidence()
	old := strings.TrimSuffix(strings.TrimPrefix(revisionOldChart, "```zeus-chart\n"), "\n```")
	bad := strings.Replace(revisionNewChart, `"unit":"亿"`, `"unit":"亿人次"`, 1)
	first := `{"charts":[` + old + "," + bad + `]}`
	provider := &sequenceProvider{responses: []llm.Response{{Content: first}, {Content: `{"charts":[` + revisionNewChart + `]}`}}}
	a := Activities{LLM: provider}
	got, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 2, strings.Count(got, "```zeus-chart"))
	require.Contains(t, got, "城乡出游")
	require.Contains(t, got, "年度国内出游人次")
	require.NotContains(t, got, `"unit":"亿人次","period":"2023-2024"`)
	require.NoError(t, validateReviewedVisuals(ReportPlan{}, findings, reportDraft{Markdown: body + "\n\n" + got}, 3))
	msgs := provider.requests[1].Messages
	require.Equal(t, first, msgs[len(msgs)-2].Content)
	require.Contains(t, msgs[len(msgs)-1].Content, `"index":2`)
	require.Contains(t, msgs[len(msgs)-1].Content, `"stage":"source"`)
	require.Contains(t, msgs[len(msgs)-1].Content, `"evidence_unit":1`)
	require.Contains(t, msgs[len(msgs)-1].Content, "已通过原文和正文校验的图将保留")
}

func TestPartialVisualCorrectionKeepsValidatedAndRejectsFailures(t *testing.T) {
	findings, body := revisionEvidence()
	old := strings.TrimSuffix(strings.TrimPrefix(revisionOldChart, "```zeus-chart\n"), "\n```")
	bad := strings.Replace(revisionNewChart, `"unit":"亿"`, `"unit":"亿人次"`, 1)
	first := `{"charts":[` + old + "," + bad + `]}`
	for _, tc := range []struct {
		name, second string
		wantErr      bool
	}{
		{"explicitly unresolved", `{"charts":[],"unresolved_chart_explanation":"剩余图缺少可核对的单位"}`, false},
		{"unsupported correction", `{"charts":[` + strings.Replace(revisionNewChart, "56.15", "99.15", 1) + `]}`, false},
		{"global no-chart claim", `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"无法制图"}`, true},
		{"missing explanation", `{"charts":[]}`, true},
		{"malformed output", `{"charts":`, true},
		{"invalid schema", `{"charts":[{}]}`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := &sequenceProvider{responses: []llm.Response{{Content: first}, {Content: tc.second}}}
			a := Activities{LLM: provider}
			got, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
			require.Len(t, provider.requests, 2)
			if tc.wantErr {
				require.Error(t, err)
				require.Empty(t, got)
				return
			}
			require.NoError(t, err)
			require.Equal(t, 1, strings.Count(got, "```zeus-chart"))
			require.Contains(t, got, "城乡出游")
			require.NotContains(t, got, "99.15")
		})
	}
	provider := &sequenceProvider{responses: []llm.Response{{Content: first}}}
	a := Activities{LLM: provider}
	_, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
	require.ErrorContains(t, err, "unexpected call")
	require.Len(t, provider.requests, 2)
}

func TestPartialVisualRetryUsesLatestValidatedTitleAndNeverCallsThirdTime(t *testing.T) {
	findings, body := revisionEvidence()
	old := strings.TrimSuffix(strings.TrimPrefix(revisionOldChart, "```zeus-chart\n"), "\n```")
	bad := strings.Replace(revisionNewChart, `"unit":"亿"`, `"unit":"亿人次"`, 1)
	corrected := strings.Replace(old, "城乡出游", "城乡出游（来源说明）", 1)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"charts":[` + old + "," + bad + `]}`}, {Content: `{"charts":[` + corrected + "," + bad + `]}`}}}
	a := Activities{LLM: provider}
	got, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 1, strings.Count(got, "```zeus-chart"))
	require.Contains(t, got, "城乡出游（来源说明）")
	require.NotContains(t, got, `"title":"城乡出游"`)
}

func TestPartialVisualCorrectionReportsBodyRejection(t *testing.T) {
	findings, body := revisionEvidence()
	body = strings.Replace(body, "56.15", "56.16", 1)
	old := strings.TrimSuffix(strings.TrimPrefix(revisionOldChart, "```zeus-chart\n"), "\n```")
	first := `{"charts":[` + old + "," + revisionNewChart + `]}`
	provider := &sequenceProvider{responses: []llm.Response{{Content: first}, {Content: `{"charts":[],"unresolved_chart_explanation":"正文缺少对应数值"}`}}}
	a := Activities{LLM: provider}
	got, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 1, strings.Count(got, "```zeus-chart"))
	feedback := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	require.Contains(t, feedback, `"index":2`)
	require.Contains(t, feedback, `"stage":"body"`)
	require.Contains(t, feedback, `"missing_evidence":1`)
}

func TestScopeOnlyRejectionGetsShortScopeCorrectionWithinBudget(t *testing.T) {
	findings, body := revisionEvidence()
	badScope := strings.Replace(revisionNewChart, `"scope":"国内出游"`, `"scope":"国内出游规模"`, 1)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"charts":[` + badScope + `]}`}, {Content: `{"charts":[]}`}}}
	a := Activities{LLM: provider}
	_, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
	require.Error(t, err)
	require.Len(t, provider.requests, 2)
	feedback := provider.requests[1].Messages[len(provider.requests[1].Messages)-1].Content
	require.Contains(t, feedback, `"evidence_scope":1`)
	require.Contains(t, feedback, "更短共同短语")
	require.Contains(t, feedback, "不要扩写")
}

func TestMalformedFirstResponseUsesTheSharedPartialRetryBudget(t *testing.T) {
	findings, body := revisionEvidence()
	old := strings.TrimSuffix(strings.TrimPrefix(revisionOldChart, "```zeus-chart\n"), "\n```")
	bad := strings.Replace(revisionNewChart, `"unit":"亿"`, `"unit":"亿人次"`, 1)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"charts":`}, {Content: `{"charts":[` + old + "," + bad + `]}`}}}
	a := Activities{LLM: provider}
	got, err := a.planReportVisuals(context.Background(), ReportPlan{}, findings, body)
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 1, strings.Count(got, "```zeus-chart"))
	require.Contains(t, got, "城乡出游")
}

func TestReviewPromptAllowsEvidencedSubsetWithoutWeakeningSeverityGate(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"status":"pass","issues":[{"severity":"medium","evidence":"数值错误","suggestion":"核对原文"}]}`}}}
	a := Activities{LLM: provider}
	got, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "正文"})
	require.NoError(t, err)
	require.Equal(t, "revise", got.Status)
	require.Contains(t, provider.requests[0].System, "至少两点同口径子集")
	require.Contains(t, provider.requests[0].System, "复合增长率")
	require.Contains(t, provider.requests[0].System, "统计对象、单位倍率有误")
}
