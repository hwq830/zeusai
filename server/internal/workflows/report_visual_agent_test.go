package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/search"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"github.com/stretchr/testify/require"
)

func agentVisualSpec(anchor, level, typ string) reporttemplate.VisualSpec {
	spec := reporttemplate.VisualSpec{
		Anchor: anchor,
		Type:   typ,
		Title:  "测试图 " + level,
		Unit:   "万人",
		Level:  level,
		Series: []reporttemplate.Series{{
			Name:   "基准",
			Points: []reporttemplate.Point{{Label: "2024", Value: 100}, {Label: "2025", Value: 200}},
		}},
		MetricDefinition: "测试指标",
		Caption:          "测试说明",
	}
	switch level {
	case "L2":
		spec.Formula = "200 / 100 = 200%"
		spec.Inputs = []string{"[1] 100", "[3] 200"}
	case "L3":
		spec.Formula = "CAGR = (200 / 100)^(1/1) - 1"
		spec.Inputs = []string{"[1] 100", "[3] 200"}
		spec.Assumptions = []string{"2024 起点 100。", "2025 终点 200。"}
	}
	return spec
}

func TestPlanVisualsDropsInvalidAndKeepsValid(t *testing.T) {
	valid := agentVisualSpec("一、规模", "L1", "bar")
	invalid := agentVisualSpec("一、规模", "L2", "bar")
	invalid.Formula = ""
	response, err := json.Marshal(map[string]any{"visuals": []reporttemplate.VisualSpec{valid, invalid}})
	require.NoError(t, err)
	provider := &sequenceProvider{responses: []llm.Response{{Content: string(response)}}}
	a := &Activities{LLM: provider}
	findings := []QuestionNotes{{
		Question: "Q",
		Notes:    "2024 100 万人，2025 200 万人 [1]",
		Sources:  []search.Result{{URL: "https://example.test"}},
	}}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{}, findings, []string{"一、规模"})
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, "L1", got[0].Level)
	require.Len(t, provider.requests, 1)
	require.Equal(t, llm.TierStrong, provider.requests[0].Tier)
	require.Equal(t, 8000, provider.requests[0].MaxTokens)
	require.Equal(t, "report.visual", provider.requests[0].Purpose)
	require.Contains(t, provider.requests[0].Messages[0].Content, `"headings":["一、规模"]`)
}

