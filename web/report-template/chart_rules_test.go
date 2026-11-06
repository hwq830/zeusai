package reporttemplate

import (
	"encoding/json"
	"strings"
	"testing"
)

func chartSpecForTest(typ string, labels []string, values []float64) VisualSpec {
	pts := make([]Point, len(labels))
	for i := range labels {
		pts[i] = Point{Label: labels[i], Value: values[i]}
	}
	return VisualSpec{Anchor: "一、市场", Type: typ, Title: "测试", Unit: "万", Level: "L1", Series: []Series{{Name: "A", Points: pts}}, MetricDefinition: "测试口径"}
}

func renderRuleChart(t *testing.T, spec VisualSpec) (string, []chartItem) {
	t.Helper()
	raw, _ := json.Marshal(spec)
	out, err := Render(Data{Title: "规则", Markdown: "## 一、市场\n\n```zeus-chart\n" + string(raw) + "\n```\n"})
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	start := strings.Index(html, `<script type="application/json" id="zeus-chart-data">`)
	if start < 0 {
		t.Fatal("chart data missing")
	}
	start += len(`<script type="application/json" id="zeus-chart-data">`)
	end := strings.Index(html[start:], "</script>")
	var items []chartItem
	if err := json.Unmarshal([]byte(html[start:start+end]), &items); err != nil {
		t.Fatal(err)
	}
	return html, items
}

func chartPoints(t *testing.T, item chartItem) []struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
} {
	t.Helper()
	raw, _ := json.Marshal(item.Config["data"])
	var pts []struct {
		Label string  `json:"label"`
		Value float64 `json:"value"`
	}
	if err := json.Unmarshal(raw, &pts); err != nil {
		t.Fatal(err)
	}
	return pts
}

func TestTimeAxisHighlightsLastOnly(t *testing.T) {
	html, items := renderRuleChart(t, chartSpecForTest("bar", []string{"2020", "2021", "2022", "2023", "2024", "2025"}, []float64{1, 2, 3, 4, 5, 6}))
	if len(items) != 1 {
		t.Fatal("missing chart")
	}
	if got := items[0].Config["highlight"]; got != float64(5) {
		t.Fatalf("highlight = %v, want 5", got)
	}
	if strings.Contains(html, "为预测值") {
		t.Fatal("time axis without forecast must not add prediction caption")
	}
}

func TestForecastHighlightGetsCaption(t *testing.T) {
	html, items := renderRuleChart(t, chartSpecForTest("bar", []string{"2022", "2023", "2024", "2025", "2026E"}, []float64{1, 2, 3, 4, 5}))
	if got := items[0].Config["highlight"]; got != float64(4) {
		t.Fatalf("highlight = %v, want 4", got)
	}
	if !strings.Contains(html, "2026E 为预测值") {
		t.Fatal("forecast label must be added to figcaption")
	}
}

func TestCategoryAxisSortsDescendingWithLabels(t *testing.T) {
	_, items := renderRuleChart(t, chartSpecForTest("bar", []string{"A", "B", "C"}, []float64{300, 1200, 500}))
	pts := chartPoints(t, items[0])
	wantLabels := []string{"B", "C", "A"}
	wantValues := []float64{1200, 500, 300}
	for i := range pts {
		if pts[i].Label != wantLabels[i] || pts[i].Value != wantValues[i] {
			t.Fatalf("point %d = %s/%v, want %s/%v", i, pts[i].Label, pts[i].Value, wantLabels[i], wantValues[i])
		}
	}
	if _, ok := items[0].Config["highlight"]; ok {
		t.Fatal("category axis must not highlight by default")
	}
}

func TestOrderedCategoryKeepsOriginalOrder(t *testing.T) {
	_, items := renderRuleChart(t, chartSpecForTest("bar", []string{"一线", "二线", "三线"}, []float64{300, 100, 200}))
	pts := chartPoints(t, items[0])
	if pts[0].Label != "一线" || pts[1].Label != "二线" || pts[2].Label != "三线" {
		t.Fatalf("ordered categories reordered: %+v", pts)
	}
}

func TestOrderAsIsKeepsOriginalOrder(t *testing.T) {
	spec := chartSpecForTest("bar", []string{"A", "B", "C"}, []float64{300, 1200, 500})
	spec.Order = "as-is"
	_, items := renderRuleChart(t, spec)
	pts := chartPoints(t, items[0])
	if pts[0].Label != "A" || pts[1].Label != "B" || pts[2].Label != "C" {
		t.Fatalf("order=as-is reordered: %+v", pts)
	}
}

func TestExplicitHighlightIndexOnCategory(t *testing.T) {
	spec := chartSpecForTest("bar", []string{"A", "B", "C"}, []float64{300, 1200, 500})
	idx := 1
	spec.HighlightIndex = &idx
	_, items := renderRuleChart(t, spec)
	// Category sorts B/C/A; explicit index 1 (B) remains the highlighted category.
	if got := items[0].Config["highlight"]; got != float64(0) {
		t.Fatalf("highlight = %v, want 0", got)
	}
}

func TestPredictionCaptionRegex(t *testing.T) {
	for _, label := range []string{"2026E", "2026年预计", "预测值", "外推"} {
		if !isPredictionLabel(label) {
			t.Fatalf("%s should be prediction", label)
		}
	}
	if isPredictionLabel("2025") {
		t.Fatal("2025 is not prediction")
	}
}
