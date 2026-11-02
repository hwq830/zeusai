package reporttemplate

import (
	"strings"
	"testing"
)

const validChart = "```zeus-chart\n{\"type\":\"bar\",\"title\":\"渠道\",\"unit\":\"%\",\"period\":\"2025\",\"points\":[{\"label\":\"搜索\",\"value\":25,\"source\":1},{\"label\":\"社区\",\"value\":80,\"source\":1}]}\n```\n"

func TestFilterChartsRequiresCitedEvidence(t *testing.T) {
	evidence := "2025 搜索 25% 社区 80% [1]"
	if got := FilterCharts("正文\n\n"+validChart, evidence, 1); !strings.Contains(got, "zeus-chart") {
		t.Fatalf("supported chart removed: %s", got)
	}
	for _, missing := range []string{"2025 搜索 25% 社区 90% [1]", "2025 搜索 25% 社区 80% [2]", "2024 搜索 25% 社区 80% [1]", "2025 搜索 25 社区 80 [1]"} {
		got := FilterCharts("正文\n\n"+validChart, missing, 1)
		if strings.Contains(got, "zeus-chart") || !strings.Contains(got, "正文") {
			t.Fatalf("unsupported chart retained or prose lost: %s", got)
		}
	}
}
func TestChartRenderEscapesAndKeepsSourceAndTable(t *testing.T) {
	doc := strings.Replace(validChart, `"渠道"`, `"<img src=x onerror=alert(1)>"`, 1)
	result, err := Render(Data{Title: "报告", Markdown: "## 数据\n\n" + doc, Sources: []Source{{Index: 1, Title: "source", URL: "https://example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(result)
	for _, want := range []string{`class="report-chart"`, `&lt;img src=x onerror=alert(1)&gt;`, `href="#src-1"`, `<details>`, `25`, `80`} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	if strings.Contains(got, "<img src=x onerror") || strings.Contains(got, "zeus-chart") {
		t.Fatal("untrusted chart markup or JSON leaked")
	}
}
func TestLineChartAndInvalidBlocks(t *testing.T) {
	line := strings.Replace(validChart, `"type":"bar"`, `"type":"line"`, 1)
	out, err := Render(Data{Markdown: line, Sources: []Source{{Index: 1}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), `<polyline`) {
		t.Fatal("missing static line chart")
	}
	out, err = Render(Data{Markdown: validChart})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), `class="report-chart"`) || strings.Contains(string(out), "zeus-chart") {
		t.Fatal("chart with missing source was displayed")
	}
}
func TestCitationNumberCannotSupplyValue(t *testing.T) {
	c := strings.Replace(validChart, `"value":25`, `"value":1`, 1)
	if strings.Contains(FilterCharts(c, "2025 搜索 社区 80% [1]", 1), "zeus-chart") {
		t.Fatal("citation number was treated as evidence")
	}
}

func TestWithoutChartsDoesNotCountEncodedChartData(t *testing.T) {
	got := WithoutCharts("正文文字\n\n" + validChart + "\n尾注")
	if strings.Contains(got, "zeus-chart") || strings.Contains(got, "points") || !strings.Contains(got, "正文文字") || !strings.Contains(got, "尾注") {
		t.Fatalf("unexpected readable text: %s", got)
	}
}
