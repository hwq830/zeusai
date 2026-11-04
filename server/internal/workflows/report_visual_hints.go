package workflows

import (
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// These are candidate hints only, not evidence of comparable methodology. A
// model must still check the original context; every returned chart still goes
// through the normal source/body checks and independent review.
var annualHintPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4})年[，,:： \t]*([\p{Han}]{3,22}?)[ \t]*(0|[1-9][0-9]*)(?:\.([0-9]+))?[ \t]*(万亿元|萬億元|亿元|億元|万亿|萬億|亿人次|億人次|万人次|萬人次|万吨|萬噸|万元|萬元|万人|萬人|人次|亿|億|万|萬|元|吨|噸|%|％)`)

var uncertainAnnualHint = []string{"预测", "預測", "预估", "預估", "预计", "預計", "估算", "估计", "估計", "推算", "假设", "假設", "情景", "上半年", "下半年", "季度", "首季", "月份", "月度", "假期", "节假日", "節假日", "春节", "春節", "约", "約"}

type annualHintKey struct{ year, metric, value, unit string }
type annualHintFact struct {
	key    annualHintKey
	source int
}

func hintNumber(integer, fraction string) string {
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return integer
	}
	return integer + "." + fraction
}

func annualHintFacts(clause string) []annualHintKey {
	for _, marker := range uncertainAnnualHint {
		if strings.Contains(clause, marker) {
			return nil
		}
	}
	var out []annualHintKey
	for _, m := range annualHintPattern.FindAllStringSubmatch(clause, -1) {
		out = append(out, annualHintKey{year: m[1], metric: m[2], value: hintNumber(m[3], m[4]), unit: m[5]})
	}
	return out
}

func hintClauses(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '。' || r == '；' || r == ';' })
}

// visualCandidateHints identifies narrow annual data pairs appearing in both
// the source and a report sentence citing that exact source. It does not make
// cross-year context or semantic compatibility decisions on the model's behalf.
func visualCandidateHints(findings []QuestionNotes, markdown, evidence string, sourceCount int) []reporttemplate.Chart {
	sourceIndex := map[string]int{}
	type yearMetric struct{ year, metric, unit string }
	values := map[yearMetric]map[string]bool{}
	var sourceFacts []annualHintFact
	for _, finding := range findings {
		local := map[int]int{}
		for i, source := range finding.Sources {
			idx, ok := sourceIndex[source.URL]
			if !ok {
				idx = len(sourceIndex) + 1
				sourceIndex[source.URL] = idx
			}
			local[i+1] = idx
		}
		for _, excerpt := range finding.NumericExcerpts {
			idx, ok := local[excerpt.Source]
			if !ok {
				continue
			}
			for _, clause := range hintClauses(excerpt.Text) {
				for _, fact := range annualHintFacts(clause) {
					sourceFacts = append(sourceFacts, annualHintFact{key: fact, source: idx})
					key := yearMetric{fact.year, fact.metric, fact.unit}
					if values[key] == nil {
						values[key] = map[string]bool{}
					}
					values[key][fact.value] = true
				}
			}
		}
	}
	sourceSet := map[annualHintFact]bool{}
	for _, fact := range sourceFacts {
		if len(values[yearMetric{fact.key.year, fact.key.metric, fact.key.unit}]) == 1 {
			sourceSet[fact] = true
		}
	}
	type metricUnit struct{ metric, unit string }
	groups := map[metricUnit]map[string]annualHintFact{}
	for _, clause := range hintClauses(markdown) {
		refs := map[int]bool{}
		for _, cite := range citeRe.FindAllStringSubmatch(clause, -1) {
			idx, _ := strconv.Atoi(cite[1])
			refs[idx] = true
		}
		for _, fact := range annualHintFacts(clause) {
			for idx := 1; idx <= sourceCount; idx++ {
				candidate := annualHintFact{key: fact, source: idx}
				if !refs[idx] || !sourceSet[candidate] {
					continue
				}
				group := metricUnit{fact.metric, fact.unit}
				if groups[group] == nil {
					groups[group] = map[string]annualHintFact{}
				}
				if _, exists := groups[group][fact.year]; !exists {
					groups[group][fact.year] = candidate
				}
			}
		}
	}
	keys := make([]metricUnit, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].metric != keys[j].metric {
			return keys[i].metric < keys[j].metric
		}
		return keys[i].unit < keys[j].unit
	})
	var hints []reporttemplate.Chart
	for _, key := range keys {
		years := make([]string, 0, len(groups[key]))
		for year := range groups[key] {
			years = append(years, year)
		}
		sort.Strings(years)
		var points []reporttemplate.ChartPoint
		for _, year := range years {
			if len(points) > 0 {
				prev, _ := strconv.Atoi(points[len(points)-1].Label)
				current, _ := strconv.Atoi(year)
				if current != prev+1 {
					break
				}
			}
			fact := groups[key][year]
			points = append(points, reporttemplate.ChartPoint{Label: year, Value: json.Number(fact.key.value), Source: fact.source})
			if len(points) == 8 {
				break
			}
		}
		if len(points) < 2 {
			continue
		}
		chart := reporttemplate.Chart{Type: "bar", Title: key.metric, Scope: key.metric, Unit: key.unit, Period: points[0].Label + "—" + points[len(points)-1].Label, Points: points}
		raw, err := json.Marshal(chart)
		if err != nil {
			continue
		}
		block := fmt.Sprintf("```zeus-chart\n%s\n```\n", raw)
		if reporttemplate.FilterCharts(block, evidence, sourceCount) == "" || reporttemplate.FilterCharts(block, markdown, sourceCount) == "" {
			continue
		}
		hints = append(hints, chart)
		if len(hints) == 3 {
			break
		}
	}
	return hints
}
