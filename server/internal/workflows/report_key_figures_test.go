package workflows

import (
	"bytes"
	"context"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/llm"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
	"github.com/stretchr/testify/require"
)

const keyFigureBody = "## 一、市场\n\n2024年规模294.16亿元，增速85.4%，用户3.82亿。"

func keyFigureDraftJSON(figures string) string {
	return `{"summary":"s","markdown":` + jsonString(keyFigureBody) + `,"keyFigures":` + figures + `}`
}

func renderCover(t *testing.T, markdown string) string {
	t.Helper()
	page, err := reporttemplate.Render(reporttemplate.Data{Title: "报告", Markdown: markdown, SourceCount: 3, ReadMinutes: 7})
	require.NoError(t, err)
	return string(page)
}

func TestDraftKeyFiguresRenderCoverAndMeta(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: keyFigureDraftJSON(`[
		{"label":"2024年AI原生月活","value":"4.99","unit":"亿","cite":2},
		{"label":"市场规模","value":"294.16","unit":"亿元"},
		{"label":"增速","value":"85.4","unit":"%"}
	]`)}}}
	// The fixture body must contain the first figure too.
	body := keyFigureBody + " AI原生App月活4.99亿。"
	provider.responses[0].Content = `{"summary":"s","markdown":` + jsonString(body) + `,"keyFigures":[
		{"label":"2024年AI原生月活","value":"4.99","unit":"亿","cite":2},
		{"label":"市场规模","value":"294.16","unit":"亿元"},
		{"label":"增速","value":"85.4","unit":"%"}
	]}`
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Len(t, out.KeyFigures, 3)
	require.Contains(t, out.Markdown, "zeus-key-figures")
	html := renderCover(t, out.Markdown)
	require.Contains(t, html, `class="cover-stats"`)
	require.Equal(t, 1, strings.Count(html, `class="cover-stats"`))
	require.Contains(t, html, "294.16")
	require.Contains(t, html, "85.4")
	require.Contains(t, html, "7 分钟")
	require.Contains(t, html, "3 个来源")
	require.NotContains(t, html, "CHARTS")
	require.NotContains(t, html, "SECTIONS")
	require.Contains(t, provider.requests[0].System, "keyFigures")
}

func TestDraftEmptyKeyFiguresOmitsCover(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: keyFigureDraftJSON(`[]`)}}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Empty(t, out.KeyFigures)
	require.NotContains(t, renderCover(t, out.Markdown), `class="cover-stats"`)
}

func TestDraftDropsKeyFigureNotInBody(t *testing.T) {
	var logs bytes.Buffer
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(old)
	provider := &sequenceProvider{responses: []llm.Response{{Content: keyFigureDraftJSON(`[
		{"label":"市场规模","value":"294.16","unit":"亿元"},
		{"label":"不存在","value":"999","unit":"亿元"}
	]`)}}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Len(t, out.KeyFigures, 1)
	require.Equal(t, "市场规模", out.KeyFigures[0].Label)
	require.Contains(t, logs.String(), "report key figure dropped")
	require.Contains(t, logs.String(), "reason=not_in_body")
}

func TestDraftAllInvalidKeyFiguresOmitCover(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: keyFigureDraftJSON(`[
		{"label":"不存在A","value":"111","unit":"亿元"},
		{"label":"不存在B","value":"222","unit":"亿元"}
	]`)}}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Empty(t, out.KeyFigures)
	require.NotContains(t, renderCover(t, out.Markdown), `class="cover-stats"`)
}

func TestDraftTruncatesKeyFiguresToFour(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: keyFigureDraftJSON(`[
		{"label":"A","value":"294.16","unit":"亿元"},
		{"label":"B","value":"85.4","unit":"%"},
		{"label":"C","value":"3.82","unit":"亿"},
		{"label":"D","value":"294.16","unit":"亿元"},
		{"label":"E","value":"85.4","unit":"%"}
	]`)}}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.Len(t, out.KeyFigures, 4)
}

func TestReviseKeyFiguresRenderCover(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: keyFigureDraftJSON(`[
		{"label":"市场规模","value":"294.16","unit":"亿元"}
	]`)}}}
	a := Activities{LLM: provider}
	out, err := a.ReviseReport(context.Background(), ReportPlan{}, nil, reportDraft{Markdown: "## 一、市场\n\n正文"}, reportReview{Status: "revise"})
	require.NoError(t, err)
	require.Len(t, out.KeyFigures, 1)
	require.Contains(t, renderCover(t, out.Markdown), `class="cover-stats"`)
}

func TestOldReportWithoutKeyFiguresHasNoCover(t *testing.T) {
	provider := &sequenceProvider{responses: []llm.Response{{Content: `{"summary":"s","markdown":"## 一、市场\n\n正文"}`}}}
	a := Activities{LLM: provider}
	out, err := a.DraftReport(context.Background(), ReportPlan{}, nil, nil)
	require.NoError(t, err)
	require.NotContains(t, renderCover(t, out.Markdown), `class="cover-stats"`)
}

func TestKeyFiguresKeepNavAndSectionsBalanced(t *testing.T) {
	figures := []reporttemplate.KeyFigure{{Label: "规模", Value: "294.16", Unit: "亿元"}}
	md := reporttemplate.AttachKeyFigures("## 一、市场\n\n规模 294.16 亿元。\n\n## 二、竞争\n\n正文。", figures)
	page, err := reporttemplate.Render(reporttemplate.Data{Title: "报告", Markdown: md, Sources: []reporttemplate.Source{{Index: 1, Title: "来源"}}})
	require.NoError(t, err)
	html := string(page)
	nav := regexp.MustCompile(`id="nav"[\s\S]*?</nav>`).FindString(html)
	require.NotEmpty(t, nav)
	require.Equal(t, strings.Count(nav, `href="#s`), strings.Count(html, `<section id="s`))
}
