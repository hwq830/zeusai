package workflows

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/stretchr/testify/require"
)

func TestVisualCandidateHintsRequireMatchedSourceAndReportSentences(t *testing.T) {
	findings := []QuestionNotes{
		{Question: "Q1", Sources: []search.Result{{URL: "https://example.test/2023"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2023年，出口总量48.91亿。"}}},
		{Question: "Q2", Sources: []search.Result{{URL: "https://example.test/2024"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2024年，出口总量56.15亿。"}}},
	}
	body := "2023年，出口总量48.91亿 [1]。2024年，出口总量56.15亿 [2]。"
	check := func(data []QuestionNotes, report string) int {
		_, sources := mergeNotes(data)
		return len(visualCandidateHints(data, report, len(sources)))
	}
	hints := visualCandidateHints(findings, body, 2)
	require.Len(t, hints, 2)
	require.Equal(t, "出口总量", hints[0].Metric)
	require.Equal(t, "亿", hints[0].Unit)
	require.Equal(t, 1, hints[0].Source)
	require.Equal(t, 2, hints[1].Source)
	require.Equal(t, "48.91", string(hints[0].Value))
	require.Contains(t, hints[0].SourceContext, "出口总量48.91亿")
	require.Contains(t, hints[0].ReportContext, "[1]")
	require.Equal(t, 1, check(findings, strings.Replace(body, "[2]", "[1]", 1)))
	require.Equal(t, 1, check(findings, strings.Replace(body, "56.15", "99.15", 1)))
	require.Equal(t, 1, check(findings, strings.Replace(body, "出口总量56.15亿", "出口收入56.15亿", 1)))
	require.Equal(t, 1, check(findings, strings.Replace(body, "2024年，", "2024年，预计", 1)))
	require.Equal(t, 1, check(findings, strings.Replace(body, "2024年，", "2024年，第一季度", 1)))
}

func TestVisualCandidateHintsKeepDifferentRegionsSeparate(t *testing.T) {
	findings := []QuestionNotes{
		{Sources: []search.Result{{URL: "https://example.test/a"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "甲省：1998年，出口总量12.50万吨。"}}},
		{Sources: []search.Result{{URL: "https://example.test/b"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "乙省：1999年，出口总量14万吨。"}}},
	}
	body := "甲省：1998年，出口总量12.5万吨 [1]。乙省：1999年，出口总量14万吨 [2]。"
	hints := visualCandidateHints(findings, body, 2)
	require.Len(t, hints, 2)
	require.Equal(t, "1998", hints[0].Year)
	require.Equal(t, "1999", hints[1].Year)
	require.Equal(t, "12.50", string(hints[0].Value)) // preserve the source's original precision
	require.Contains(t, hints[0].SourceContext, "甲省")
	require.Contains(t, hints[0].ReportContext, "甲省")
	require.Contains(t, hints[1].SourceContext, "乙省")
	require.Contains(t, hints[1].ReportContext, "乙省")
	encoded, err := json.Marshal(hints)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), `"points"`)
	require.NotContains(t, string(encoded), `"period"`)
}

func TestVisualCandidateHintsKeepSourceContextAndIgnoreUncitedConflicts(t *testing.T) {
	findings := []QuestionNotes{
		{Sources: []search.Result{{URL: "https://example.test/2023"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "甲地区统计。\n2023年，出口总量48.91亿。"}}},
		{Sources: []search.Result{{URL: "https://example.test/conflict"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "乙地区统计。\n2023年，出口总量59.15亿。"}}},
	}
	body := "甲地区：2023年，出口总量48.91亿 [1]。"
	hints := visualCandidateHints(findings, body, 2)
	require.Len(t, hints, 1)
	require.Contains(t, hints[0].SourceContext, "甲地区统计")
	require.NotContains(t, hints[0].SourceContext, "乙地区")
}