func TestPlanVisualsAcceptsLegacyChartsKeyAndTruncatesSix(t *testing.T) {
	visuals := make([]reporttemplate.VisualSpec, 7)
	for i := range visuals {
		visuals[i] = agentVisualSpec("一、规模", "L1", "bar")
	}
	response, err := json.Marshal(map[string]any{"charts": visuals})
	require.NoError(t, err)
	provider := &sequenceProvider{responses: []llm.Response{{Content: string(response)}}}
	a := &Activities{LLM: provider}
	findings := []QuestionNotes{{Question: "Q", Notes: "2024 100 2025 200 [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{}, findings, []string{"一、规模"})
	require.NoError(t, err)
	require.Len(t, got, 6, "batch limit must cap at six")
}

func TestPlanVisualsNoSourcesDoesNotCallModel(t *testing.T) {
	provider := &sequenceProvider{}
	a := &Activities{LLM: provider}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{}, nil, []string{"一、规模"})
	require.NoError(t, err)
	require.Empty(t, got)
	require.Empty(t, provider.requests)
}

func TestPlanVisualsEmptyHeadingsDoesNotCallModel(t *testing.T) {
	provider := &sequenceProvider{}
	a := &Activities{LLM: provider}
	findings := []QuestionNotes{{Question: "Q", Notes: "2024 100 [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{}, findings, nil)
	require.NoError(t, err)
	require.Empty(t, got)
	require.Empty(t, provider.requests)
}

func TestReportHeadingsExtractsH2H3AndSkipsCode(t *testing.T) {
	md := "# 报告\n\n## 一、规模\n\n### 1.1 场地\n\n```\n## 代码里的假标题\n```\n\n## 五、未来趋势研判（2026-2030）\n"
	require.Equal(t, []string{"一、规模", "1.1 场地", "五、未来趋势研判（2026-2030）"}, reportHeadings(md))
}

func TestReviseReportPromptKeepsHeadings(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"summary":"s","markdown":"## 一、规模\n\n修订正文"}`}}}
	a := Activities{LLM: provider}
	_, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、规模\n\n正文"}, reportReview{Status: "revise", Issues: []reviewIssue{{Severity: "high", Evidence: "e", Suggestion: "s"}}})
	require.NoError(t, err)
	require.Contains(t, provider.requests[0].System, "保留所有 ## / ### 章节标题原文不变")
	require.Contains(t, provider.requests[0].System, "不得新增或删除章节标题")
}

func TestPlanVisualsModelErrorIsReturnedForIsolation(t *testing.T) {
	a := &Activities{LLM: qualityProvider{err: errors.New("model down")}}
	findings := []QuestionNotes{{Question: "Q", Notes: "2024 100 [1]", Sources: []search.Result{{URL: "https://example.test"}}}}
	got, err := a.PlanVisuals(context.Background(), ReportPlan{}, findings, []string{"一、规模"})
	require.Error(t, err)
	require.Empty(t, got)
}

func TestInsertVisualsAnchorsAndOrder(t *testing.T) {
	md := "# Report\n\n## 一、规模\n\n正文一\n\n## 五、未来趋势研判（2026-2030）\n\n正文二\n"
	first := agentVisualSpec("一、规模", "L1", "bar")
	second := agentVisualSpec("五、未来趋势研判（2026-2030）", "L3", "line")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{first, second})
	require.Equal(t, 2, inserted)
	require.Equal(t, 0, missing)
	require.Equal(t, 2, strings.Count(got, "```zeus-chart"))
	heading1 := strings.Index(got, "## 一、规模")
	chart1 := strings.Index(got, `"anchor":"一、规模"`)
	heading2 := strings.Index(got, "## 五、未来趋势研判（2026-2030）")
	chart2 := strings.Index(got, `"anchor":"五、未来趋势研判（2026-2030）"`)
	require.Less(t, heading1, chart1)
	require.Less(t, chart1, heading2)
	require.Less(t, heading2, chart2)
}

func TestInsertVisualsMissingAnchorDropsWithoutAppending(t *testing.T) {
	md := "# Report\n\n## 一、规模\n\n正文\n"
	spec := agentVisualSpec("不存在的章节", "L1", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 0, inserted)
	require.Equal(t, 1, missing)
	require.Equal(t, md, got)
}

func TestInsertVisualsDuplicateAnchorKeepsBoth(t *testing.T) {
	md := "## 一、规模\n\n正文\n"
	first := agentVisualSpec("一、规模", "L1", "bar")
	second := agentVisualSpec("一、规模", "L2", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{first, second})
	require.Equal(t, 2, inserted)
	require.Equal(t, 0, missing)
	require.Equal(t, 2, strings.Count(got, "```zeus-chart"))
}

func TestInsertVisualsIsIdempotent(t *testing.T) {
	md := "## 一、规模\n\n正文\n"
	spec := agentVisualSpec("一、规模", "L1", "bar")
	once, inserted, _ := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 1, inserted)
	// Clear-then-insert is idempotent in state: the second call replaces the
	// block rather than appending it.
	twice, inserted, _ := insertVisuals(uuid.Nil, once, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 1, inserted)
	require.Equal(t, once, twice)
	require.Equal(t, 1, strings.Count(twice, "```zeus-chart"))
}

func TestInsertVisualsClearsStaleChartBlocks(t *testing.T) {
	md := "## 一、规模\n\n```zeus-chart\n{\"anchor\":\"一、规模\",\"title\":\"旧标题\",\"level\":\"L1\"}\n```\n\n正文\n"
	spec := agentVisualSpec("一、规模", "L1", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 1, inserted)
	require.Equal(t, 0, missing)
	require.Equal(t, 1, strings.Count(got, "```zeus-chart"))
	require.NotContains(t, got, "旧标题")
	require.Contains(t, got, `"title":"测试图 L1"`)
}

func TestInsertVisualsDoesNotUseHeadingInsideCodeFence(t *testing.T) {
	md := "```\n## 一、规模\n```\n\n正文\n"
	spec := agentVisualSpec("一、规模", "L1", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 0, inserted)
	require.Equal(t, 1, missing)
	require.Equal(t, md, got)
}

func TestInsertVisualsRequiresExactAnchor(t *testing.T) {
	md := "## 一、规模（2026）\n\n正文\n"
	spec := agentVisualSpec("一、规模", "L1", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 0, inserted)
	require.Equal(t, 1, missing)
	require.Equal(t, md, got)
}

func TestInsertVisualsDropsInvalidSpec(t *testing.T) {
	md := "## 一、规模\n\n正文\n"
	spec := agentVisualSpec("一、规模", "L2", "bar")
	spec.Formula = ""
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 0, inserted)
	require.Equal(t, 0, missing)
	require.NotContains(t, got, "```zeus-chart")
}

func TestInsertVisualsDoesNotUseTildeFenceHeading(t *testing.T) {
	md := "~~~\n## 一、规模\n~~~\n\n正文\n"
	spec := agentVisualSpec("一、规模", "L1", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 0, inserted)
	require.Equal(t, 1, missing)
	require.Equal(t, md, got)
}

func TestInsertVisualsUsesFirstDuplicateHeading(t *testing.T) {
	md := "## 一、规模\n\n第一段\n\n## 一、规模\n\n第二段\n"
	spec := agentVisualSpec("一、规模", "L1", "bar")
	got, inserted, missing := insertVisuals(uuid.Nil, md, []reporttemplate.VisualSpec{spec})
	require.Equal(t, 1, inserted)
	require.Equal(t, 0, missing)
	first := strings.Index(got, "## 一、规模")
	chart := strings.Index(got, `"anchor":"一、规模"`)
	second := strings.Index(got[first+1:], "## 一、规模") + first + 1
	require.Less(t, first, chart)
	require.Less(t, chart, second)
}
