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

func TestNewTrendRequiresComparableOrderedDates(t *testing.T) {
	chart := "```zeus-chart\n{\"type\":\"line\",\"title\":\"销量\",\"unit\":\"辆\",\"period\":\"2023–2025\",\"scope\":\"中国\",\"points\":[{\"label\":\"2023\",\"value\":10,\"source\":1},{\"label\":\"2025\",\"value\":20,\"source\":1}]}\n```\n"
	evidence := "中国销量 2023–2025：2023 年 10 辆，2025 年 20 辆 [1]"
	if !strings.Contains(FilterCharts(chart, evidence, 1), "zeus-chart") {
		t.Fatal("supported trend removed")
	}
	for _, bad := range []string{strings.Replace(chart, `"scope":"中国"`, `"scope":""`, 1), strings.Replace(chart, `"label":"2025"`, `"label":"2022"`, 1), strings.Replace(chart, `"label":"2025"`, `"label":"2025-01"`, 1), strings.Replace(chart, `"source":1}]`, `"source":2}]`, 1)} {
		if strings.Contains(FilterCharts(bad, evidence, 2), "zeus-chart") {
			t.Fatal("unsupported trend retained")
		}
	}
}
func TestForecastMustBeMarkedAndAllEvidenceRetained(t *testing.T) {
	evidence := "2025 中国预测 搜索 25% 社区 80% [1]"
	if strings.Contains(FilterCharts(validChart, evidence, 1), "zeus-chart") {
		t.Fatal("unmarked forecast retained")
	}
	qualified := strings.Replace(validChart, `"period":"2025"`, `"period":"2025","scope":"中国","qualifier":"预测"`, 1)
	if !strings.Contains(FilterCharts(qualified, evidence, 1), "zeus-chart") {
		t.Fatal("marked forecast rejected")
	}
	if strings.Contains(FilterCharts(qualified, evidence+"（实际与预测混合）", 1), "zeus-chart") {
		t.Fatal("mixed actual and forecast data must not be charted")
	}
	many := strings.Repeat(qualified+"\n", 5)
	if strings.Count(FilterCharts(many, evidence, 1), "zeus-chart") != 5 {
		t.Fatal("valid charts should not have a fixed count limit")
	}
}
func TestDotAndPrintDataSources(t *testing.T) {
	dot := strings.Replace(validChart, `"type":"bar"`, `"type":"dot"`, 1)
	out, err := Render(Data{Markdown: dot, Sources: []Source{{Index: 1, Title: "A", URL: "https://example.com"}}})
	if err != nil {
		t.Fatal(err)
	}
	got := string(out)
	for _, part := range []string{`chart-dot-track`, `chart-point-source`, `chart-print-table`, `href="#src-1"`, `统计范围未标注`} {
		if !strings.Contains(got, part) {
			t.Errorf("missing %s", part)
		}
	}
}
func TestUnevenTrendUsesActualTimeSpacing(t *testing.T) {
	c := Chart{Type: "line", Title: "X", Unit: "份", Period: "2020–2024", Points: []ChartPoint{{Label: "2020", Value: "1", Source: 1}, {Label: "2021", Value: "2", Source: 1}, {Label: "2024", Value: "3", Source: 1}}}
	got := renderChart(c, "zh")
	if strings.Contains(got, "160.00,") || !strings.Contains(got, "90.14,") {
		t.Fatalf("time spacing not proportional: %s", got)
	}
}

func TestWorldBankGroupedPopulationNumbers(t *testing.T) {
	// Public WDI population table, 2022, persons in thousands:
	// https://databankfiles.worldbank.org/public/ddpext_download/POP.pdf
	chart := "```zeus-chart\n{\"type\":\"bar\",\"title\":\"Population\",\"unit\":\"thousands\",\"period\":\"2022\",\"points\":[{\"label\":\"India\",\"value\":1417173,\"source\":1},{\"label\":\"China\",\"value\":1412175,\"source\":1}]}\n```\n"
	evidence := "Population 2022 (thousands): India 1,417,173; China 1,412,175 [1]"
	if !strings.Contains(FilterCharts(chart, evidence, 1), "zeus-chart") {
		t.Fatal("properly grouped source values should be accepted")
	}
	for _, bad := range []string{strings.Replace(evidence, "1,417,173", "-1,417,173", 1), strings.Replace(evidence, "1,417,173", "− 1,417,173", 1), strings.Replace(evidence, "1,417,173", "11,417,173", 1), strings.Replace(evidence, "1,417,173", "1,417,1734", 1)} {
		if strings.Contains(FilterCharts(chart, bad, 1), "zeus-chart") {
			t.Fatalf("invalid or negative numeric evidence accepted: %s", bad)
		}
	}
}

