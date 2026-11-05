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
	"unicode"
	"unicode/utf8"
)

// Charts are stored as fenced JSON inside the report, so old clients and saved
// reports continue to use the existing Markdown format.
var chartBlock = regexp.MustCompile("(?ms)^```zeus-chart[ \\t]*\\r?\\n(.*?)\\r?\\n```[ \\t]*(?:\\r?\\n|$)")
var evidenceNumber = regexp.MustCompile(`[-−–—+]?\s*[0-9][0-9,.]*`)
var validEvidenceNumber = regexp.MustCompile(`^(?:[0-9]+|[0-9]{1,3}(?:,[0-9]{3})+)(?:\.[0-9]+)?$`)
var validChartDecimal = regexp.MustCompile(`^(?:0|[1-9][0-9]*)(?:\.[0-9]+)?$`)
var dateRange = regexp.MustCompile(`(?i)^\s*(\d{4}(?:-\d{2}(?:-\d{2})?)?)\s*(?:—|–|~|至|到|to|-)\s*(\d{4}(?:-\d{2}(?:-\d{2})?)?)\s*$`)
var valueBeforeDate = regexp.MustCompile(`(?i)^\s*(?:in|for|during|at)\b[^0-9\n;；。]{0,30}$`)
var fourDigitNumber = regexp.MustCompile(`[0-9]{4}`)
var leadingGeoScope = regexp.MustCompile(`^(?:国内|國內|全国|全國|全球|全省|全市|全县|全縣|本省|本市|本县|本縣|[\p{Han}]{1,8}?(?:自治区|自治區|地区|地區|省|市|县|縣|州|国|國))`)
var regionalContext = regexp.MustCompile(`[\p{Han}]{1,8}?(?:自治区|自治區|地区|地區|省|市|县|縣|州)`)

// Match whole nonnegative numeric tokens, accepting conventional thousands
// separators without treating a negative or part of a grouped number as evidence.
func containsEvidenceNumber(text, value string) bool {
	if !validChartDecimal.MatchString(value) {
		return false
	}
	value = trimDecimalZeros(value)
	for _, token := range evidenceNumber.FindAllString(text, -1) {
		token = strings.TrimSpace(token)
		if strings.HasPrefix(token, "-") || strings.HasPrefix(token, "−") || strings.HasPrefix(token, "–") || strings.HasPrefix(token, "—") {
			continue
		}
		token = strings.TrimPrefix(token, "+")
		token = strings.TrimRight(token, ",.")
		if validEvidenceNumber.MatchString(token) && trimDecimalZeros(strings.ReplaceAll(token, ",", "")) == value {
			return true
		}
	}
	return false
}

// Only trim the fractional part: integer zeroes and all significant digits are retained.
func trimDecimalZeros(value string) string {
	if strings.Contains(value, ".") {
		return strings.TrimSuffix(strings.TrimRight(value, "0"), ".")
	}
	return value
}

// A value must follow its own label in the same clause, without another
// plotted label in between. Paragraph-wide co-occurrence alone can attach
// another category's number to this point.
func pointValueMatches(text string, chart Chart, point ChartPoint) bool {
	originalText := text
	text = citeRe.ReplaceAllString(text, "")
	for _, loc := range evidenceNumber.FindAllStringIndex(text, -1) {
		if !containsEvidenceNumber(text[loc[0]:loc[1]], string(point.Value)) {
			continue
		}
		token := text[loc[0]:loc[1]]
		leading := len(token) - len(strings.TrimLeftFunc(token, unicode.IsSpace))
		prefix := text[:loc[0]+leading]
		boundary := strings.LastIndexAny(prefix, "\n;；。")
		last, label := -1, ""
		for _, candidate := range chart.Points {
			if i := strings.LastIndex(prefix, candidate.Label); i > last || (i == last && i >= 0 && len(candidate.Label) > len(label)) {
				last, label = i, candidate.Label
			}
		}
		if label == point.Label && last > boundary && len(prefix)-(last+len(label)) <= 200 {
			return true
		}
		// Some official releases say “60,322,400 in mid-2022”. Accept the
		// value-before-date form only when the date follows in the same clause
		// with an explicit time preposition and no intervening chart label.
		if last <= boundary {
			suffix := text[loc[1]:]
			if end := strings.IndexAny(suffix, "\n;；。"); end >= 0 {
				suffix = suffix[:end]
			}
			first, following := len(suffix)+1, ""
			for _, candidate := range chart.Points {
				if i := strings.Index(suffix, candidate.Label); i >= 0 && i < first {
					first, following = i, candidate.Label
				}
			}
			if following == point.Label && valueBeforeDate.MatchString(suffix[:first]) {
				return true
			}
		}
	}
	return scopedAnnualValueMatches(originalText, chart, point)
}

