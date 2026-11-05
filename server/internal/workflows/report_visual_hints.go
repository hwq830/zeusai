package workflows

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

// These are individual candidate facts, not evidence of comparable methodology.
// No facts are grouped into a chart or declared comparable by this extractor.
var annualHintPattern = regexp.MustCompile(`(?:^|[^0-9])([0-9]{4})年[，,:：]?([\p{Han}A-Za-z0-9]{3,22}?)(0|[1-9][0-9]*)(?:\.([0-9]+))?(万亿元|萬億元|亿元|億元|万亿|萬億|亿人次|億人次|万人次|萬人次|万吨|萬噸|万元|萬元|万人|萬人|人次|亿|億|万|萬|元|吨|噸|%)`)

// Period markers invalidate an annual observation. Qualifier markers do not:
// an approximate or forecast value is still a usable candidate when the chart
// keeps that qualifier, so it is surfaced instead of dropped.
var nonAnnualHint = []string{"上半年", "下半年", "季度", "首季", "月份", "月度", "假期", "节假日", "節假日", "春节", "春節"}

var qualifierHint = []string{"预测", "預測", "预估", "預估", "预计", "預計", "估算", "估计", "估計", "推算", "假设", "假設", "情景", "约", "約"}

type annualHintKey struct{ year, metricKey, value, unit string }
type annualHintFact struct {
	key    annualHintKey
	source int
}
type visualCandidateHint struct {
	Year          string      `json:"year"`
	Metric        string      `json:"metric"`
	Value         json.Number `json:"value"`
	Unit          string      `json:"unit"`
	Qualifier     string      `json:"qualifier,omitempty"`
	Source        int         `json:"source"`
	SourceContext string      `json:"source_context"`
	ReportContext string      `json:"report_context"`
}
type annualHintObservation struct {
	key       annualHintKey
	rawValue  string
	metric    string
	qualifier string
}

func hintNumber(integer, fraction string) string {
	fraction = strings.TrimRight(fraction, "0")
	if fraction == "" {
		return integer
	}
	return integer + "." + fraction
}

// normalizeVisualText removes only presentation differences: whitespace,
// full-width digits/letters and the full-width percent sign. It does not
// expand synonyms, abbreviations or metric names.
func normalizeVisualText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) {
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
		b.WriteRune(unicode.ToLower(r))
	}
	return b.String()
}

// hintMetricKey compares the metric words only. Latin letters are dropped so a
// scraper's "Al" and the report's "AI" do not block an otherwise matching fact;
// a differently named metric still differs and is not paired.
func hintMetricKey(s string) string {
	s = normalizeVisualText(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.Is(unicode.Han, r) || unicode.IsDigit(r) {
			b.WriteRune(r)
		}
	}
	s = b.String()
	for _, prefix := range []string{"预计", "预测", "预估", "估算", "估计", "约", "将突破", "突破", "达到", "达"} {
		s = strings.TrimPrefix(s, prefix)
	}
	for _, suffix := range []string{"约为", "约", "预计", "预测", "预估", "估算", "估计", "将突破", "突破", "达到", "达", "为"} {
		s = strings.TrimSuffix(s, suffix)
	}
	return s
}

func hintQualifier(clause string, start, end int) string {
	lo := start - 16
	if lo < 0 {
		lo = 0
	}
	hi := end
	if hi > len(clause) {
		hi = len(clause)
	}
	window := clause[lo:hi]
	best, bestDistance := "", int(^uint(0)>>1)
	for _, marker := range qualifierHint {
		i := strings.Index(window, marker)
		if i < 0 {
			continue
		}
		distance := lo + i - start
		if distance < 0 {
			distance = -distance
		}
		if distance < bestDistance {
			best, bestDistance = marker, distance
		}
	}
	return best
}

func annualHintFacts(clause string) []annualHintObservation {
	clause = normalizeVisualText(clause)
	if clause == "" {
		return nil
	}
	for _, marker := range nonAnnualHint {
		if strings.Contains(clause, marker) {
			return nil
		}
	}
	matches := annualHintPattern.FindAllStringSubmatchIndex(clause, -1)
	out := make([]annualHintObservation, 0, len(matches))
	for _, m := range matches {
		integer := clause[m[6]:m[7]]
		fraction := ""
		if m[8] >= 0 {
			fraction = clause[m[8]:m[9]]
		}
		rawValue := integer
		if fraction != "" {
			rawValue += "." + fraction
		}
		metric := clause[m[4]:m[5]]
		out = append(out, annualHintObservation{
			key:       annualHintKey{year: clause[m[2]:m[3]], metricKey: hintMetricKey(metric), value: hintNumber(integer, fraction), unit: clause[m[10]:m[11]]},
			rawValue:  rawValue,
			metric:    metric,
			qualifier: hintQualifier(clause, m[2], m[11]),
		})
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
	type sourceObservation struct {
		value, context, metric, qualifier string
	}
	sourceFacts := map[annualHintFact][]sourceObservation{}
	for _, source := range buildReportEvidence(findings).Sources {
		for _, excerpt := range source.Excerpts {
			if utf8.RuneCountInString(excerpt) > 1200 {
				continue
			}
			for _, clause := range hintClauses(excerpt) {
				if utf8.RuneCountInString(clause) > 800 {
					continue
				}
				for _, obs := range annualHintFacts(clause) {
					fact := annualHintFact{key: obs.key, source: source.Index}
					sourceFacts[fact] = append(sourceFacts[fact], sourceObservation{value: obs.rawValue, context: excerpt, metric: obs.metric, qualifier: obs.qualifier})
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
						hint := visualCandidateHint{Year: obs.key.year, Metric: source.metric, Value: json.Number(source.value), Unit: obs.key.unit, Qualifier: source.qualifier, Source: idx, SourceContext: source.context, ReportContext: paragraph}
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
