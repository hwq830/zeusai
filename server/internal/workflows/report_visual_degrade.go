package workflows

import (
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

const (
	visualDegradeNoSources    = "no_sources"
	visualDegradeInsufficient = "insufficient_comparable_data"
	visualDegradeGateNone     = "none"
)

var (
	errVisualDataPresent  = errors.New("visual data present but no chart produced")
	errVisualNoCandidates = errors.New("no visual candidates passed evidence validation")
	errVisualBodyMismatch = errors.New("chart candidates do not match report body")
)

// visualPlanError keeps the rejection category and the number of candidates
// that failed it, so the degrade decision can log both without depending on
// the error text.
type visualPlanError struct {
	err        error
	candidates int
}

func (e *visualPlanError) Error() string { return e.err.Error() }
func (e *visualPlanError) Unwrap() error { return e.err }

func visualPlanErrorCandidates(err error) int {
	var planErr *visualPlanError
	if errors.As(err, &planErr) {
		return planErr.candidates
	}
	return 0
}

func visualDegradeNote(p ReportPlan, reason string) string {
	if reason == visualDegradeNoSources {
		return p.Lang.Pick("视觉说明：没有可核验的来源，本次未生成图表。", "Visual note: no verifiable sources were available, so no chart was generated.")
	}
	return p.Lang.Pick("视觉说明：本主题在所给材料中没有可比较的同口径多期数据，因此未生成图表。以上来源要点为文字摘要，不是图表。", "Visual note: the provided material has no comparable multi-period series on the same scope, so no chart was generated. The highlights above are text excerpts, not charts.")
}

func normalizeDegradeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		if unicode.IsSpace(r) || r == ',' {
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

var (
	visualSeriesYearPattern   = regexp.MustCompile(`(?:19|20)[0-9]{2}`)
	visualSeriesNumberPattern = regexp.MustCompile(`([0-9]+(?:\.[0-9]+)?)(万亿元|亿元|亿|万元|万|%)`)
)

func visualSeriesClauses(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		return r == '\n' || r == '。' || r == '；' || r == ';' || r == '!' || r == '?'
	})
}

type visualSeriesObservation struct {
	year, value, unit string
	source            int
}

func visualSeriesObservations(text string, sourceIndex int) []visualSeriesObservation {
	var out []visualSeriesObservation
	for _, clause := range visualSeriesClauses(text) {
		clause = normalizeDegradeText(clause)
		if clause == "" {
			continue
		}
		years := visualSeriesYearPattern.FindAllStringIndex(clause, -1)
		for i, loc := range years {
			end := len(clause)
			if i+1 < len(years) {
				end = years[i+1][0]
			}
			rest := clause[loc[1]:end]
			for _, match := range visualSeriesNumberPattern.FindAllStringSubmatch(rest, -1) {
				out = append(out, visualSeriesObservation{year: clause[loc[0]:loc[1]], value: match[1], unit: match[2], source: sourceIndex})
			}
		}
	}
	return out
}

// visualPotentialSeries is a conservative gate that only blocks a degrade. It
// is intentionally loose: a missed series allows a visible degrade, which QA
// can audit; a false positive only keeps the current fail-and-fix behavior.
func visualPotentialSeries(findings []QuestionNotes, body string) (bool, string) {
	bodyNorm := normalizeDegradeText(body)
	if bodyNorm == "" {
		return false, visualDegradeGateNone
	}
	type observationKey struct {
		year, value, unit string
		source            int
	}
	seen := map[observationKey]bool{}
	byUnit := map[string][]visualSeriesObservation{}
	// buildReportEvidence uses the stable URL order, matching the report's
	// citation numbers. Walk its sources so observations carry that index.
	for _, source := range buildReportEvidence(findings).Sources {
		for _, excerpt := range source.Excerpts {
			for _, observation := range visualSeriesObservations(excerpt, source.Index) {
				key := observationKey{year: observation.year, value: observation.value, unit: observation.unit, source: observation.source}
				if seen[key] {
					continue
				}
				seen[key] = true
				byUnit[observation.unit] = append(byUnit[observation.unit], observation)
			}
		}
	}
	units := make([]string, 0, len(byUnit))
	for unit := range byUnit {
		units = append(units, unit)
	}
	sort.Strings(units)
	contains := func(observation visualSeriesObservation) bool {
		return strings.Contains(bodyNorm, observation.year) &&
			strings.Contains(bodyNorm, observation.value) &&
			strings.Contains(bodyNorm, "["+strconv.Itoa(observation.source)+"]")
	}
	for _, unit := range units {
		observations := byUnit[unit]
		// Annual/cross-source series: two different years, each present in the
		// body with its own value and citation.
		for i := 0; i < len(observations); i++ {
			for j := i + 1; j < len(observations); j++ {
				left, right := observations[i], observations[j]
				if left.year == right.year || !contains(left) || !contains(right) {
					continue
				}
				years := []string{left.year, right.year}
				sort.Strings(years)
				sources := []int{left.source, right.source}
				sort.Ints(sources)
				sourceLabel := fmt.Sprintf("[%d,%d]", sources[0], sources[1])
				if sources[0] == sources[1] {
					sourceLabel = fmt.Sprintf("[%d]", sources[0])
				}
				return true, clip(fmt.Sprintf("%s@%s:%s", unit, sourceLabel, strings.Join(years, ",")), 120)
			}
		}
		// Single-period category chart: at least two distinct values from the
		// source that also appear in the body with the same citation.
		matched := 0
		var sources []int
		var years []string
		for _, observation := range observations {
			if contains(observation) {
				matched++
				sources = append(sources, observation.source)
				years = append(years, observation.year)
			}
		}
		if matched >= 2 {
			sort.Ints(sources)
			sort.Strings(years)
			return true, clip(fmt.Sprintf("%s@[%d]:%s", unit, sources[0], strings.Join(years, ",")), 120)
		}
	}
	return false, visualDegradeGateNone
}

func verifyVisualDegradeNote(markdown, note string) error {
	if strings.TrimSpace(note) == "" || !strings.Contains(markdown, note) {
		return errors.New("visual degrade note missing from report")
	}
	return nil
}

func appendVisualDegrade(p ReportPlan, d reportDraft, findings []QuestionNotes, sourceCount int, reason string, candidates, attempts int) (reportDraft, error) {
	if potential, evidence := visualPotentialSeries(findings, d.Markdown); potential {
		slog.Info("report visual degrade blocked", "task_id", p.TaskID, "gate_evidence", evidence, "candidates", candidates, "planning_attempts", attempts)
		return reportDraft{}, errVisualDataPresent
	}
	note := visualDegradeNote(p, reason)
	structure := reporttemplate.StructureFromReport(d.Markdown, sourceCount)
	if structure != "" {
		d.Markdown += "\n\n" + structure
	}
	d.Markdown += "\n\n" + note
	if err := verifyVisualDegradeNote(d.Markdown, note); err != nil {
		return reportDraft{}, err
	}
	slog.Info("report visual degraded", "task_id", p.TaskID, "reason", reason, "candidates", candidates, "planning_attempts", attempts, "gate_evidence", visualDegradeGateNone, "explanation", clip(note, 120))
	return d, nil
}
