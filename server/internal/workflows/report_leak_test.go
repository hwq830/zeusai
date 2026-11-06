package workflows

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/llm"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"github.com/stretchr/testify/require"
)

const leakedReview = `## 一、市场

正文。

## 审查说明

模型独立审查发现以下待核实问题；这不是对原始来源的事实核验：

- high: 核心结论称两个数字指向同一量级，但口径不同。
- medium: 图表单位不统一。

## 来源

[1] 来源一
`

func TestStripReviewLeakKeepsSources(t *testing.T) {
	got, chars, reason := stripReviewLeaks(leakedReview)
	require.NotContains(t, got, "审查说明")
	require.NotContains(t, got, "high:")
	require.Contains(t, got, "## 来源")
	require.Contains(t, got, "正文")
	require.Greater(t, chars, 0)
	require.Equal(t, "review_severity_list", reason)
}

func TestStripReviewLeakTitleOnlyIsKept(t *testing.T) {
	md := "## 一、市场\n\n正文\n\n## 审查说明\n\n本节说明内容审核政策的适用范围。\n\n## 来源\n\n[1] 来源一\n"
	got, chars, reason := stripReviewLeaks(md)
	require.Equal(t, md, got)
	require.Zero(t, chars)
	require.Empty(t, reason)
}

func TestStripReviewLeakMultipleSections(t *testing.T) {
	md := `## 一、市场

正文。

## 审查说明

- high: 第一个泄漏。

## 自检说明

待核实问题：第二个泄漏。

## 来源

[1] 来源一
`
	got, chars, reason := stripReviewLeaks(md)
	require.NotContains(t, got, "审查说明")
	require.NotContains(t, got, "自检说明")
	require.NotContains(t, got, "待核实问题")
	require.Contains(t, got, "## 来源")
	require.Greater(t, chars, 0)
	require.Equal(t, "review_severity_list", reason)
}

func TestStripReviewLeakNoLeakIsByteIdentical(t *testing.T) {
	md := "## 一、市场\n\n正文 [1]。\n\n## 来源\n\n[1] 来源一\n"
	got, chars, reason := stripReviewLeaks(md)
	require.Equal(t, md, got)
	require.Zero(t, chars)
	require.Empty(t, reason)
}

func TestReviewLeakHeadingAloneIsNotDetected(t *testing.T) {
	md := "## 审查说明\n\n这是正常的政策解释，不是审查意见。\n"
	_, chars, reason := stripReviewLeaks(md)
	require.Zero(t, chars)
	require.Empty(t, reason)
}

func TestDraftStripsReviewLeakAfterRetry(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.NotContains(t, out.Markdown, "审查说明")
	require.Contains(t, out.Markdown, "## 来源")
	require.Contains(t, logs.String(), "report review leak stripped")
	require.Contains(t, logs.String(), "reason=review_severity_list")
	require.Contains(t, provider.requests[0].System, "不得输出审查意见")
}

func TestDraftLeakRetryCanSucceed(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
		{Content: `{"summary":"s","markdown":"## 一、市场\n\n修订完成。"}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.NotContains(t, out.Markdown, "审查说明")
	require.Contains(t, out.Markdown, "修订完成")
}

func TestReviseStripsReviewLeakAfterBudget(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)

	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、市场\n\n正文"}, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.NotContains(t, out.Markdown, "审查说明")
	require.Contains(t, out.Markdown, "## 来源")
	require.Contains(t, logs.String(), "report review leak stripped")
	require.Contains(t, logs.String(), "chars=")
	require.Contains(t, logs.String(), "reason=review_severity_list")
	require.Contains(t, provider.requests[0].System, "不得输出审查意见")
}

func TestReviseLeakRetryCanSucceed(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
		{Content: `{"summary":"s","markdown":"## 一、市场\n\n修订完成。"}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、市场\n\n正文"}, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.NotContains(t, out.Markdown, "审查说明")
	require.Contains(t, out.Markdown, "修订完成")
}

func TestReviseTruncationThenLeakStripsWithoutThirdCall(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{
		{FinishReason: "length"},
		{Content: `{"summary":"s","markdown":` + jsonString(leakedReview) + `}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、市场\n\n正文"}, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.Equal(t, 24576, provider.requests[1].MaxTokens)
	require.NotContains(t, out.Markdown, "审查说明")
	require.Contains(t, out.Markdown, "## 来源")
}

func TestStrippedLeakDisappearsFromRenderedTOC(t *testing.T) {
	got, _, _ := stripReviewLeaks(leakedReview)
	page, err := reporttemplate.Render(reporttemplate.Data{Title: "报告", Markdown: got, Sources: []reporttemplate.Source{{Index: 1, Title: "来源一"}}})
	require.NoError(t, err)
	html := string(page)
	require.NotContains(t, html, "审查说明")
	nav := regexp.MustCompile(`id="nav"[\s\S]*?</nav>`).FindString(html)
	require.NotEmpty(t, nav)
	require.Equal(t, strings.Count(nav, `href="#s`), strings.Count(html, `<section id="s`))
}

func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func TestMultipleLeakStripLogsCorrectChars(t *testing.T) {
	md := `## 一、市场

正文。

## 审查说明

- high: 第一个泄漏。

## 自检说明

待核实问题：第二个泄漏。

## 来源

[1] 来源一
`
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)
	_, wantChars, _ := stripReviewLeaks(md)
	provider := &sequenceProvider{responses: []llm.Response{
		{Content: `{"summary":"s","markdown":` + jsonString(md) + `}`},
		{Content: `{"summary":"s","markdown":` + jsonString(md) + `}`},
	}}
	a := Activities{LLM: provider}
	out, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、市场\n\n正文"}, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, provider.requests, 2)
	require.NotContains(t, out.Markdown, "审查说明")
	require.NotContains(t, out.Markdown, "自检说明")
	require.Contains(t, out.Markdown, "## 来源")
	require.Contains(t, logs.String(), "report review leak stripped")
	require.Contains(t, logs.String(), "chars="+strconv.Itoa(wantChars))
}