// Some official annual releases state the year in one paragraph line and put
// the next annual metric at the start of the following line. Accept that form
// only when the cited passage has one unambiguous year and the metric's exact
// scope is immediately followed by the original number and unit. In
// particular, never carry a year across a quarterly, seasonal, forecast or
// category-qualified statement.
func scopedAnnualValueMatches(text string, chart Chart, point ChartPoint) bool {
	if (chart.Type != "bar" && chart.Type != "dot") || len(point.Label) != 4 || strings.TrimSpace(chart.Scope) == "" || !chronological(chart.Points) {
		return false
	}
	// A metric-only scope such as “出口总量” cannot establish whether the
	// preceding annual context belongs to the same region. Require an explicit
	// geographic scope and reject any incompatible region in the year clause.
	geoScope := leadingGeoScope.FindString(chart.Scope)
	if geoScope == "" {
		return false
	}
	unsafePeriod := []string{"季度", "上半年", "下半年", "月份", "月度", "假期", "节假日", "節假日", "春节", "春節", "预测", "預測", "预估", "預估", "预计", "預計", "估算", "估计", "估計", "假设", "假設"}
	for _, marker := range append(unsafePeriod, "月", "周", "季", "半年") {
		if strings.Contains(chart.Scope, marker) {
			return false
		}
	}
	yearBefore, yearClauseEnd := -1, -1
	yearMetric := ""
	for _, loc := range fourDigitNumber.FindAllStringIndex(text, -1) {
		// Do not interpret a substring of a larger number or a decimal as a
		// year. Other standalone four-digit numbers conservatively block it.
		if (loc[0] > 0 && ((text[loc[0]-1] >= '0' && text[loc[0]-1] <= '9') || text[loc[0]-1] == '.')) || (loc[1] < len(text) && ((text[loc[1]] >= '0' && text[loc[1]] <= '9') || text[loc[1]] == '.')) {
			continue
		}
		token := text[loc[0]:loc[1]]
		if token != point.Label {
			return false
		}
		if strings.HasPrefix(strings.TrimLeftFunc(text[loc[1]:], unicode.IsSpace), "年") {
			start := strings.LastIndexAny(text[:loc[0]], "\n;；。")
			if start >= 0 {
				_, width := utf8.DecodeRuneInString(text[start:])
				start += width
			} else {
				start = 0
			}
			yearClause := text[start:]
			if end := strings.IndexAny(yearClause, "\n;；。"); end >= 0 {
				yearClause = yearClause[:end]
			}
			for _, cite := range citeRe.FindAllStringSubmatch(yearClause, -1) {
				index, _ := strconv.Atoi(cite[1])
				if index != point.Source {
					return false
				}
			}
			if !strings.Contains(yearClause, geoScope) {
				return false
			}
			// The year sentence must introduce this geographic scope directly.
			// An unqualified place name before or between them (e.g. “上海：”)
			// would otherwise allow two regions to masquerade as national data.
			prefix := strings.TrimFunc(text[start:loc[0]], func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
			if prefix != "" && prefix != geoScope && prefix != "据统计" && prefix != "據統計" && prefix != "根据统计" && prefix != "根據統計" {
				return false
			}
			afterYear := strings.TrimLeftFunc(text[loc[1]:], unicode.IsSpace)
			afterYear = strings.TrimPrefix(afterYear, "年")
			afterYear = strings.TrimLeftFunc(afterYear, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsPunct(r) })
			if !strings.HasPrefix(afterYear, geoScope) {
				return false
			}
			afterGeoText := strings.TrimLeftFunc(strings.TrimPrefix(afterYear, geoScope), unicode.IsSpace)
			afterGeo := []rune(afterGeoText)
			if len(afterGeo) < 2 || !strings.Contains(strings.TrimPrefix(chart.Scope, geoScope), string(afterGeo[:2])) {
				return false
			}
			metricEnd := evidenceNumber.FindStringIndex(afterGeoText)
			if metricEnd == nil {
				return false
			}
			metric := strings.TrimSpace(afterGeoText[:metricEnd[0]])
			if len([]rune(metric)) < 2 || len([]rune(metric)) > 30 {
				return false
			}
			for _, region := range regionalContext.FindAllString(yearClause, -1) {
				if !strings.Contains(chart.Scope, region) {
					return false
				}
			}
			for _, marker := range append(unsafePeriod, "月", "周", "季", "半年") {
				if strings.Contains(yearClause, marker) {
					return false
				}
			}
			if yearBefore < 0 {
				yearBefore = loc[0]
				yearClauseEnd = start + len(yearClause)
				yearMetric = metric
			}
		}
	}
	if yearBefore < 0 {
		return false
	}
	for _, loc := range evidenceNumber.FindAllStringIndex(text, -1) {
		if !containsEvidenceNumber(text[loc[0]:loc[1]], string(point.Value)) {
			continue
		}
		leading := len(text[loc[0]:loc[1]]) - len(strings.TrimLeftFunc(text[loc[0]:loc[1]], unicode.IsSpace))
		prefix := text[:loc[0]+leading]
		boundary := strings.LastIndexAny(prefix, "\n;；。")
		if boundary < 0 || yearBefore >= boundary {
			continue
		}
		_, boundaryWidth := utf8.DecodeRuneInString(prefix[boundary:])
		if strings.TrimSpace(prefix[boundary+boundaryWidth:]) != chart.Scope {
			continue
		}
		// Between the annual context and the target, allow only explicit
		// breakdowns of the different metric established by the year sentence.
		// This prevents a quarterly/category heading from re-scoping the target.
		if yearClauseEnd < 0 || yearClauseEnd > boundary {
			continue
		}
		intermediate := text[yearClauseEnd : boundary+boundaryWidth]
		unsafeIntermediate := false
		for _, cite := range citeRe.FindAllStringSubmatch(intermediate, -1) {
			index, _ := strconv.Atoi(cite[1])
			if index != point.Source {
				unsafeIntermediate = true
				break
			}
		}
		if unsafeIntermediate {
			continue
		}
		for _, part := range strings.FieldsFunc(intermediate, func(r rune) bool { return r == '\n' || r == '。' || r == '；' || r == ';' }) {
			part = strings.TrimSpace(citeRe.ReplaceAllString(part, ""))
			if part == "" {
				continue
			}
			if !strings.Contains(part, yearMetric) || strings.Contains(chart.Scope, yearMetric) {
				unsafeIntermediate = true
				break
			}
			for _, region := range regionalContext.FindAllString(part, -1) {
				if !strings.Contains(chart.Scope, region) {
					unsafeIntermediate = true
					break
				}
			}
			if unsafeIntermediate {
				break
			}
		}
		if unsafeIntermediate {
			continue
		}
		suffix := strings.TrimLeftFunc(text[loc[1]:], unicode.IsSpace)
		if !strings.HasPrefix(suffix, chart.Unit) {
			continue
		}
		afterUnit := strings.TrimPrefix(suffix, chart.Unit)
		if afterUnit != "" {
			next, _ := utf8.DecodeRuneInString(afterUnit)
			if unicode.IsLetter(next) || unicode.IsDigit(next) {
				continue
			}
		}
		clause := suffix
		if end := strings.IndexAny(clause, "\n;；。"); end >= 0 {
			clause = clause[:end]
		}
		unsafe := false
		for _, marker := range unsafePeriod {
			if strings.Contains(clause, marker) {
				unsafe = true
				break
			}
		}
		if !unsafe {
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

// schemaRejection identifies a safe, content-free reason for an invalid chart.
// It is diagnostic only: parseChart remains the authority for acceptance.
func schemaRejection(raw string, sources map[int]bool) string {
	if len(raw) > 8192 {
		return "schema_block_too_long"
	}
	var c Chart
	if json.Unmarshal([]byte(raw), &c) != nil {
		return "schema_invalid_json_or_field_type"
	}
	if len(c.Points) < 2 || len(c.Points) > 8 {
		return "schema_point_count"
	}
	if c.Type != "bar" && c.Type != "line" && c.Type != "dot" {
		return "schema_chart_type"
	}
	if len(c.Title) == 0 || len(c.Title) > 160 {
		return "schema_title_length"
	}
	if len(c.Unit) == 0 || len(c.Unit) > 64 {
		return "schema_unit_length"
	}
	if len(c.Period) == 0 || len(c.Period) > 64 {
		return "schema_period_length"
	}
	if len(c.Scope) > 160 {
		return "schema_scope_length"
	}
	if len(c.Qualifier) > 80 {
		return "schema_qualifier_length"
	}
	labels := map[string]bool{}
	for _, p := range c.Points {
		value, err := strconv.ParseFloat(string(p.Value), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1e15 {
			return "schema_point_value"
		}
		if len(p.Label) == 0 || len(p.Label) > 100 || labels[p.Label] {
			return "schema_point_label"
		}
		if !sources[p.Source] {
			return "schema_point_source"
		}
		labels[p.Label] = true
	}
	return "schema_other"
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
func WithoutCharts(markdown string) string {
	return structureBlock.ReplaceAllString(chartBlock.ReplaceAllString(markdown, ""), "")
}

// ChartBlocks extracts existing chart assets without retaining surrounding prose.
func ChartBlocks(markdown string) string {
	return strings.Join(chartBlock.FindAllString(markdown, -1), "\n")
}

// normalizeChartText removes only presentation differences: whitespace,
// full-width digits/letters and the full-width percent sign. It never expands
// synonyms, abbreviations or metric names, so containment after normalization
// is still literal containment of the original words.
func normalizeChartText(s string) string {
	return normalizeChartTextWithSpaces(s, false)
}

// normalizeChartTextKeepSpaces normalizes width and case but keeps one space
// between tokens. Value binding relies on word boundaries (for example
// "in mid-2022"), so it cannot use the compact form.
func normalizeChartTextKeepSpaces(s string) string {
	return normalizeChartTextWithSpaces(s, true)
}

func normalizeChartTextWithSpaces(s string, keepSpaces bool) string {
	runes := []rune(s)
	var b strings.Builder
	last := rune(0)
	for i := 0; i < len(runes); i++ {
		r := runes[i]
		if r == '\n' && keepSpaces {
			if b.Len() > 0 && last != '\n' {
				b.WriteRune('\n')
			}
			last = '\n'
			continue
		}
		if unicode.IsSpace(r) {
			j := i + 1
			for j < len(runes) && unicode.IsSpace(runes[j]) {
				j++
			}
			if keepSpaces {
				if b.Len() > 0 && last != ' ' && last != '\n' {
					b.WriteRune(' ')
					last = ' '
				}
			} else if j < len(runes) && unicode.IsDigit(last) && isChartDigit(runes[j]) {
				// Keep one separator between two digits so a year and its value
				// are not concatenated.
				b.WriteRune(' ')
				last = ' '
			}
			continue
		}
		switch {
		case r >= '０' && r <= '９':
			r = '0' + (r - '０')
		case r >= 'Ａ' && r <= 'Ｚ':
			r = 'A' + (r - 'Ａ')
		case r >= 'ａ' && r <= 'ｚ':
			r = 'a' + (r - 'ａ')
		case r == '％':
			r = '%'
		}
		r = unicode.ToLower(r)
		b.WriteRune(r)
		last = r
	}
	return strings.TrimSpace(b.String())
}

func isChartDigit(r rune) bool {
	return (r >= '0' && r <= '9') || (r >= '０' && r <= '９')
}

func normalizedChart(c Chart) Chart {
	return normalizedChartWith(c, normalizeChartText)
}

func normalizedChartKeepSpaces(c Chart) Chart {
	return normalizedChartWith(c, normalizeChartTextKeepSpaces)
}

func normalizedChartWith(c Chart, normalize func(string) string) Chart {
	out := c
	out.Title = normalize(c.Title)
	out.Period = normalize(c.Period)
	out.Unit = normalize(c.Unit)
	out.Scope = normalize(c.Scope)
	out.Qualifier = normalize(c.Qualifier)
	out.Points = make([]ChartPoint, len(c.Points))
	for i, point := range c.Points {
		point.Label = normalize(point.Label)
		out.Points[i] = point
	}
	return out
}

// FilterCharts only retains charts whose labels, values, unit and period appear
// together with the cited source in one research-notes paragraph. The prose is
// written independently, so rejecting a chart never removes the report text.
func FilterCharts(markdown, evidence string, sourceCount int) string {
	filtered, _ := FilterChartsWithReasons(markdown, evidence, sourceCount)
	return filtered
}

// FilterChartsWithReasons reports aggregate rejection categories without exposing source text.
func FilterChartsWithReasons(markdown, evidence string, sourceCount int) (string, map[string]int) {
	reasons := map[string]int{}
	valid := map[int]bool{}
	for i := 1; i <= sourceCount; i++ {
		valid[i] = true
	}
	paragraphs := strings.Split(strings.ReplaceAll(evidence, "\r\n", "\n"), "\n\n")
	filtered := chartBlock.ReplaceAllStringFunc(markdown, func(block string) string {
		parts := chartBlock.FindStringSubmatch(block)
		if len(parts) != 2 {
			return ""
		}
		c, ok := parseChart(parts[1], valid)
		if !ok {
			reasons["invalid_schema"]++
			reasons[schemaRejection(parts[1], valid)]++
			return ""
		}
		if c.Type == "line" && (!chronological(c.Points) || strings.TrimSpace(c.Scope) == "") {
			reasons["invalid_trend"]++
			return ""
		}
		if c.Type == "line" {
			for _, point := range c.Points {
				if point.Source != c.Points[0].Source {
					reasons["mixed_trend_sources"]++
					return ""
				}
			}
		}
		// A range is valid only when derived from ordered date labels. Each
		// evidence passage may then contain its own year rather than a range
		// that the original source never stated.
		rangeDates := dateRange.FindStringSubmatch(c.Period)
		if c.Type == "line" && len(rangeDates) == 0 {
			reasons["invalid_period"]++
			return ""
		}
		if len(rangeDates) > 0 && c.Type != "line" && !chronological(c.Points) {
			// Category charts can compare values aggregated over a whole period;
			// they must match the original full period rather than per-point dates.
			rangeDates = nil
		}
		if len(rangeDates) > 0 && (!chronological(c.Points) || rangeDates[1] != c.Points[0].Label || rangeDates[2] != c.Points[len(c.Points)-1].Label) {
			reasons["invalid_period"]++
			return ""
		}
		nc := normalizedChart(c)
		vc := normalizedChartKeepSpaces(c)
		for pointIndex := range c.Points {
			npoint := nc.Points[pointIndex]
			supported := false
			cite := regexp.MustCompile(`\[` + strconv.Itoa(npoint.Source) + `\]`)
			// Diagnose the closest paragraph from the cited source only. These
			// flags contain no labels, values, quotations or source content.
			bestScore := -1
			var missing []string
			for _, para := range paragraphs {
				if !cite.MatchString(para) {
					continue
				}
				npara := normalizeChartText(para)
				vpara := normalizeChartTextKeepSpaces(para)
				qualifier := nc.Qualifier
				marked := false
				mismatch := false
				for _, word := range []string{"预测", "預測", "预估", "預估", "估算", "估计", "估計", "假设", "假設", "情景", "forecast", "estimate", "projected", "projection", "scenario"} {
					if strings.Contains(npara, word) {
						marked = true
						if !strings.Contains(qualifier, word) {
							mismatch = true
						}
					}
				}
				// A mixed forecast/actual paragraph cannot safely establish which
				// category a co-occurring number belongs to.
				periodPresent := strings.Contains(npara, nc.Period) || (len(rangeDates) > 0 && strings.Contains(npara, npoint.Label))
				checks := []struct {
					name string
					ok   bool
				}{
					{"label", strings.Contains(npara, npoint.Label)},
					{"unit", strings.Contains(npara, nc.Unit)},
					{"period", periodPresent},
					{"title", c.Type != "line" || strings.Contains(npara, nc.Title)},
					{"scope", nc.Scope == "" || strings.Contains(npara, nc.Scope)},
					{"qualifier", nc.Qualifier == "" || strings.Contains(npara, nc.Qualifier)},
					{"value_binding", pointValueMatches(vpara, vc, vc.Points[pointIndex])},
					{"forecast_context", !mismatch && !(marked && (strings.Contains(npara, "实际") || strings.Contains(npara, "實際") || strings.Contains(npara, "actual")))},
				}
				var failed []string
				for _, check := range checks {
					if !check.ok {
						failed = append(failed, check.name)
					}
				}
				if len(failed) == 0 {
					supported = true
					break
				}
				if score := len(checks) - len(failed); score > bestScore {
					bestScore, missing = score, failed
				}
			}
			if !supported {
				reasons["missing_evidence"]++
				if bestScore < 0 {
					reasons["evidence_source"]++
				}
				for _, field := range missing {
					reasons["evidence_"+field]++
				}
				return ""
			}
		}
		return block
	})
	return filtered, reasons
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
