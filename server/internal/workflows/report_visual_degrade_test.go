package workflows

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"testing"

	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/stretchr/testify/require"
)

func captureWorkflowLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buffer bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buffer, &slog.HandlerOptions{Level: slog.LevelInfo})))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return &buffer
}

func qualitativeVisualEvidence() ([]QuestionNotes, string) {
	findings := []QuestionNotes{{
		Question:             "政策",
		Notes:                "政策强调公共服务 [1]",
		Sources:              []search.Result{{URL: "https://example.test/policy"}},
		SourceExcerpts:       []SourceExcerpt{{Source: 1, Text: "政策强调公共服务。"}},
		SourceReadStatus:     []string{"read"},
		SourceReadsAttempted: 1,
		SourceReadsSucceeded: 1,
	}}
	return findings, "政策强调公共服务 [1]。"
}

func comparableVisualEvidence() ([]QuestionNotes, string) {
	findings := []QuestionNotes{{
		Question:             "市场规模",
		Notes:                "市场规模 [1]",
		Sources:              []search.Result{{URL: "https://example.test/market"}},
		SourceExcerpts:       []SourceExcerpt{{Source: 1, Text: "数据显示， 2024 年 中国 Al 大模型 市场 规模约为294.16亿元，预计2026年将突破700亿元。"}},
		SourceReadStatus:     []string{"read"},
		SourceReadsAttempted: 1,
		SourceReadsSucceeded: 1,
	}}
	body := "## 市场规模\n\n| 中国AI大模型市场规模 | 约294.16亿元 | 2024年 | [1] |\n| 中国AI大模型市场规模 | 突破700亿元 | 2026年（预测） | [1] |"
	return findings, body
}

func emptyVisualResponses() []llm.Response {
	return []llm.Response{
		{Content: `{"charts":[],"no_chart_reason":"insufficient_comparable_data"}`},
		{Content: `{"charts":[],"no_chart_reason":"insufficient_comparable_data","no_chart_explanation":"没有可比数据"}`},
	}
}

func TestVisualPotentialSeriesFindsSameSourceActualAndForecast(t *testing.T) {
	findings, body := comparableVisualEvidence()
	ok, evidence := visualPotentialSeries(findings, body)
	require.True(t, ok)
	require.Contains(t, evidence, "亿元@[1]")
	require.Contains(t, evidence, "2024")
	require.Contains(t, evidence, "2026")
}

func TestVisualPotentialSeriesIgnoresQualitativeReport(t *testing.T) {
	findings, body := qualitativeVisualEvidence()
	ok, evidence := visualPotentialSeries(findings, body)
	require.False(t, ok)
	require.Equal(t, visualDegradeGateNone, evidence)
}

func TestVisualDegradeWritesVisibleNoteAndLog(t *testing.T) {
	logs := captureWorkflowLogs(t)
	findings, body := qualitativeVisualEvidence()
	provider := &sequenceProvider{responses: emptyVisualResponses()}
	a := Activities{LLM: provider}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "本主题在所给材料中没有可比较的同口径多期数据")
	require.Contains(t, logs.String(), "report visual degraded")
	require.Contains(t, logs.String(), "reason=insufficient_comparable_data")
	require.Contains(t, logs.String(), "candidates=0")
	require.Contains(t, logs.String(), "planning_attempts=2")
	require.Contains(t, logs.String(), "gate_evidence=none")
	require.Contains(t, logs.String(), "本主题在所给材料中没有可比较的同口径多期数据")
}

func TestVisualDegradeBlockedWhenSeriesExists(t *testing.T) {
	logs := captureWorkflowLogs(t)
	findings, body := comparableVisualEvidence()
	provider := &sequenceProvider{responses: emptyVisualResponses()}
	a := Activities{LLM: provider}
	_, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, "")
	require.ErrorIs(t, err, errVisualDataPresent)
	require.NotContains(t, logs.String(), "report visual degraded")
	require.Contains(t, logs.String(), "report visual degrade blocked")
	require.Contains(t, logs.String(), "亿元@[1]:2024,2026")
}

