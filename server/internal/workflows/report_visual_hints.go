package workflows

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// These are individual candidate facts, not evidence of comparable methodology.
// No facts are grouped into a chart or declared comparable by this extractor.
var annualHintPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4})年[，,:： \t]*([\p{Han}]{3,22}?)[ \t]*(0|[1-9][0-9]*)(?:\.([0-9]+))?[ \t]*(万亿元|萬億元|亿元|億元|万亿|萬億|亿人次|億人次|万人次|萬人次|万吨|萬噸|万元|萬元|万人|萬人|人次|亿|億|万|萬|元|吨|噸|%|％)`)

var uncertainAnnualHint = []string{"预测", "預測", "预估", "預估", "预计", "預計", "估算", "估计", "估計", "推算", "假设", "假設", "情景", "上半年", "下半年", "季度", "首季", "月份", "月度", "假期", "节假日", "節假日", "春节", "春節", "约", "約"}

type annualHintKey struct{ year, metric, value, unit string }
type annualHintFact struct {
	key    annualHintKey
	source int
}
type visualCandidateHint struct {
	Year          string      `json:"year"`
	Metric        string      `json:"metric"`
	Value         json.Number `json:"value"`
	Unit          string      `json:"unit"`
	Source        int         `json:"source"`
	SourceContext string      `json:"source_context"`
	ReportContext string      `json:"report_context"`
}
type annualHintObservation struct {
	key      annualHintKey
	rawValue string
}

func hintNumber(integer, fraction string) string {
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return integer
	}
	return integer + "." + fraction
}

func annualHintFacts(clause string) []annualHintObservation {
	for _, marker := range uncertainAnnualHint {
		if strings.Contains(clause, marker) {
			return nil
		}
	}
	var out []annualHintObservation
	for _, m := range annualHintPattern.FindAllStringSubmatch(clause, -1) {
		rawValue := m[3]
		if m[4] != "" {
			rawValue += "." + m[4]
		}
		out = append(out, annualHintObservation{key: annualHintKey{year: m[1], metric: m[2], value: hintNumber(m[3], m[4]), unit: m[5]}, rawValue: rawValue})
	}
	return out
}

func hintClauses(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == '\n' || r == '。' || r == '；' || r == ';' })
}

// visualCandidateHints emits only single observations that occur in an actual
// excerpt and a report sentence citing that same source. The untouched source
// and report context are included so the model can check scope independently.
// It never assembles values into a chart or equates the scope of two sources.
func visualCandidateHints(findings []QuestionNotes, markdown string, sourceCount int) []visualCandidateHint {
	sourceIndex := map[string]int{}
	type sourceObservation struct {
		value, context string
	}
	sourceFacts := map[annualHintFact][]sourceObservation{}
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
			if !ok || utf8.RuneCountInString(excerpt.Text) > 1200 {
				continue
			}
			for _, clause := range hintClauses(excerpt.Text) {
				if utf8.RuneCountInString(clause) > 800 {
					continue
				}
				for _, obs := range annualHintFacts(clause) {
					fact := annualHintFact{key: obs.key, source: idx}
					sourceFacts[fact] = append(sourceFacts[fact], sourceObservation{value: obs.rawValue, context: excerpt.Text})
				}
			}
		}
	}
	var hints []visualCandidateHint
	seen := map[visualCandidateHint]bool{}
	for _, paragraph := range strings.Split(strings.ReplaceAll(markdown, "\r\n", "\n"), "\n\n") {
		if utf8.RuneCountInString(paragraph) > 1600 {
			continue
		}
		for _, clause := range hintClauses(paragraph) {
			refs := map[int]bool{}
			for _, cite := range citeRe.FindAllStringSubmatch(clause, -1) {
				idx, _ := strconv.Atoi(cite[1])
				refs[idx] = true
			}
			for _, obs := range annualHintFacts(clause) {
				for idx := 1; idx <= sourceCount; idx++ {
					if !refs[idx] {
						continue
					}
					for _, source := range sourceFacts[annualHintFact{key: obs.key, source: idx}] {
						hint := visualCandidateHint{Year: obs.key.year, Metric: obs.key.metric, Value: json.Number(source.value), Unit: obs.key.unit, Source: idx, SourceContext: source.context, ReportContext: paragraph}
						if !seen[hint] {
							seen[hint] = true
							hints = append(hints, hint)
							if len(hints) == 12 {
								return hints
							}
						}
					}
				}
			}
		}
	}
	return hints
}
