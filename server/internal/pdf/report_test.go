package pdf

import (
	"bytes"
	"context"
	"os"
	"testing"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func TestChromiumReportWithJSChart(t *testing.T) {
	path := os.Getenv("ZEUS_TEST_CHROMIUM")
	if path == "" {
		t.Skip("ZEUS_TEST_CHROMIUM not set")
	}
	markdown := "## 市场\n\n```zeus-chart\n{\"anchor\":\"一、市场\",\"type\":\"bar\",\"title\":\"JS 图表\",\"unit\":\"万人\",\"level\":\"L1\",\"series\":[{\"name\":\"基准\",\"points\":[{\"label\":\"2024\",\"value\":100},{\"label\":\"2025\",\"value\":200}]}],\"metric_definition\":\"测试指标\"}\n```\n"
	page, err := reporttemplate.Render(reporttemplate.Data{Title: "JS 报告", Markdown: markdown, Sources: []reporttemplate.Source{{Index: 1, Title: "示例来源"}}})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(page, []byte(`id="c0"`)) {
		t.Fatal("JS chart placeholder missing")
	}
	output, err := (Chromium{Path: path, NoSandbox: true}).Render(context.Background(), page)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(output, []byte("%PDF")) || len(output) < 1000 {
		t.Fatal("invalid JS chart PDF")
	}
}
