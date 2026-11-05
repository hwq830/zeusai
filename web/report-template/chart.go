package reporttemplate

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Charts are stored as fenced JSON inside the report, so old clients and saved
// reports continue to use the existing Markdown format.
var chartBlock = regexp.MustCompile("(?ms)^```zeus-chart[ \\t]*\\r?\\n(.*?)\\r?\\n```[ \\t]*(?:\\r?\\n|$)")
var evidenceNumber = regexp.MustCompile(`[-−–—+]?\s*[0-9][0-9,.]*`)
var validEvidenceNumber = regexp.MustCompile(`^(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$`)

// Match whole nonnegative numeric tokens, accepting conventional thousands
// separators without treating a negative or part of a grouped number as evidence.
func containsEvidenceNumber(text, value string) bool {
	for _, token := range evidenceNumber.FindAllString(text, -1) {
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "-") || strings.HasPrefix(token, "−") || strings.HasPrefix(token, "–") || strings.HasPrefix(token, "—") {
			continue
		}
		token = strings.TrimPrefix(token, "+")
		token = strings.TrimRight(token, ",.")
		if validEvidenceNumber.MatchString(token) && strings.ReplaceAll(token, ",", "") == value {
			return true
		}
	}
	return false
}

type Chart struct {
	Type      string       `json:"type"`
	Title     string       `json:"title"`
	Unit      string       `json:"unit"`
	Period    string       `json:"period"`
	Scope     string       `json:"scope,omitempty"`
	Qualifier string       `json:"qualifier,omitempty"`
	Points    []ChartPoint `json:"points"`
}
type ChartPoint struct {
	Label  string      `json:"label"`
	Value  json.Number `json:"value"`
	Source int         `json:"source"`
}

func parseChart(raw string, sources map[int]bool) (Chart, bool) {
	var c Chart
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &c) != nil || len(c.Points) < 2 || len(c.Points) > 8 {
		return c, false
	}
	if c.Type != "bar" && c.Type != "line" && c.Type != "dot" {
		return c, false
	}
	if len(c.Title) == 0 || len(c.Title) > 160 || len(c.Unit) == 0 || len(c.Unit) > 64 || len(c.Period) == 0 || len(c.Period) > 64 || len(c.Scope) > 160 || len(c.Qualifier) > 80 {
		return c, false
	}
	labels := map[string]bool{}
	for _, p := range c.Points {
		value, err := strconv.ParseFloat(string(p.Value), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e15 || len(p.Label) == 0 || len(p.Label) > 100 || labels[p.Label] || !sources[p.Source] {
			return c, false
		}
		labels[p.Label] = true
	}
	return c, true
}

// Only newly generated trend charts are filtered this way; rendering historical
// charts remains backward compatible. We cannot infer chronology from categories.
func chronological(points []ChartPoint) bool {
	var previous time.Time
	precision := 0
	for _, p := range points {
		if precision == 0 {
			precision = len(p.Label)
		} else if precision != len(p.Label) {
			return false
		}
		var current time.Time
		var err error
		switch len(p.Label) {
		case 4:
			current, err = time.Parse("2006", p.Label)
		case 7:
			current, err = time.Parse("2006-01", p.Label)
		case 10:
			current, err = time.Parse("2006-01-02", p.Label)
		default:
			return false
		}
		if err != nil || (!previous.IsZero() && !current.After(previous)) {
			return false
		}
		previous = current
	}
	return true
}

func chartDate(label string) time.Time {
	layout := map[int]string{4: "2006", 7: "2006-01", 10: "2006-01-02"}[len(label)]
	t, _ := time.Parse(layout, label)
	return t
}

// WithoutCharts returns the readable Markdown without embedded chart JSON.
func WithoutCharts(markdown string) string { return chartBlock.ReplaceAllString(markdown, "") }

// FilterCharts only retains charts whose labels, values, unit and period appear
// together with the cited source in one research-notes paragraph. The prose is
// written independently, so rejecting a chart never removes the report text.
func FilterCharts(markdown, evidence string, sourceCount int) string {
	valid := map[int]bool{}
	for i := 1; i <= sourceCount; i++ {
		valid[i] = true
	}
	paragraphs := strings.Split(strings.ReplaceAll(evidence, "\r\n", "\n"), "\n\n")
	return chartBlock.ReplaceAllStringFunc(markdown, func(block string) string {
		parts := chartBlock.FindStringSubmatch(block)
		if len(parts) != 2 {
			return ""
		}
		if spec, ok := parseVisualSpec(parts[1]); ok {
			if ValidateVisualSpec(spec) == nil {
				return block
			}
			return ""
		}
		c, ok := parseChart(parts[1], valid)
		if !ok || (c.Type == "line" && (!chronological(c.Points) || strings.TrimSpace(c.Scope) == "")) {
			return ""
		}
		if c.Type == "line" {
			for _, point := range c.Points {
				if point.Source != c.Points[0].Source {
					return ""
				}
			}
		}
		for _, point := range c.Points {
			supported := false
			cite := regexp.MustCompile(`\[` + strconv.Itoa(point.Source) + `\]`)
			for _, para := range paragraphs {
				lower := strings.ToLower(para)
				qualifier := strings.ToLower(c.Qualifier)
				marked := false
				mismatch := false
				for _, word := range []string{"预测", "預測", "预估", "預估", "估算", "估计", "估計", "假设", "假設", "情景", "forecast", "estimate", "projected", "projection", "scenario"} {
					if strings.Contains(lower, word) {
						marked = true
						if !strings.Contains(qualifier, word) {
							mismatch = true
						}
					}
				}
				// A mixed forecast/actual paragraph cannot safely establish which
				// category a co-occurring number belongs to.
				if mismatch || (marked && (strings.Contains(lower, "实际") || strings.Contains(lower, "實際") || strings.Contains(lower, "actual"))) {
					continue
				}
				if strings.Contains(para, point.Label) && strings.Contains(para, c.Unit) && strings.Contains(para, c.Period) && (c.Type != "line" || strings.Contains(para, c.Title)) && (c.Scope == "" || strings.Contains(para, c.Scope)) && (c.Qualifier == "" || strings.Contains(para, c.Qualifier)) && containsEvidenceNumber(citeRe.ReplaceAllString(para, ""), string(point.Value)) && cite.MatchString(para) {
					supported = true
					break
				}
			}
			if !supported {
				return ""
			}
		}
		return block
	})
}

