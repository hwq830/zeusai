package pdf

import (
	"bytes"
	"context"
	"os"
	"testing"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func TestChromiumReportWithStaticChart(t *testing.T) {
	path := os.Getenv("ZEUS_TEST_CHROMIUM")
	if path == "" {
		t.Skip("ZEUS_TEST_CHROMIUM not set")
	}
	markdown := "## 市场\n\n```zeus-chart\n{\"type\":\"bar\",\"title\":\"触达（示例）\",\"unit\":\"指数\",\"period\":\"2025\",\"points\":[{\"label\":\"A\",\"value\":25,\"source\":1},{\"label\":\"B\",\"value\":80,\"source\":1}]}\n```\n"
	page, err := reporttemplate.Render(reporttemplate.Data{Title: "报告", Markdown: markdown, Sources: []reporttemplate.Source{{Index: 1, Title: "示例来源"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(page, []byte(`class="report-chart"`)) {
		t.Fatal("chart missing from printable HTML")
	}
	output, err := (Chromium{Path: path, NoSandbox: true}).Render(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(output, []byte("%PDF")) || len(output) < 1000 {
		t.Fatal("invalid report PDF")
	}
}
