package workflows

import (
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
		return len(visualCandidateHints(data, report, mergeNumericEvidence(data), len(sources)))
	}
	hints := visualCandidateHints(findings, body, mergeNumericEvidence(findings), 2)
	require.Len(t, hints, 1)
	require.Equal(t, "出口总量", hints[0].Scope)
	require.Equal(t, "亿", hints[0].Unit)
	require.Equal(t, 1, hints[0].Points[0].Source)
	require.Equal(t, 2, hints[0].Points[1].Source)
	require.Equal(t, "48.91", string(hints[0].Points[0].Value))
	require.Zero(t, check(findings, strings.Replace(body, "[2]", "[1]", 1)))
	require.Zero(t, check(findings, strings.Replace(body, "56.15", "99.15", 1)))
	require.Zero(t, check(findings, strings.Replace(body, "出口总量56.15亿", "出口收入56.15亿", 1)))
	require.Zero(t, check(findings, strings.Replace(body, "2024年，", "2024年，预计", 1)))
	require.Zero(t, check(findings, strings.Replace(body, "2024年，", "2024年，第一季度", 1)))
}

func TestVisualCandidateHintsExcludeConflictingSourceValues(t *testing.T) {
	findings := []QuestionNotes{
		{Sources: []search.Result{{URL: "https://example.test/2023"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2023年，出口总量48.91亿。"}}},
		{Sources: []search.Result{{URL: "https://example.test/2024"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2024年，出口总量56.15亿。"}}},
		{Sources: []search.Result{{URL: "https://example.test/conflict"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "2024年，出口总量59.15亿。"}}},
	}
	body := "2023年，出口总量48.91亿 [1]。2024年，出口总量56.15亿 [2]。"
	require.Empty(t, visualCandidateHints(findings, body, mergeNumericEvidence(findings), 3))
}

func TestVisualCandidateHintsAreNotTiedToRecentYearsOrTopic(t *testing.T) {
	findings := []QuestionNotes{
		{Sources: []search.Result{{URL: "https://example.test/a"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "1998年，出口总量12.50万吨。"}}},
		{Sources: []search.Result{{URL: "https://example.test/b"}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: "1999年，出口总量14万吨。"}}},
	}
	body := "1998年，出口总量12.5万吨 [1]。1999年，出口总量14万吨 [2]。"
	hints := visualCandidateHints(findings, body, mergeNumericEvidence(findings), 2)
	require.Len(t, hints, 1)
	require.Equal(t, "万吨", hints[0].Unit)
	require.Equal(t, "1998—1999", hints[0].Period)
}