func renderChart(c Chart, lang string) string {
	max := 0.0
	for _, p := range c.Points {
		v, _ := strconv.ParseFloat(string(p.Value), 64)
		if v > max {
			max = v
		}
	}
	dataLabel, sourceLabel := "查看数据表", "来源"
	missingScope := "统计范围未标注"
	if lang == "en" {
		dataLabel, sourceLabel = "View data table", "Sources"
		missingScope = "Scope not specified"
	} else if lang == "zh-Hant" {
		dataLabel, sourceLabel = "查看資料表", "來源"
		missingScope = "統計範圍未標註"
	}
	var b strings.Builder
	scope := c.Scope
	if scope == "" {
		scope = missingScope
	}
	fmt.Fprintf(&b, `<figure class="report-chart" data-period="%s" data-unit="%s" data-scope="%s" data-qualifier="%s"><figcaption><strong>%s</strong><span>%s · %s</span></figcaption>`, html.EscapeString(c.Period), html.EscapeString(c.Unit), html.EscapeString(scope), html.EscapeString(c.Qualifier), html.EscapeString(c.Title), html.EscapeString(c.Period), html.EscapeString(c.Unit))
	fmt.Fprintf(&b, `<div class="chart-context">%s %s</div>`, html.EscapeString(scope), html.EscapeString(c.Qualifier))
	if c.Type == "line" {
		b.WriteString(`<svg class="chart-line" viewBox="0 0 320 120" role="img" aria-label="` + html.EscapeString(c.Title) + `">`)
		var coords strings.Builder
		for i, p := range c.Points {
			value, _ := strconv.ParseFloat(string(p.Value), 64)
			x := 20.0 + 280.0*float64(i)/float64(len(c.Points)-1)
			if chronological(c.Points) {
				start, end, at := chartDate(c.Points[0].Label), chartDate(c.Points[len(c.Points)-1].Label), chartDate(p.Label)
				x = 20.0 + 280.0*float64(at.Unix()-start.Unix())/float64(end.Unix()-start.Unix())
			}
			y := 100.0
			if max > 0 {
				y = 100.0 - 80.0*value/max
			}
			fmt.Fprintf(&coords, "%.2f,%.2f ", x, y)
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="currentColor" stroke-width="3" points="%s"/></svg>`, coords.String())
	}
	b.WriteString(`<div class="chart-rows">`)
	seen := map[int]bool{}
	for _, p := range c.Points {
		v, _ := strconv.ParseFloat(string(p.Value), 64)
		width := 0.0
		if max > 0 {
			width = 100 * v / max
		}
		label := fmt.Sprintf(`%s <a class="chart-point-source" href="#src-%d">[%d]</a>`, html.EscapeString(p.Label), p.Source, p.Source)
		if c.Type == "dot" {
			fmt.Fprintf(&b, `<div class="chart-row"><span class="chart-label">%s</span><span class="chart-track chart-dot-track"><span class="chart-dot" style="left:%.3f%%"></span></span><span class="chart-value">%s</span></div>`, label, width, html.EscapeString(string(p.Value)))
		} else if c.Type == "line" {
			fmt.Fprintf(&b, `<div class="chart-line-value"><span>%s</span><strong>%s</strong></div>`, label, html.EscapeString(string(p.Value)))
		} else {
			fmt.Fprintf(&b, `<div class="chart-row"><span class="chart-label">%s</span><span class="chart-track"><span class="chart-bar" style="width:%.3f%%"></span></span><span class="chart-value">%s</span></div>`, label, width, html.EscapeString(string(p.Value)))
		}
		seen[p.Source] = true
	}
	b.WriteString(`</div><div class="chart-source">` + sourceLabel + `: `)
	first := true
	for _, p := range c.Points {
		if !seen[p.Source] {
			continue
		}
		if !first {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, `<a href="#src-%d">[%d]</a>`, p.Source, p.Source)
		first = false
		delete(seen, p.Source)
	}
	b.WriteString(`</div><details><summary>` + dataLabel + `</summary>`)
	var table strings.Builder
	table.WriteString(`<table><tbody>`)
	for _, p := range c.Points {
		fmt.Fprintf(&table, `<tr><th scope="row">%s</th><td>%s %s</td><td><a href="#src-%d">[%d]</a></td></tr>`, html.EscapeString(p.Label), html.EscapeString(string(p.Value)), html.EscapeString(c.Unit), p.Source, p.Source)
	}
	table.WriteString(`</tbody></table>`)
	b.WriteString(table.String() + `</details><div class="chart-print-table">` + table.String() + `</div></figure>`)
	return b.String()
}
