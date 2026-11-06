package reporttemplate

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
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
}

const (
	visualViewBoxWidth = 360
	visualBarHeight    = 240
	visualLineHeight   = 300
	visualPlotRight    = 328
	visualMargin       = 16
	visualYAxisGap     = 6
	visualLeftPad      = 4
	visualMinPlotLeft  = 32
	visualFont         = 12.5
	visualTitleFont    = 14
)

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
	case "bar", "line", "barGrouped":
	default:
		return visualSpecError("type must be bar, line or barGrouped")
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
type visualTick struct {
	value float64
	label string
}

// visualRenderMetrics exposes the computed left gutter for tests. Keeping it
// out of the SVG string makes the "gutter >= widest label + padding" contract
// assertable without re-estimating markup.
type visualRenderMetrics struct {
	PlotLeft         float64
	PlotRight        float64
	WidestYAxisLabel float64
}

// RenderVisualSpec validates and renders one self-contained figure. The visible
// L2/L3 badge is produced here, not read from the agent-written caption. The SVG
// is mobile-first: viewBox width 360, bar height 240, line height 300.
func RenderVisualSpec(spec VisualSpec) (string, error) {
	if err := ValidateVisualSpec(spec); err != nil {
		return "", err
	}
	svg, _ := renderVisualSVG(spec)
	var b strings.Builder
	fmt.Fprintf(&b, `<figure class="report-visual" data-level="%s" data-axis-truncated="%t">`, html.EscapeString(spec.Level), spec.AxisTruncated)
	b.WriteString(svg)
	if strings.TrimSpace(spec.Caption) != "" {
		b.WriteString(`<figcaption class="visual-caption" style="font-size:12.8px;line-height:1.7;color:#5b6577;margin-top:10px">`)
		b.WriteString(html.EscapeString(spec.Caption))
		b.WriteString(`</figcaption>`)
	}
	b.WriteString(`</figure>`)
	return b.String(), nil
}

