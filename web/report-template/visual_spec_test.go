package reporttemplate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"testing"
)

func visualTestSpec(level string) VisualSpec {
	spec := VisualSpec{
		Anchor:           "五、未来趋势研判（2026-2030）",
		Type:             "bar",
		Title:            "测试图：三情景",
		Unit:             "万人",
		Level:            level,
		Series:           []Series{{Name: "基准", Points: []Point{{Label: "2024", Value: 100}, {Label: "2025", Value: 200}}}},
		MetricDefinition: "测试指标：2024 到 2025 的数值",
		Caption:          "测试说明",
	}
	switch level {
	case "L2":
		spec.Formula = "200 / 100 = 200%"
		spec.Inputs = []string{"[1] 100 万人", "[3] 200 万人"}
	case "L3":
		spec.Formula = "CAGR = (200 / 100)^(1/1) - 1"
		spec.Inputs = []string{"[1] 100 万人", "[3] 200 万人"}
		spec.Assumptions = []string{"2024 起点为 100 万。", "2025 终点为 200 万。"}
	}
	return spec
}

func TestValidateVisualSpecLevelRules(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*VisualSpec)
		wantErr bool
	}{
		{"valid L1", func(*VisualSpec) {}, false},
		{"valid L2", func(s *VisualSpec) { *s = visualTestSpec("L2") }, false},
		{"valid L3", func(s *VisualSpec) { *s = visualTestSpec("L3") }, false},
		{"bad level", func(s *VisualSpec) { s.Level = "L4" }, true},
		{"bad type", func(s *VisualSpec) { s.Type = "pie" }, true},
		{"missing anchor", func(s *VisualSpec) { s.Anchor = "" }, true},
		{"missing title", func(s *VisualSpec) { s.Title = "" }, true},
		{"missing unit", func(s *VisualSpec) { s.Unit = "" }, true},
		{"missing metric for L1", func(s *VisualSpec) { s.MetricDefinition = "" }, true},
		{"L2 missing formula", func(s *VisualSpec) { *s = visualTestSpec("L2"); s.Formula = "" }, true},
		{"L2 missing inputs", func(s *VisualSpec) { *s = visualTestSpec("L2"); s.Inputs = nil }, true},
		{"L2 blank inputs", func(s *VisualSpec) { *s = visualTestSpec("L2"); s.Inputs = []string{" "} }, true},
		{"L3 one assumption", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.Assumptions = []string{"只有一条"} }, true},
		{"L3 missing metric", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.MetricDefinition = "" }, true},
		{"L3 missing formula", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.Formula = "" }, true},
		{"L3 missing inputs", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.Inputs = nil }, true},
		{"L3 fact anchor", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.Anchor = "一、现状与数据" }, true},
		{"L3 mixed fact and trend anchor", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.Anchor = "现状与趋势" }, true},
		{"L3 trend anchor", func(s *VisualSpec) { *s = visualTestSpec("L3"); s.Anchor = "未来展望与情景" }, false},
		{"empty series", func(s *VisualSpec) { s.Series = nil }, true},
		{"series with one point", func(s *VisualSpec) { s.Series[0].Points = s.Series[0].Points[:1] }, true},
		{"empty point label", func(s *VisualSpec) { s.Series[0].Points[0].Label = "" }, true},
		{"non-finite value", func(s *VisualSpec) { s.Series[0].Points[0].Value = math.Inf(1) }, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := visualTestSpec("L1")
			tt.mutate(&spec)
			err := ValidateVisualSpec(spec)
			if tt.wantErr && !errors.Is(err, ErrInvalidVisualSpec) {
				t.Fatalf("expected ErrInvalidVisualSpec, got %v", err)
			}
			if !tt.wantErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func TestValidateVisualSpecsCount(t *testing.T) {
	if err := ValidateVisualSpecs(nil); !errors.Is(err, ErrInvalidVisualSpec) {
		t.Fatalf("empty block should fail: %v", err)
	}
	one := visualTestSpec("L1")
	if err := ValidateVisualSpecs([]VisualSpec{one}); err != nil {
		t.Fatalf("one chart should pass: %v", err)
	}
	six := make([]VisualSpec, 6)
	for i := range six {
		six[i] = visualTestSpec("L1")
	}
	if err := ValidateVisualSpecs(six); err != nil {
		t.Fatalf("six charts should pass: %v", err)
	}
	seven := make([]VisualSpec, 7)
	for i := range seven {
		seven[i] = visualTestSpec("L1")
	}
	if err := ValidateVisualSpecs(seven); !errors.Is(err, ErrInvalidVisualSpec) {
		t.Fatalf("seven charts should fail: %v", err)
	}
	invalid := visualTestSpec("L2")
	invalid.Formula = ""
	if err := ValidateVisualSpecs([]VisualSpec{one, invalid}); !errors.Is(err, ErrInvalidVisualSpec) {
		t.Fatalf("invalid member should fail: %v", err)
	}
}

func TestVisualTrendAnchorKeywords(t *testing.T) {
	for _, anchor := range []string{"五、未来趋势研判", "未来展望", "三情景预测", "Outlook 2030", "Scenario planning"} {
		if !visualTrendAnchor(anchor) {
			t.Errorf("expected trend anchor: %s", anchor)
		}
	}
	for _, anchor := range []string{"一、现状与数据", "现状与趋势", "二、市场规模", "Current status and forecast", "Facts and data"} {
		if visualTrendAnchor(anchor) {
			t.Errorf("expected non-trend anchor: %s", anchor)
		}
	}
}

func TestFilterChartsPreservesValidVisualSpec(t *testing.T) {
	spec := visualTestSpec("L1")
	raw, err := json.Marshal(spec)
	if err != nil {
		t.Fatal(err)
	}
	block := "```zeus-chart\n" + string(raw) + "\n```\n"
	got := FilterCharts(block, "2024 100 2025 200 [1]", 1)
	if !strings.Contains(got, "zeus-chart") {
		t.Fatal("valid visual spec must survive the legacy filter")
	}
	invalid := visualTestSpec("L2")
	invalid.Formula = ""
	raw, _ = json.Marshal(invalid)
	badBlock := "```zeus-chart\n" + string(raw) + "\n```\n"
	if got := FilterCharts(badBlock, "2024 100 2025 200 [1]", 1); strings.Contains(got, "zeus-chart") {
		t.Fatal("invalid visual spec must not survive the filter")
	}
}

func extremeVisualSpec(typ string, values ...float64) VisualSpec {
	points := make([]Point, 0, len(values))
	for i, value := range values {
		points = append(points, Point{Label: fmt.Sprintf("P%d", i+1), Value: value})
	}
	return VisualSpec{
		Anchor:           "一、规模",
		Type:             typ,
		Title:            "极端数值图",
		Unit:             "亿次/日",
		Level:            "L1",
		Series:           []Series{{Name: "数值", Points: points}},
		MetricDefinition: "极端数值测试",
	}
}
