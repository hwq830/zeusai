package workflows

import (
	"context"
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
	require.Equal(t, 2, strings.Count(got.Markdown, "```zeus-chart"))
	require.Contains(t, got.Markdown, "城乡出游（转载，原文未核对）")
	require.NotContains(t, got.Markdown, `"title":"城乡出游"`)
	require.Contains(t, got.Markdown, "年度国内出游人次")
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
