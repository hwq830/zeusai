package reporttemplate

import (
	"encoding/json"
	"strings"
	"testing"
)

func newTypeSpec(typ string) VisualSpec {
	return VisualSpec{Anchor: "一、市场", Type: typ, Title: "测试图", Unit: "万", Level: "L1", MetricDefinition: "测试口径", Caption: "来源：测试；口径：测试；时间：2024"}
}

func TestNewChartTypesPrepareAndCaption(t *testing.T) {
	hbar := newTypeSpec("hbar")
	hbar.Series = []Series{{Name: "A", Points: []Point{{Label: "豆包", Value: 500}, {Label: "Kimi", Value: 300}, {Label: "文小言", Value: 200}}}}
	hbar.LabelWidth = 180

	donut := newTypeSpec("donut")
	donut.Series = []Series{{Name: "A", Points: []Point{{Label: "甲", Value: 60}, {Label: "乙", Value: 40}}}}
	donut.CenterBig = "100"
	donut.CenterSmall = "合计"

	quadrant := newTypeSpec("quadrant")
	quadrant.XLabel = "规模"
	quadrant.YLabel = "增速"
	quadrant.Series = []Series{{Name: "A", Points: []Point{
		{Label: "A", X: 0.2, Y: 0.8}, {Label: "B", X: 0.7, Y: 0.6},
		{Label: "C", X: 0.3, Y: 0.2}, {Label: "D", X: 0.9, Y: 0.1},
	}}}
	quadrant.Corners = []string{"高规模高增速", "低规模高增速", "高规模低增速", "低规模低增速"}

	for _, spec := range []VisualSpec{hbar, donut, quadrant} {
		html, items := renderRuleChart(t, spec)
		if len(items) != 1 {
			t.Fatalf("%s chart item missing", spec.Type)
		}
		if items[0].RenderType != spec.Type {
			t.Fatalf("%s renderType=%s", spec.Type, items[0].RenderType)
		}
		if !strings.Contains(html, `<figcaption>`) || !strings.Contains(html, "来源：测试") {
			t.Fatalf("%s missing figcaption/source", spec.Type)
		}
	}
}

func TestNewChartTypeConfigs(t *testing.T) {
	hbar := newTypeSpec("hbar")
	hbar.Series = []Series{{Name: "A", Points: []Point{{Label: "大", Value: 10, Note: "10万"}, {Label: "小", Value: 5}}}}
	hbar.LabelWidth = 160
	_, items := renderRuleChart(t, hbar)
	raw, _ := json.Marshal(items[0].Config)
	if !strings.Contains(string(raw), `"labelWidth":160`) || !strings.Contains(string(raw), `"note":"10万"`) {
		t.Fatalf("hbar config=%s", raw)
	}

	donut := newTypeSpec("donut")
	donut.Series = []Series{{Name: "A", Points: []Point{{Label: "甲", Value: 60}, {Label: "乙", Value: 40}}}}
	donut.CenterBig = "100"
	donut.CenterSmall = "合计"
	_, items = renderRuleChart(t, donut)
	raw, _ = json.Marshal(items[0].Config)
	if !strings.Contains(string(raw), `"center":{"big":"100","small":"合计"}`) {
		t.Fatalf("donut config=%s", raw)
	}
	if !strings.Contains(string(raw), `"color":"var(--deep2)"`) {
		t.Fatalf("donut must assign segment colors: %s", raw)
	}

	quadrant := newTypeSpec("quadrant")
	quadrant.XLabel = "规模"
	quadrant.YLabel = "增速"
	quadrant.Series = []Series{{Name: "A", Points: []Point{
		{Label: "A", X: 0.1, Y: 0.9, Sub: "领导者"}, {Label: "B", X: 0.8, Y: 0.7},
		{Label: "C", X: 0.2, Y: 0.2}, {Label: "D", X: 0.9, Y: 0.1},
	}}}
	quadrant.Corners = []string{"TL", "TR", "BL", "BR"}
	_, items = renderRuleChart(t, quadrant)
	raw, _ = json.Marshal(items[0].Config)
	if !strings.Contains(string(raw), `"xLabel":"规模"`) || !strings.Contains(string(raw), `"yLabel":"增速"`) || !strings.Contains(string(raw), `"sub":"领导者"`) {
		t.Fatalf("quadrant config=%s", raw)
	}
}