func TestVisualDegradeNoSources(t *testing.T) {
	logs := captureWorkflowLogs(t)
	a := Activities{LLM: qualityProvider{err: errors.New("unexpected model call")}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "正文"}, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "没有可核验的来源")
	require.Contains(t, logs.String(), "reason=no_sources")
	require.Contains(t, logs.String(), "planning_attempts=0")
}

func TestVisualDegradeAllCandidatesRejected(t *testing.T) {
	logs := captureWorkflowLogs(t)
	findings, body := qualitativeVisualEvidence()
	const bad = `{"charts":[{"type":"bar","title":"x","unit":"%","period":"2025","points":[{"label":"A","value":1,"source":1},{"label":"B","value":2,"source":1}]}]}`
	provider := &sequenceProvider{responses: []llm.Response{{Content: bad}, {Content: bad}}}
	a := Activities{LLM: provider}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, findings, reportDraft{Markdown: body}, "")
	require.NoError(t, err)
	require.Contains(t, got.Markdown, "本主题在所给材料中没有可比较的同口径多期数据")
	require.Contains(t, logs.String(), "report visual degraded")
	require.Contains(t, logs.String(), "candidates=1")
}

func TestVisualDegradeRejectsMissingExplanation(t *testing.T) {
	require.Error(t, verifyVisualDegradeNote("正文", "视觉说明：本主题在所给材料中没有可比较的同口径多期数据。"))
	require.NoError(t, verifyVisualDegradeNote("正文\n\n视觉说明：本主题在所给材料中没有可比较的同口径多期数据。", "视觉说明：本主题在所给材料中没有可比较的同口径多期数据。"))
}

func TestReviewVerdictLogAndNoChartRule(t *testing.T) {
	logs := captureWorkflowLogs(t)
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"status":"pass","issues":[]}`}}}
	a := Activities{LLM: provider}
	_, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "正文 [1]。"})
	require.NoError(t, err)
	require.Contains(t, provider.requests[0].System, "不得因缺图判 revise")
	require.Contains(t, provider.requests[0].System, "空泛")
	require.Contains(t, logs.String(), "report review verdict")
	require.Contains(t, logs.String(), "status=pass")
	require.Contains(t, logs.String(), "issue_count=0")
	require.Contains(t, logs.String(), "severity_high=0")
	require.Contains(t, logs.String(), "used_fallback=false")
}

func TestReviewVerdictCountsSeverity(t *testing.T) {
	logs := captureWorkflowLogs(t)
	response := `{"status":"revise","issues":[{"severity":"medium","evidence":"e1","suggestion":"s1"},{"severity":"medium","evidence":"e2","suggestion":"s2"},{"severity":"low","evidence":"e3","suggestion":"s3"}]}`
	provider := &sequenceProvider{responses: []llm.Response{{Content: response}}}
	a := Activities{LLM: provider}
	got, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "正文 [1]。"})
	require.NoError(t, err)
	require.Equal(t, "revise", got.Status)
	require.Contains(t, logs.String(), "issue_count=3")
	require.Contains(t, logs.String(), "severity_high=0")
	require.Contains(t, logs.String(), "severity_medium=2")
	require.Contains(t, logs.String(), "severity_low=1")
}

func TestReviewVerdictMarksFallback(t *testing.T) {
	logs := captureWorkflowLogs(t)
	provider := &sequenceProvider{responses: []llm.Response{{FinishReason: "length"}, {Content: `{"status":"pass","issues":[]}`}}}
	a := Activities{LLM: provider}
	_, err := a.ReviewReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "正文 [1]。"})
	require.NoError(t, err)
	require.Contains(t, logs.String(), "report review verdict")
	require.Contains(t, logs.String(), "used_fallback=true")
}
