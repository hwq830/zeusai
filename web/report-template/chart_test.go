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
