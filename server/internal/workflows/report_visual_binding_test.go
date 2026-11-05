package workflows

import (
	"context"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/stretchr/testify/require"
)

func bindingFindings(source string) []QuestionNotes {
	return []QuestionNotes{{
		Question:             "visual",
		Notes:                "visual [1]",
		Sources:              []search.Result{{URL: "https://example.test/binding"}},
		SourceExcerpts:       []SourceExcerpt{{Source: 1, Text: source}},
		SourceReadStatus:     []string{"read"},
		SourceReadsAttempted: 1,
		SourceReadsSucceeded: 1,
	}}
}

func TestReportVisualPipelineBindsRateBeforeLabel(t *testing.T) {
	source := "IDC全球2024年12月份的调研发现，69%的人使用了生成式AI应用，22%的人每天都在使用生成式AI应用 [1]。"
	body := "IDC全球2024年12月份的调研发现，69%的人使用了生成式AI应用，22%的人每天都在使用生成式AI应用 [1]。"
	chart := `{"charts":[{"type":"bar","title":"生成式AI使用率","unit":"%","period":"2024","scope":"全球","points":[{"label":"生成式AI应用","value":69,"source":1},{"label":"每天","value":22,"source":1}]}]}`
	a := Activities{LLM: qualityProvider{result: chart}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, bindingFindings(source), reportDraft{Markdown: body}, "")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(got.Markdown, "```zeus-chart"))
}

func TestReportVisualPipelineBindsRespectivelyList(t *testing.T) {
	source := "《2025年AI应用层发展核心报告》显示，国内AI原生App月活分化。其中，豆包与DeepSeek分别以2.26亿和1.35亿的月活规模位列冠亚军 [1]。"
	body := "《2025年AI应用层发展核心报告》显示，国内AI原生App月活分化。其中，豆包与DeepSeek分别以2.26亿和1.35亿的月活规模位列冠亚军 [1]。"
	chart := `{"charts":[{"type":"bar","title":"AI原生App月活","unit":"亿","period":"2025","scope":"国内","points":[{"label":"豆包","value":2.26,"source":1},{"label":"DeepSeek","value":1.35,"source":1}]}]}`
	a := Activities{LLM: qualityProvider{result: chart}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, bindingFindings(source), reportDraft{Markdown: body}, "")
	require.NoError(t, err)
	require.Equal(t, 1, strings.Count(got.Markdown, "```zeus-chart"))
}

func TestReportVisualPipelineRejectsSwappedRespectivelyValues(t *testing.T) {
	source := "《2025年AI应用层发展核心报告》显示，国内AI原生App月活分化。其中，豆包与DeepSeek分别以2.26亿和1.35亿的月活规模位列冠亚军 [1]。"
	body := source
	chart := `{"charts":[{"type":"bar","title":"AI原生App月活","unit":"亿","period":"2025","scope":"国内","points":[{"label":"豆包","value":1.35,"source":1},{"label":"DeepSeek","value":2.26,"source":1}]}]}`
	a := Activities{LLM: qualityProvider{result: chart}}
	got, err := a.PrepareReportVisuals(context.Background(), ReportPlan{}, bindingFindings(source), reportDraft{Markdown: body}, "")
	// The swapped chart is rejected; the safety net may turn that into a
	// visible no-chart report, but it must never publish the wrong chart.
	require.NotContains(t, got.Markdown, "```zeus-chart")
	_ = err
}
