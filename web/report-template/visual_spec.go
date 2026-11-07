package reporttemplate

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode/utf8"
)

// ErrInvalidVisualSpec is returned by ValidateVisualSpec and
// ValidateVisualSpecs so callers can distinguish contract failures from render
// failures with errors.Is.
var ErrInvalidVisualSpec = errors.New("invalid visual spec")

// Point is one plotted value. Label is the x-axis/category label; Value is the
// already-resolved number (not a percentage or a citation number).
type Point struct {
	Label string  `json:"label"`
	Value float64 `json:"value"`
	X     float64 `json:"x,omitempty"`
	Y     float64 `json:"y,omitempty"`
	Sub   string  `json:"sub,omitempty"`
	Note  string  `json:"note,omitempty"`
}

// Series is one line or one bar group. A bar/line chart usually has one
// series; barGrouped and multi-line charts use several.
type Series struct {
	Name   string  `json:"name"`
	Points []Point `json:"points"`
}

// VisualSpec is the P1 contract between the agent and the renderer. Level is
// the data provenance level; the renderer derives the visible badge from it.
type VisualSpec struct {
	Anchor           string   `json:"anchor"`
	Type             string   `json:"type"`
	Title            string   `json:"title"`
	Unit             string   `json:"unit"`
	Level            string   `json:"level"`
	Series           []Series `json:"series"`
	Formula          string   `json:"formula,omitempty"`
	Inputs           []string `json:"inputs,omitempty"`
	Assumptions      []string `json:"assumptions,omitempty"`
	MetricDefinition string   `json:"metric_definition"`
	Caption          string   `json:"caption,omitempty"`
	// AxisTruncated declares that the y-axis intentionally does not start at
	// zero. The renderer must draw a visible break marker; a bare boolean in
	// the JSON is not enough for a reader.
	AxisTruncated bool `json:"axis_truncated,omitempty"`
	// HighlightIndex optionally names one category bar to emphasize. Time axes
	// ignore it and always emphasize the latest point.
	HighlightIndex *int `json:"highlight_index,omitempty"`
	// Order is an optional rendering hint; only "as-is" is recognized and
	// suppresses the default category-axis descending sort.
	Order string `json:"order,omitempty"`
	// New chart metadata. hbar can set label_width; donut uses center_big /
	// center_small; quadrant requires both axis names and can set four corners.
	LabelWidth  int      `json:"label_width,omitempty"`
	CenterBig   string   `json:"center_big,omitempty"`
	CenterSmall string   `json:"center_small,omitempty"`
	XLabel      string   `json:"x_label,omitempty"`
	YLabel      string   `json:"y_label,omitempty"`
	Corners     []string `json:"corners,omitempty"`
}

var visualReferencePattern = regexp.MustCompile(`\[[0-9]+(?:\s*,\s*[0-9]+)*\]`)

