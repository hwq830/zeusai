package reporttemplate

import (
	"encoding/json"
	"fmt"
	"html"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Charts are stored as fenced JSON inside the report, so old clients and saved
// reports continue to use the existing Markdown format.
var chartBlock = regexp.MustCompile("(?ms)^```zeus-chart[ \\t]*\\r?\\n(.*?)\\r?\\n```[ \\t]*(?:\\r?\\n|$)")

type Chart struct {
	Type   string       `json:"type"`
	Title  string       `json:"title"`
	Unit   string       `json:"unit"`
	Period string       `json:"period"`
	Points []ChartPoint `json:"points"`
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
	if c.Type != "bar" && c.Type != "line" {
		return c, false
	}
	if len(c.Title) == 0 || len(c.Title) > 160 || len(c.Unit) == 0 || len(c.Unit) > 64 || len(c.Period) == 0 || len(c.Period) > 64 {
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
	count := 0
	return chartBlock.ReplaceAllStringFunc(markdown, func(block string) string {
		parts := chartBlock.FindStringSubmatch(block)
		if len(parts) != 2 {
			return ""
		}
		c, ok := parseChart(parts[1], valid)
		if !ok || count >= 3 {
			return ""
		}
		for _, point := range c.Points {
			supported := false
			num := regexp.MustCompile(`(^|[^0-9.])` + regexp.QuoteMeta(string(point.Value)) + `([^0-9.]|$)`)
			cite := regexp.MustCompile(`\[` + strconv.Itoa(point.Source) + `\]`)
			for _, para := range paragraphs {
				if strings.Contains(para, point.Label) && strings.Contains(para, c.Unit) && strings.Contains(para, c.Period) && num.MatchString(citeRe.ReplaceAllString(para, "")) && cite.MatchString(para) {
					supported = true
					break
				}
			}
			if !supported {
				return ""
			}
		}
		count++
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
	if lang == "en" {
		dataLabel, sourceLabel = "View data table", "Sources"
	} else if lang == "zh-Hant" {
		dataLabel, sourceLabel = "查看資料表", "來源"
	}
	var b strings.Builder
	fmt.Fprintf(&b, `<figure class="report-chart" data-period="%s" data-unit="%s"><figcaption><strong>%s</strong><span>%s · %s</span></figcaption>`, html.EscapeString(c.Period), html.EscapeString(c.Unit), html.EscapeString(c.Title), html.EscapeString(c.Period), html.EscapeString(c.Unit))
	if c.Type == "line" {
		b.WriteString(`<svg class="chart-line" viewBox="0 0 320 120" role="img" aria-label="` + html.EscapeString(c.Title) + `">`)
		var coords strings.Builder
		for i, p := range c.Points {
			value, _ := strconv.ParseFloat(string(p.Value), 64)
			x := 20.0 + 280.0*float64(i)/float64(len(c.Points)-1)
			y := 100.0
			if max > 0 {
				y = 100.0 - 80.0*value/max
			}
			fmt.Fprintf(&coords, "%.2f,%.2f ", x, y)
		}
		fmt.Fprintf(&b, `<polyline fill="none" stroke="#486cda" stroke-width="3" points="%s"/></svg>`, coords.String())
	}
	b.WriteString(`<div class="chart-rows">`)
	seen := map[int]bool{}
	for _, p := range c.Points {
		v, _ := strconv.ParseFloat(string(p.Value), 64)
		width := 0.0
		if max > 0 {
			width = 100 * v / max
		}
		fmt.Fprintf(&b, `<div class="chart-row"><span class="chart-label">%s</span><span class="chart-track"><span class="chart-bar" style="width:%.3f%%"></span></span><span class="chart-value">%s</span></div>`, html.EscapeString(p.Label), width, html.EscapeString(string(p.Value)))
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
	b.WriteString(`</div><details><summary>` + dataLabel + `</summary><table><tbody>`)
	for _, p := range c.Points {
		fmt.Fprintf(&b, `<tr><th scope="row">%s</th><td>%s %s</td><td><a href="#src-%d">[%d]</a></td></tr>`, html.EscapeString(p.Label), html.EscapeString(string(p.Value)), html.EscapeString(c.Unit), p.Source, p.Source)
	}
	b.WriteString(`</tbody></table></details></figure>`)
	return b.String()
}