func TestFilterChartsReportsRejectionCategories(t *testing.T) {
	filtered, reasons := FilterChartsWithReasons(validChart, "2025 搜索 25% [1]", 1)
	if strings.Contains(filtered, "zeus-chart") || reasons["missing_evidence"] != 1 {
		t.Fatalf("unexpected filter result: %v", reasons)
	}
	invalid := strings.Replace(validChart, `"type":"bar"`, `"type":"invalid"`, 1)
	_, reasons = FilterChartsWithReasons(invalid, "2025 搜索 25% 社区 80% [1]", 1)
	if reasons["invalid_schema"] != 1 {
		t.Fatalf("unexpected schema result: %v", reasons)
	}
}

func TestChartRejectsSwappedCategoryValues(t *testing.T) {
	evidence := "2025 搜索 25% 社区 80% [1]"
	swapped := strings.Replace(validChart, `"value":25`, `"value":80`, 1)
	swapped = strings.Replace(swapped, `"value":80,"source":1}]`, `"value":25,"source":1}]`, 1)
	if strings.Contains(FilterCharts(swapped, evidence, 1), "zeus-chart") {
		t.Fatal("swapped values must not be associated with the wrong labels")
	}
	if !strings.Contains(FilterCharts(validChart, evidence, 1), "zeus-chart") {
		t.Fatal("correctly associated values should pass")
	}
}

func TestAnnualSourcePassagesUsePointYearsNotInventedRange(t *testing.T) {
	// ONS Great Britain mid-year population estimates, revised 2026:
	// https://www.ons.gov.uk/peoplepopulationandcommunity/populationandmigration/populationestimates/timeseries/gbpop/pop
	evidence := "Great Britain population estimate, people: 2022 65,769,400 [1]\n\nGreat Britain population estimate, people: 2023 66,667,700 [1]\n\nGreat Britain population estimate, people: 2024 67,328,400 [1]"
	chart := "```zeus-chart\n{\"type\":\"line\",\"title\":\"population\",\"unit\":\"people\",\"period\":\"2022—2024\",\"scope\":\"Great Britain\",\"qualifier\":\"estimate\",\"points\":[{\"label\":\"2022\",\"value\":65769400,\"source\":1},{\"label\":\"2023\",\"value\":66667700,\"source\":1},{\"label\":\"2024\",\"value\":67328400,\"source\":1}]}\n```\n"
	if !strings.Contains(FilterCharts(chart, evidence, 1), "zeus-chart") {
		t.Fatal("separate annual cited evidence should support a derived date range")
	}
	wrong := strings.Replace(chart, `"period":"2022—2024"`, `"period":"2021—2024"`, 1)
	if strings.Contains(FilterCharts(wrong, evidence, 1), "zeus-chart") {
		t.Fatal("range must match the verified endpoint labels")
	}
	mismatch := strings.Replace(chart, `"value":66667700`, `"value":67328400`, 1)
	if strings.Contains(FilterCharts(mismatch, evidence, 1), "zeus-chart") {
		t.Fatal("a different year's value must not pass")
	}
	multi := strings.Replace(chart, `"type":"line"`, `"type":"bar"`, 1)
	multi = strings.Replace(multi, `"label":"2023","value":66667700,"source":1`, `"label":"2023","value":66667700,"source":2`, 1)
	multi = strings.Replace(multi, `"label":"2024","value":67328400,"source":1`, `"label":"2024","value":67328400,"source":3`, 1)
	multiEvidence := strings.Replace(evidence, "2023 66,667,700 [1]", "2023 66,667,700 [2]", 1)
	multiEvidence = strings.Replace(multiEvidence, "2024 67,328,400 [1]", "2024 67,328,400 [3]", 1)
	if !strings.Contains(FilterCharts(multi, multiEvidence, 3), "zeus-chart") {
		t.Fatal("bar chart should allow separately cited years")
	}
	if strings.Contains(FilterCharts(strings.Replace(multi, `"type":"bar"`, `"type":"line"`, 1), multiEvidence, 3), "zeus-chart") {
		t.Fatal("line chart must retain single-source rule")
	}
}