func TestNewChartTypesValidationRejects(t *testing.T) {
	hbar := newTypeSpec("hbar")
	hbar.Series = []Series{{Name: "A", Points: []Point{{Label: "小", Value: 1}, {Label: "大", Value: 2}}}}
	if err := ValidateVisualSpec(hbar); err == nil || !strings.Contains(err.Error(), "hbar 必须按 value 降序排列") {
		t.Fatalf("hbar unordered error=%v", err)
	}
	hbar.Series[0].Points = []Point{{Label: "a", Value: 11}, {Label: "b", Value: 10}, {Label: "c", Value: 9}, {Label: "d", Value: 8}, {Label: "e", Value: 7}, {Label: "f", Value: 6}, {Label: "g", Value: 5}, {Label: "h", Value: 4}, {Label: "i", Value: 3}, {Label: "j", Value: 2}, {Label: "k", Value: 1}}
	if err := ValidateVisualSpec(hbar); err == nil || !strings.Contains(err.Error(), "hbar 需要 2–10 个点") {
		t.Fatalf("hbar too many error=%v", err)
	}

	donut := newTypeSpec("donut")
	donut.Series = []Series{{Name: "A", Points: []Point{{Label: "a", Value: 1}, {Label: "b", Value: 2}, {Label: "c", Value: 3}, {Label: "d", Value: 4}, {Label: "e", Value: 5}, {Label: "f", Value: 6}}}}
	if err := ValidateVisualSpec(donut); err == nil || !strings.Contains(err.Error(), "donut 需要 2–5 个数据段") {
		t.Fatalf("donut too many error=%v", err)
	}
	donut.Series[0].Points = []Point{{Label: "a", Value: -1}, {Label: "b", Value: 2}}
	if err := ValidateVisualSpec(donut); err == nil || !strings.Contains(err.Error(), "donut 第 1 个值必须大于 0") {
		t.Fatalf("donut negative error=%v", err)
	}

	quadrant := newTypeSpec("quadrant")
	quadrant.XLabel = "规模"
	quadrant.YLabel = "增速"
	quadrant.Series = []Series{{Name: "A", Points: []Point{
		{Label: "A", X: 1.2, Y: 0.5}, {Label: "B", X: 0.5, Y: 0.5},
		{Label: "C", X: 0.2, Y: 0.2}, {Label: "D", X: 0.8, Y: 0.8},
	}}}
	if err := ValidateVisualSpec(quadrant); err == nil || !strings.Contains(err.Error(), "x/y 必须在 0–1 之间") {
		t.Fatalf("quadrant range error=%v", err)
	}
	quadrant.Series[0].Points[0].X = 0.2
	quadrant.YLabel = ""
	if err := ValidateVisualSpec(quadrant); err == nil || !strings.Contains(err.Error(), "必须提供 x_label 和 y_label") {
		t.Fatalf("quadrant axes error=%v", err)
	}
	quadrant.YLabel = "增速"
	quadrant.Series[0].Points = quadrant.Series[0].Points[:3]
	if err := ValidateVisualSpec(quadrant); err == nil || !strings.Contains(err.Error(), "quadrant 需要 4–8 个点") {
		t.Fatalf("quadrant points error=%v", err)
	}
}

func TestNewChartTypesRejectLongLabelsAndMultipleSeries(t *testing.T) {
	hbar := newTypeSpec("hbar")
	hbar.Series = []Series{{Name: "A", Points: []Point{{Label: strings.Repeat("长", 41), Value: 2}, {Label: "短", Value: 1}}}}
	if err := ValidateVisualSpec(hbar); err == nil || !strings.Contains(err.Error(), "hbar 第 1 个标签超过 40 字") {
		t.Fatalf("hbar label error=%v", err)
	}
	hbar.Series = append(hbar.Series, Series{Name: "B", Points: []Point{{Label: "x", Value: 2}, {Label: "y", Value: 1}}})
	if err := ValidateVisualSpec(hbar); err == nil || !strings.Contains(err.Error(), "hbar 只允许单序列") {
		t.Fatalf("hbar series error=%v", err)
	}

	quadrant := newTypeSpec("quadrant")
	quadrant.XLabel = "x"
	quadrant.YLabel = "y"
	quadrant.Series = []Series{{Name: "A", Points: []Point{
		{Label: strings.Repeat("长", 13), X: 0.1, Y: 0.1}, {Label: "b", X: 0.2, Y: 0.2},
		{Label: "c", X: 0.3, Y: 0.3}, {Label: "d", X: 0.4, Y: 0.4},
	}}}
	if err := ValidateVisualSpec(quadrant); err == nil || !strings.Contains(err.Error(), "quadrant 第 1 个标签超过 12 字") {
		t.Fatalf("quadrant label error=%v", err)
	}
}
