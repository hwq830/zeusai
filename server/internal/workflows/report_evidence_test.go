package workflows

import (
	"encoding/json"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/stretchr/testify/require"
)

func TestSourceExcerptSelectionUsesReadTextAndIsBounded(t *testing.T) {
	page := "首页 | 登录 | 搜索 | 帮助\n产品导航 | 注册 | 收藏 | 分享\n这里介绍城市政策如何保障公众公平获得服务。\n" + strings.Repeat("背景资料。", 220) + "该政策明确保护公众隐私并要求公布处理流程。"
	got := sourceExcerptsForQuery(page, 2, false, "政策 隐私 公众")
	require.NotEmpty(t, got)
	for _, e := range got {
		require.Equal(t, 2, e.Source)
		require.LessOrEqual(t, utf8.RuneCountInString(e.Text), 1200)
		require.Contains(t, page, e.Text)
		require.NotContains(t, e.Text, "导航")
	}
	text := ""
	for _, e := range got {
		text += e.Text
	}
	require.Contains(t, text, "政策")
	require.NotRegexp(t, `[0-9]`, text)

	_, unread := collectResearchExcerpts([]string{page}, []bool{false}, "政策")
	require.Empty(t, unread)
}

func TestNumericBudgetDoesNotStopLaterGeneralSources(t *testing.T) {
	var pages []string
	for i := 0; i < 4; i++ {
		pages = append(pages, "年度统计结果 2023 年 12 万人。\n\n另一年度统计结果 2024 年 15 万人。")
	}
	pages = append(pages, "这一来源说明公共服务政策如何保护公众隐私。")
	numeric, general := collectResearchExcerpts(pages, []bool{true, true, true, true, true}, "公共服务政策")
	require.Len(t, numeric, 8)
	require.LessOrEqual(t, len(general), 8)
	total, count := 0, map[int]int{}
	for _, e := range general {
		total += utf8.RuneCountInString(e.Text)
		count[e.Source]++
	}
	require.LessOrEqual(t, total, 8000)
	require.LessOrEqual(t, count[5], 2)
	require.Greater(t, count[5], 0)
}

func TestSourceExcerptsRetainCompleteSentencesFromClippedLongLine(t *testing.T) {
	wanted := "公共服务政策明确规定保障公众隐私与公平。"
	page := strings.Repeat("无关背景资料说明。", 4300) + wanted + strings.Repeat("尚未结束的尾部", 2000)
	text := clip(page, 40000)
	require.Greater(t, utf8.RuneCountInString(page), 40000)
	got := sourceExcerptsForQuery(text, 1, true, "公共服务政策 公众隐私")
	require.NotEmpty(t, got)
	var combined string
	for _, e := range got {
		combined += e.Text
		require.Contains(t, text, e.Text)
		require.LessOrEqual(t, utf8.RuneCountInString(e.Text), 1200)
	}
	require.Contains(t, combined, wanted)
	require.NotContains(t, combined, "尚未结束的尾部")
}

func TestSourceExcerptsRetainTablesAndDocumentReferences(t *testing.T) {
	table := "比较项目 | 公共服务政策 | 公众隐私保护"
	prose := "公共服务政策说明参见 report.pdf 文件，其中规定应保护公众隐私。"
	page := "首页 | 登录 | 搜索 | 帮助\n政策指南.docx VIP\n" + table + "\n" + prose
	got := sourceExcerptsForQuery(page, 1, false, "公共服务政策 隐私")
	require.Len(t, got, 2)
	combined := got[0].Text + "\n" + got[1].Text
	require.Contains(t, combined, table)
	require.Contains(t, combined, prose)
	require.NotContains(t, combined, "政策指南.docx VIP")
}

func TestEvidenceAggregatesSourcesAndFallsBackToNumericExcerpts(t *testing.T) {
	stamp := time.Date(2024, 10, 13, 16, 0, 0, 0, time.UTC)
	a := search.Result{URL: "https://example.test/a", Title: "A", PublishedAt: &stamp, Snippet: "UNVERIFIED_SNIPPET"}
	b := search.Result{URL: "https://example.test/b", Title: "B"}
	c := search.Result{URL: "https://example.test/c", Title: "C"}
	original := "原文说明制度明确保护公众隐私和权益。"
	numeric := "2024 城镇 16.3% 农村 9.9%"
	got := buildReportEvidence([]QuestionNotes{
		{Question: "一", Notes: "UNVERIFIED_NOTE 999", Sources: []search.Result{a, b}, SourceReadStatus: []string{"unknown", "failed"}, SourceExcerpts: []SourceExcerpt{{Source: 1, Text: original}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: numeric}}},
		{Question: "二", Notes: "another unsupported claim", Sources: []search.Result{b, a, c}, SourceReadStatus: []string{"read", "read", "not_attempted"}, SourceExcerpts: []SourceExcerpt{{Source: 2, Text: original}}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: numeric}}},
	})
	require.Len(t, got.Sources, 3)
	require.Equal(t, []int{1, 2}, got.Questions[0].Sources)
	require.Equal(t, []int{2, 1, 3}, got.Questions[1].Sources)
	require.Equal(t, "read", got.Sources[0].ReadStatus)
	require.Equal(t, "read", got.Sources[1].ReadStatus)
	require.True(t, got.Sources[1].HasExcerpt)
	require.Equal(t, []string{numeric}, got.Sources[1].Excerpts)
	require.False(t, got.Sources[2].HasExcerpt)
	require.Equal(t, "not_attempted", got.Sources[2].ReadStatus)
	require.Contains(t, got.Sources[2].Gap, "尚未读取页面")
	require.Equal(t, 1, strings.Count(strings.Join(got.Sources[0].Excerpts, "|"), original))
	raw, err := json.Marshal(got)
	require.NoError(t, err)
	for _, forbidden := range []string{"UNVERIFIED_NOTE", "UNVERIFIED_SNIPPET", "publishedAt", "dateLastCrawled", "2024-10-13"} {
		require.NotContains(t, string(raw), forbidden)
	}
	legacy := buildReportEvidence([]QuestionNotes{{Question: "旧任务", Notes: "DO_NOT_USE", Sources: []search.Result{a}, NumericExcerpts: []NumericExcerpt{{Source: 1, Text: numeric}}}})
	require.Equal(t, []string{numeric}, legacy.Sources[0].Excerpts)
	require.Equal(t, "unknown", legacy.Sources[0].ReadStatus)
}
