package reporttemplate

import (
	"errors"
	"html"
	"math"
	"regexp"
	"strconv"
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

func TestRenderVisualSpecBarL1(t *testing.T) {
	out, err := RenderVisualSpec(visualTestSpec("L1"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`class="report-visual"`, `data-level="L1"`, `viewBox="0 0 360 240"`, "测试图：三情景", "单位：万人", "<rect"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "L2 ·") || strings.Contains(out, "L3 ·") {
		t.Fatal("L1 must not have an L2/L3 badge")
	}
	if strings.Contains(out, `class="axis-truncated"`) {
		t.Fatal("untruncated axis must not have a marker")
	}
}

func TestRenderVisualSpecBarGroupedL2(t *testing.T) {
	spec := visualTestSpec("L2")
	spec.Type = "barGrouped"
	spec.Series = []Series{
		{Name: "基准", Points: []Point{{Label: "2024", Value: 100}, {Label: "2025", Value: 200}}},
		{Name: "乐观", Points: []Point{{Label: "2024", Value: 120}, {Label: "2025", Value: 240}}},
	}
	out, err := RenderVisualSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`viewBox="0 0 360 240"`, "L2 · 据 [1][3] 计算", "基准", "乐观", "<rect"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
}

func TestRenderVisualSpecLineL3AndTruncatedAxis(t *testing.T) {
	spec := visualTestSpec("L3")
	spec.Type = "line"
	spec.AxisTruncated = true
	spec.Series = []Series{
		{Name: "基准", Points: []Point{{Label: "2024", Value: 800}, {Label: "2025", Value: 1000}, {Label: "2026", Value: 1200}}},
		{Name: "乐观", Points: []Point{{Label: "2024", Value: 800}, {Label: "2025", Value: 1100}, {Label: "2026", Value: 1400}}},
	}
	out, err := RenderVisualSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`viewBox="0 0 360 300"`, "L3 · 分析性判断，非官方数据", "<polyline", `class="axis-truncated"`, "轴不从 0 起"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Count(out, "轴不从 0 起") != 1 {
		t.Fatalf("axis marker must be rendered once, got %d", strings.Count(out, "轴不从 0 起"))
	}
}

func TestRenderVisualSpecRejectsInvalid(t *testing.T) {
	spec := visualTestSpec("L2")
	spec.Inputs = nil
	if _, err := RenderVisualSpec(spec); !errors.Is(err, ErrInvalidVisualSpec) {
		t.Fatalf("expected validation error, got %v", err)
	}
}

func TestRenderVisualSpecEscapesCaptionAndTitle(t *testing.T) {
	spec := visualTestSpec("L1")
	spec.Title = `<script>alert(1)</script>`
	spec.Caption = `<img src=x onerror=alert(1)>`
	out, err := RenderVisualSpec(spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "<img src=x") {
		t.Fatal("untrusted text leaked into rendered figure")
	}
	for _, want := range []string{"&lt;script&gt;", "&lt;img src=x"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing escaped %q", want)
		}
	}
}

var visualTextTag = regexp.MustCompile(`(?s)<text([^>]*)>(.*?)</text>`)
var visualAttribute = regexp.MustCompile(`([a-zA-Z0-9-]+)="([^"]*)"`)
var visualInnerTag = regexp.MustCompile(`<[^>]+>`)

func TestRenderVisualSpecNoHorizontalOverflow(t *testing.T) {
	line := visualTestSpec("L3")
	line.Type = "line"
	line.AxisTruncated = true
	line.Title = "三情景常态化参与人口：基准 / 乐观 / 悲观（2026–2030）"
	line.Series = []Series{
		{Name: "基准", Points: []Point{{Label: "2026", Value: 800}, {Label: "2027", Value: 1010}, {Label: "2028", Value: 1260}, {Label: "2029", Value: 1590}, {Label: "2030", Value: 2000}}},
		{Name: "乐观", Points: []Point{{Label: "2026", Value: 800}, {Label: "2027", Value: 1110}, {Label: "2028", Value: 1550}, {Label: "2029", Value: 2160}, {Label: "2030", Value: 3000}}},
		{Name: "悲观", Points: []Point{{Label: "2026", Value: 800}, {Label: "2027", Value: 870}, {Label: "2028", Value: 940}, {Label: "2029", Value: 1020}, {Label: "2030", Value: 1100}}},
	}
	grouped := visualTestSpec("L2")
	grouped.Type = "barGrouped"
	grouped.Series = []Series{
		{Name: "基准", Points: []Point{{Label: "2024", Value: 100}, {Label: "2025", Value: 200}}},
		{Name: "乐观", Points: []Point{{Label: "2024", Value: 120}, {Label: "2025", Value: 240}}},
	}
	for name, spec := range map[string]VisualSpec{"bar": visualTestSpec("L1"), "grouped": grouped, "line": line} {
		t.Run(name, func(t *testing.T) {
			out, err := RenderVisualSpec(spec)
			if err != nil {
				t.Fatal(err)
			}
			assertVisualTextWithinViewBox(t, out, 360)
		})
	}
}

func assertVisualTextWithinViewBox(t *testing.T, out string, width float64) {
	t.Helper()
	for _, match := range visualTextTag.FindAllStringSubmatch(out, -1) {
		attrs := map[string]string{}
		for _, attr := range visualAttribute.FindAllStringSubmatch(match[1], -1) {
			attrs[attr[1]] = attr[2]
		}
		x, _ := strconv.ParseFloat(attrs["x"], 64)
		fontSize, _ := strconv.ParseFloat(attrs["font-size"], 64)
		if fontSize == 0 {
			fontSize = visualFont
		}
		text := html.UnescapeString(strings.TrimSpace(visualInnerTag.ReplaceAllString(match[2], "")))
		estimated := estimateVisualTextWidth(text, fontSize)
		left, right := x, x+estimated
		switch attrs["text-anchor"] {
		case "middle":
			left, right = x-estimated/2, x+estimated/2
		case "end":
			left, right = x-estimated, x
		}
		if left < -0.5 || right > width+0.5 {
			t.Errorf("text %q spans %.1f..%.1f outside viewBox 0..%.0f", text, left, right, width)
		}
	}
}