// ValidateVisualSpec checks one spec. The rules are deliberately explicit:
// L2 must be reproducible, L3 must declare its assumptions and metric, and an
// L3 cannot be anchored in a current-status/fact chapter.
func ValidateVisualSpec(s VisualSpec) error {
	switch s.Level {
	case "L1", "L2", "L3":
	default:
		return visualSpecError("level must be L1, L2 or L3")
	}
	switch s.Type {
	case "bar", "line", "barGrouped", "hbar", "donut", "quadrant":
	default:
		return visualSpecError("type must be bar, line, barGrouped, hbar, donut or quadrant")
	}
	if strings.TrimSpace(s.Anchor) == "" {
		return visualSpecError("anchor is required")
	}
	if strings.TrimSpace(s.Title) == "" {
		return visualSpecError("title is required")
	}
	if strings.TrimSpace(s.Unit) == "" {
		return visualSpecError("unit is required")
	}
	if strings.TrimSpace(s.MetricDefinition) == "" {
		return visualSpecError("metric_definition is required for all levels")
	}
	if len(s.Series) == 0 {
		return visualSpecError("at least one series is required")
	}
	for i, series := range s.Series {
		if len(series.Points) < 2 {
			return visualSpecError("series %d must contain at least two points", i+1)
		}
		for j, point := range series.Points {
			if strings.TrimSpace(point.Label) == "" {
				return visualSpecError("series %d point %d has an empty label", i+1, j+1)
			}
			if math.IsNaN(point.Value) || math.IsInf(point.Value, 0) {
				return visualSpecError("series %d point %d has a non-finite value", i+1, j+1)
			}
		}
	}
	switch s.Type {
	case "hbar":
		if len(s.Series) != 1 {
			return visualSpecError("hbar 只允许单序列")
		}
		points := s.Series[0].Points
		if len(points) < 2 || len(points) > 10 {
			return visualSpecError("hbar 需要 2–10 个点")
		}
		for i, p := range points {
			if utf8.RuneCountInString(strings.TrimSpace(p.Label)) > 40 {
				return visualSpecError("hbar 第 %d 个标签超过 40 字", i+1)
			}
			if i > 0 && points[i-1].Value < p.Value {
				return visualSpecError("hbar 必须按 value 降序排列")
			}
		}
	case "donut":
		if len(s.Series) != 1 {
			return visualSpecError("donut 只允许单序列")
		}
		points := s.Series[0].Points
		if len(points) < 2 || len(points) > 5 {
			return visualSpecError("donut 需要 2–5 个数据段")
		}
		for i, p := range points {
			if p.Value <= 0 {
				return visualSpecError("donut 第 %d 个值必须大于 0", i+1)
			}
		}
	case "quadrant":
		if len(s.Series) != 1 {
			return visualSpecError("quadrant 只允许单序列")
		}
		points := s.Series[0].Points
		if len(points) < 4 || len(points) > 8 {
			return visualSpecError("quadrant 需要 4–8 个点")
		}
		if strings.TrimSpace(s.XLabel) == "" || strings.TrimSpace(s.YLabel) == "" {
			return visualSpecError("quadrant 必须提供 x_label 和 y_label 两条轴名")
		}
		for i, p := range points {
			if utf8.RuneCountInString(strings.TrimSpace(p.Label)) > 12 {
				return visualSpecError("quadrant 第 %d 个标签超过 12 字", i+1)
			}
			if math.IsNaN(p.X) || math.IsInf(p.X, 0) || math.IsNaN(p.Y) || math.IsInf(p.Y, 0) || p.X < 0 || p.X > 1 || p.Y < 0 || p.Y > 1 {
				return visualSpecError("quadrant 第 %d 个点的 x/y 必须在 0–1 之间", i+1)
			}
		}
	}

	// Formula/Inputs are the reproducible part of L2 and L3. The P1 contract
	// marks both as required for the two derived levels.
	if s.Level == "L2" || s.Level == "L3" {
		if strings.TrimSpace(s.Formula) == "" {
			return visualSpecError("%s requires formula", s.Level)
		}
		if !hasNonEmpty(s.Inputs) {
			return visualSpecError("%s requires at least one non-empty input", s.Level)
		}
	}
	if s.Level == "L3" {
		if countNonEmpty(s.Assumptions) < 2 {
			return visualSpecError("L3 requires at least two non-empty assumptions")
		}
		if !visualTrendAnchor(s.Anchor) {
			return visualSpecError("L3 anchor must be in a trend/outlook/scenario/forecast chapter")
		}
	}
	return nil
}

// ValidateVisualSpecs validates the whole block. The count limit is a
// deliberately small, testable rule rather than an implicit renderer behavior.
func ValidateVisualSpecs(specs []VisualSpec) error {
	if len(specs) < 1 || len(specs) > 6 {
		return visualSpecError("visual block must contain between 1 and 6 charts")
	}
	for i, spec := range specs {
		if err := ValidateVisualSpec(spec); err != nil {
			return fmt.Errorf("visual spec %d: %w", i+1, err)
		}
	}
	return nil
}

func visualSpecError(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrInvalidVisualSpec, fmt.Sprintf(format, args...))
}

func hasNonEmpty(values []string) bool {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return true
		}
	}
	return false
}

func countNonEmpty(values []string) int {
	count := 0
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			count++
		}
	}
	return count
}

var visualTrendKeywords = []string{
	"趋势", "展望", "研判", "情景", "预测",
	"trend", "outlook", "forecast", "scenario", "projection", "prospect",
}

var visualFactKeywords = []string{
	"现状", "数据", "事实", "回顾", "背景",
	"current status", "facts", "data", "background",
}

// visualTrendAnchor is intentionally conservative: a fact chapter that also
// contains "趋势" (for example "现状与趋势") is still rejected. The negative
// list runs first so an L3 cannot hide in a current-status section.
func visualTrendAnchor(anchor string) bool {
	lower := strings.ToLower(anchor)
	for _, word := range visualFactKeywords {
		if strings.Contains(lower, word) {
			return false
		}
	}
	for _, word := range visualTrendKeywords {
		if strings.Contains(lower, word) {
			return true
		}
	}
	return false
}

// parseVisualSpec distinguishes a P1 VisualSpec block from the legacy Chart
// block by the presence of the top-level "level" field. A block with a level
// but invalid payload still returns true, so the caller does not accidentally
// fall back to the legacy parser.
func parseVisualSpec(raw string) (VisualSpec, bool) {
	var probe struct {
		Level string `json:"level"`
	}
	if json.Unmarshal([]byte(raw), &probe) != nil || strings.TrimSpace(probe.Level) == "" {
		return VisualSpec{}, false
	}
	var spec VisualSpec
	if err := json.Unmarshal([]byte(raw), &spec); err != nil {
		return VisualSpec{}, true
	}
	return spec, true
}

// visualTick is one y-axis grid line. The formatted label is kept beside the
// value so the gutter can be measured before any SVG is written.

// RenderVisualSpec is retained only for the workflow's schema gate. The page
// renderer no longer produces server-side SVG; charts are generated by the
// template's chart*() library in the browser.
func RenderVisualSpec(spec VisualSpec) (string, error) {
	return "", ValidateVisualSpec(spec)
}