func renderVisualSVG(spec VisualSpec) (string, visualRenderMetrics) {
	height := float64(visualBarHeight)
	if spec.Type == "line" {
		height = float64(visualLineHeight)
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<svg class="visual-svg" viewBox="0 0 %d %.0f" role="img" aria-label="%s" style="display:block;width:100%%;max-width:540px;margin:0 auto;height:auto;background:#fff" font-family="-apple-system,BlinkMacSystemFont,'Segoe UI','PingFang SC','Microsoft YaHei',sans-serif">`, visualViewBoxWidth, height, html.EscapeString(spec.Title))
	fmt.Fprintf(&b, `<title>%s</title>`, html.EscapeString(spec.Title))
	fmt.Fprintf(&b, `<desc>%s · %s。级别 %s。%s</desc>`, html.EscapeString(spec.Title), html.EscapeString(spec.Unit), html.EscapeString(spec.Level), html.EscapeString(spec.MetricDefinition))
	fmt.Fprintf(&b, `<rect x="0" y="0" width="%d" height="%.0f" fill="#ffffff"/>`, visualViewBoxWidth, height)

	badge := visualBadge(spec)
	titleY := 24.0
	if badge != "" {
		titleY = 46
		drawVisualBadge(&b, spec.Level, badge)
	}
	titleLines := wrapVisualText(spec.Title, 22, 2)
	titleBottom := titleY
	for i, line := range titleLines {
		y := titleY + float64(i)*18
		fmt.Fprintf(&b, `<text x="16" y="%.1f" font-size="%s" font-weight="700" fill="#1b2332">%s</text>`, y, numberText(visualTitleFont), html.EscapeString(line))
		titleBottom = y
	}
	unitY := titleBottom + 18
	fmt.Fprintf(&b, `<text x="16" y="%.1f" font-size="%s" fill="#5b6577">单位：%s</text>`, unitY, numberText(visualFont), html.EscapeString(spec.Unit))

	plotTop := unitY + 10
	legendBottom := unitY
	if len(spec.Series) > 1 {
		legendBottom = drawVisualLegend(&b, spec, unitY+18)
		if legendBottom+10 > plotTop {
			plotTop = legendBottom + 10
		}
	}
	plotBottom := height - 42
	if plotTop >= plotBottom-24 {
		// Keep the plot usable when a long title and a legend both consume the
		// header. The cap is in viewBox units, not CSS overflow.
		plotTop = plotBottom - 24
	}

	allValues := visualValues(spec)
	axisMin, axisMax, step := visualAxisScale(minVisualValue(allValues), maxVisualValue(allValues), spec.AxisTruncated)
	ticks := visualAxisTicks(axisMin, axisMax, step)
	widest := 0.0
	for _, tick := range ticks {
		if width := estimateVisualTextWidth(tick.label, visualFont); width > widest {
			widest = width
		}
	}
	plotLeft := visualPlotLeftFor(widest)
	yFor := func(value float64) float64 {
		if axisMax <= axisMin {
			return plotBottom
		}
		if value < axisMin {
			value = axisMin
		}
		if value > axisMax {
			value = axisMax
		}
		return plotBottom - (value-axisMin)/(axisMax-axisMin)*(plotBottom-plotTop)
	}
	drawVisualAxis(&b, plotLeft, plotTop, plotBottom, ticks, yFor)

	switch spec.Type {
	case "line":
		drawVisualLines(&b, spec, plotLeft, plotTop, plotBottom, yFor)
	case "barGrouped":
		drawVisualBars(&b, spec, plotLeft, plotTop, plotBottom, yFor, true)
	default:
		drawVisualBars(&b, spec, plotLeft, plotTop, plotBottom, yFor, false)
	}

	// The x-axis labels are drawn after the marks so they stay legible.
	drawVisualXLabels(&b, spec, plotLeft, height)
	if spec.AxisTruncated {
		drawVisualTruncatedMarker(&b, plotLeft, plotBottom)
	}
	b.WriteString(`</svg>`)
	return b.String(), visualRenderMetrics{PlotLeft: plotLeft, PlotRight: visualPlotRight, WidestYAxisLabel: widest}
}

func visualAxisTicks(axisMin, axisMax, step float64) []visualTick {
	ticks := make([]visualTick, 0, 8)
	for value := axisMin; value <= axisMax+step/1000; value += step {
		ticks = append(ticks, visualTick{value: value, label: visualNumber(value)})
	}
	return ticks
}

func visualPlotLeftFor(widestLabel float64) float64 {
	left := widestLabel + visualYAxisGap + visualLeftPad
	if left < visualMinPlotLeft {
		left = visualMinPlotLeft
	}
	// Keep a usable plot even for pathological labels; normal report values
	// (including 1,400,000 and negatives) are never capped.
	if max := float64(visualPlotRight - 80); left > max {
		left = max
	}
	return left
}

func visualBadge(spec VisualSpec) string {
	switch spec.Level {
	case "L2":
		refs := visualReferences(spec)
		if len(refs) == 0 {
			return "L2 · 据来源计算"
		}
		return "L2 · 据 " + strings.Join(refs, "") + " 计算"
	case "L3":
		return "L3 · 分析性判断，非官方数据"
	default:
		return ""
	}
}

func visualReferences(spec VisualSpec) []string {
	seen := map[string]bool{}
	var refs []string
	for _, text := range append([]string{spec.Formula}, spec.Inputs...) {
		for _, match := range visualReferencePattern.FindAllString(text, -1) {
			match = strings.ReplaceAll(match, " ", "")
			if !seen[match] {
				seen[match] = true
				refs = append(refs, match)
			}
		}
	}
	sort.Strings(refs)
	return refs
}

func drawVisualBadge(b *strings.Builder, level, badge string) {
	width := estimateVisualTextWidth(badge, visualFont) + 20
	if width > 328 {
		width = 328
	}
	fill, stroke, textFill := "#e8edfb", "#c9d6fb", "#2952cc"
	if level == "L3" {
		fill, stroke, textFill = "#fff7e8", "#f0d3a8", "#b45309"
	}
	fmt.Fprintf(b, `<rect x="16" y="10" width="%.1f" height="20" rx="10" fill="%s" stroke="%s"/>`, width, fill, stroke)
	fmt.Fprintf(b, `<text x="26" y="24" font-size="%s" font-weight="700" fill="%s">%s</text>`, numberText(visualFont), textFill, html.EscapeString(badge))
}

func drawVisualLegend(b *strings.Builder, spec VisualSpec, y float64) float64 {
	x := float64(visualMargin)
	lineY := y
	for i, series := range spec.Series {
		name := wrapVisualText(series.Name, 8, 1)
		label := ""
		if len(name) > 0 {
			label = name[0]
		}
		if strings.TrimSpace(label) == "" {
			label = fmt.Sprintf("系列%d", i+1)
		}
		itemWidth := 18 + estimateVisualTextWidth(label, visualFont) + 12
		if x+itemWidth > visualPlotRight {
			x = float64(visualMargin)
			lineY += 16
		}
		color := visualSeriesColor(i)
		fmt.Fprintf(b, `<rect x="%.1f" y="%.1f" width="10" height="3" rx="1.5" fill="%s"/>`, x, lineY-4, color)
		fmt.Fprintf(b, `<text x="%.1f" y="%.1f" font-size="%s" fill="#5b6577">%s</text>`, x+14, lineY, numberText(visualFont), html.EscapeString(label))
		x += itemWidth
	}
	return lineY
}

func drawVisualAxis(b *strings.Builder, plotLeft, plotTop, plotBottom float64, ticks []visualTick, yFor func(float64) float64) {
	fmt.Fprintf(b, `<line x1="%.2f" y1="%.1f" x2="%d" y2="%.1f" stroke="#c9d2e0" stroke-width="1.5"/>`, plotLeft, plotBottom, visualPlotRight, plotBottom)
	fmt.Fprintf(b, `<line x1="%.2f" y1="%.1f" x2="%.2f" y2="%.1f" stroke="#c9d2e0" stroke-width="1.5"/>`, plotLeft, plotTop, plotLeft, plotBottom)
	for _, tick := range ticks {
		y := yFor(tick.value)
		fmt.Fprintf(b, `<line x1="%.2f" y1="%.1f" x2="%d" y2="%.1f" stroke="#e2e7ef" stroke-width="1"/>`, plotLeft, y, visualPlotRight, y)
		fmt.Fprintf(b, `<text x="%.2f" y="%.1f" text-anchor="end" font-size="%s" fill="#5b6577">%s</text>`, plotLeft-visualYAxisGap, y+4, numberText(visualFont), html.EscapeString(tick.label))
	}
}

func drawVisualTruncatedMarker(b *strings.Builder, plotLeft, plotBottom float64) {
	fmt.Fprintf(b, `<g class="axis-truncated"><path d="M%.1f,%.1f L%.1f,%.1f L%.1f,%.1f L%.1f,%.1f" fill="none" stroke="#b45309" stroke-width="1.8"/><text x="%.1f" y="%.1f" text-anchor="end" font-size="%s" fill="#b45309">轴不从 0 起</text></g>`,
		plotLeft-6, plotBottom+2,
		plotLeft-1, plotBottom-4,
		plotLeft+4, plotBottom+2,
		plotLeft+9, plotBottom-4,
		float64(visualPlotRight)-4, plotBottom-4,
		numberText(visualFont),
	)
}

func drawVisualBars(b *strings.Builder, spec VisualSpec, plotLeft, plotTop, plotBottom float64, yFor func(float64) float64, grouped bool) {
	series := spec.Series
	if !grouped {
		series = series[:1]
	}
	if len(series) == 0 {
		return
	}
	labels := series[0].Points
	n := len(labels)
	if n == 0 {
		return
	}
	groupWidth := (visualPlotRight - plotLeft) / float64(n)
	barWidth := groupWidth * 0.56
	if grouped {
		barWidth = groupWidth * 0.72 / float64(len(series))
	}
	zero := yFor(0)
	for i := range labels {
		groupCenter := plotLeft + groupWidth*(float64(i)+0.5)
		for s, one := range series {
			if i >= len(one.Points) {
				continue
			}
			value := one.Points[i].Value
			top := yFor(value)
			y, height := top, zero-top
			if value < 0 {
				y, height = zero, top-zero
			}
			if height < 0 {
				height = 0
			}
			x := groupCenter - barWidth/2
			labelX := groupCenter
			if grouped {
				x = groupCenter - (barWidth*float64(len(series)))/2 + barWidth*float64(s)
				labelX = x + barWidth/2
			}
			fmt.Fprintf(b, `<rect x="%.2f" y="%.2f" width="%.2f" height="%.2f" rx="3" fill="%s"/>`, x, y, barWidth, height, visualSeriesColor(s))
			if n*len(series) <= 8 {
				text := visualNumber(value)
				labelY := top - 4
				if value < 0 {
					labelY = top + 12
				}
				labelX = visualCenteredLabelX(labelX, estimateVisualTextWidth(text, visualFont))
				fmt.Fprintf(b, `<text x="%.2f" y="%.2f" text-anchor="middle" font-size="%s" fill="#1b2332">%s</text>`, labelX, labelY, numberText(visualFont), html.EscapeString(text))
			}
		}
	}
}

func visualCenteredLabelX(x, width float64) float64 {
	left := width/2 + 2
	right := float64(visualViewBoxWidth) - width/2 - 2
	if left > right {
		return float64(visualViewBoxWidth) / 2
	}
	if x < left {
		return left
	}
	if x > right {
		return right
	}
	return x
}

func drawVisualLines(b *strings.Builder, spec VisualSpec, plotLeft, plotTop, plotBottom float64, yFor func(float64) float64) {
	if len(spec.Series) == 0 {
		return
	}
	n := len(spec.Series[0].Points)
	if n == 0 {
		return
	}
	for s, series := range spec.Series {
		if len(series.Points) < 2 {
			continue
		}
		var points strings.Builder
		for i, point := range series.Points {
			x := plotLeft + (visualPlotRight-plotLeft)*float64(i)/float64(n-1)
			y := yFor(point.Value)
			fmt.Fprintf(&points, "%.2f,%.2f ", x, y)
			fmt.Fprintf(b, `<circle cx="%.2f" cy="%.2f" r="3" fill="#ffffff" stroke="%s" stroke-width="2"/>`, x, y, visualSeriesColor(s))
		}
		fmt.Fprintf(b, `<polyline points="%s" fill="none" stroke="%s" stroke-width="2.6" stroke-linecap="round" stroke-linejoin="round"/>`, strings.TrimSpace(points.String()), visualSeriesColor(s))
		last := series.Points[len(series.Points)-1]
		lastX := float64(visualPlotRight)
		lastY := yFor(last.Value)
		fmt.Fprintf(b, `<text x="%.2f" y="%.2f" text-anchor="end" font-size="%s" font-weight="700" fill="%s">%s</text>`, lastX-2, lastY-5, numberText(visualFont), visualSeriesColor(s), html.EscapeString(visualNumber(last.Value)))
	}
}

func drawVisualXLabels(b *strings.Builder, spec VisualSpec, plotLeft, height float64) {
	if len(spec.Series) == 0 {
		return
	}
	points := spec.Series[0].Points
	n := len(points)
	if n == 0 {
		return
	}
	for i, point := range points {
		if n > 8 && i%2 == 1 {
			continue
		}
		x := plotLeft + (visualPlotRight-plotLeft)*float64(i)/float64(n-1)
		label := shortVisualLabel(point.Label, 8)
		x = visualCenteredLabelX(x, estimateVisualTextWidth(label, visualFont))
		fmt.Fprintf(b, `<text x="%.2f" y="%.1f" text-anchor="middle" font-size="%s" fill="#1b2332">%s</text>`, x, height-24, numberText(visualFont), html.EscapeString(label))
	}
}

func visualValues(spec VisualSpec) []float64 {
	var values []float64
	for _, series := range spec.Series {
		for _, point := range series.Points {
			values = append(values, point.Value)
		}
	}
	return values
}

func minVisualValue(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	min := values[0]
	for _, value := range values[1:] {
		if value < min {
			min = value
		}
	}
	return min
}

func maxVisualValue(values []float64) float64 {
	if len(values) == 0 {
		return 1
	}
	max := values[0]
	for _, value := range values[1:] {
		if value > max {
			max = value
		}
	}
	return max
}

func visualAxisScale(minValue, maxValue float64, truncated bool) (axisMin, axisMax, step float64) {
	if maxValue <= minValue {
		maxValue = minValue + 1
	}
	minEdge, maxEdge := minValue, maxValue
	if truncated {
		if minEdge > 0 {
			minEdge *= 0.9
		} else if minEdge < 0 {
			minEdge *= 1.1
		}
		if maxEdge < 0 {
			maxEdge = 0
		}
	} else {
		if minEdge > 0 {
			minEdge = 0
		}
		if maxEdge < 0 {
			maxEdge = 0
		}
	}
	span := maxEdge - minEdge
	if span <= 0 {
		span = 1
	}
	raw := span / 5
	magnitude := math.Pow(10, math.Floor(math.Log10(raw)))
	if magnitude <= 0 {
		magnitude = 1
	}
	step = magnitude * 10
	for _, multiple := range []float64{1, 2, 2.5, 5, 10} {
		if raw <= multiple*magnitude {
			step = multiple * magnitude
			break
		}
	}
	if step <= 0 {
		step = 1
	}
	axisMin = math.Floor(minEdge/step) * step
	axisMax = math.Ceil(maxEdge/step) * step
	if axisMax <= axisMin {
		axisMax = axisMin + step
	}
	return axisMin, axisMax, step
}

func visualSeriesColor(index int) string {
	colors := []string{"#2952cc", "#0f766e", "#b45309", "#7c3aed", "#be123c"}
	return colors[index%len(colors)]
}

func visualNumber(value float64) string {
	if math.Abs(value-math.Round(value)) < 1e-9 {
		return addVisualThousands(strconv.FormatInt(int64(math.Round(value)), 10))
	}
	text := strconv.FormatFloat(value, 'f', 2, 64)
	text = strings.TrimRight(strings.TrimRight(text, "0"), ".")
	parts := strings.SplitN(text, ".", 2)
	out := addVisualThousands(parts[0])
	if len(parts) == 2 {
		out += "." + parts[1]
	}
	return out
}

func addVisualThousands(value string) string {
	negative := strings.HasPrefix(value, "-")
	if negative {
		value = strings.TrimPrefix(value, "-")
	}
	for i := len(value) - 3; i > 0; i -= 3 {
		value = value[:i] + "," + value[i:]
	}
	if negative {
		return "-" + value
	}
	return value
}

func wrapVisualText(text string, maxRunes, maxLines int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return []string{""}
	}
	runes := []rune(text)
	var lines []string
	for len(runes) > 0 && len(lines) < maxLines {
		take := maxRunes
		if len(runes) < take {
			take = len(runes)
		}
		line := string(runes[:take])
		runes = runes[take:]
		if len(lines) == maxLines-1 && len(runes) > 0 {
			line = strings.TrimRight(line, "，、；：,;: ") + "…"
		}
		lines = append(lines, line)
	}
	if len(lines) == 0 {
		return []string{""}
	}
	return lines
}

func shortVisualLabel(text string, maxRunes int) string {
	runes := []rune(strings.TrimSpace(text))
	if len(runes) <= maxRunes {
		return string(runes)
	}
	if maxRunes <= 1 {
		return "…"
	}
	return string(runes[:maxRunes-1]) + "…"
}

func estimateVisualTextWidth(text string, fontSize float64) float64 {
	width := 0.0
	for _, r := range text {
		switch {
		case unicode.IsSpace(r):
			width += fontSize * 0.35
		case r < 128:
			width += fontSize * 0.58
		default:
			width += fontSize
		}
	}
	return width
}

func numberText(value float64) string {
	return strconv.FormatFloat(value, 'f', -1, 64)
}
