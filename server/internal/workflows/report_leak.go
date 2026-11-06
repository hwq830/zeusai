package workflows

import (
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	reviewLeakHeading = regexp.MustCompile(`^ {0,3}(#{1,3})[ \t]*(审查说明|自检说明|审查意见|内部审查|审核说明|内部自检|质量自检)[ \t]*$`)
	reviewLeakList    = regexp.MustCompile(`^\s*[-*]\s*(high|medium|low)\s*[:：]`)
	mdHeading         = regexp.MustCompile(`^ {0,3}(#{1,6})[ \t]+`)
	reviewLeakPhrases = []struct {
		reason string
		text   string
	}{
		{"pending_verification", "待核实问题"},
		{"not_source_verification", "这不是对原始来源的事实核验"},
		{"review_found", "审查发现以下"},
	}
)

type reviewLeakSection struct {
	start, end int // line indexes, end exclusive
	level      int
	reason     string
}

// reviewLeakReason reports the first leak feature in a section body.
func reviewLeakReason(lines []string, start, end int) string {
	for _, line := range lines[start:end] {
		if reviewLeakList.MatchString(line) {
			return "review_severity_list"
		}
	}
	for _, line := range lines[start:end] {
		for _, phrase := range reviewLeakPhrases {
			if strings.Contains(line, phrase.text) {
				return phrase.reason
			}
		}
	}
	return ""
}

// findReviewLeaks returns only sections where both the sensitive heading and a
// content feature are present. A heading alone is normal prose and is kept.
func findReviewLeaks(markdown string) []reviewLeakSection {
	lines := strings.SplitAfter(markdown, "\n")
	var out []reviewLeakSection
	for i := 0; i < len(lines); i++ {
		heading := reviewLeakHeading.FindStringSubmatch(strings.TrimRight(lines[i], "\r\n"))
		if heading == nil {
			continue
		}
		level := len(heading[1])
		end := len(lines)
		for j := i + 1; j < len(lines); j++ {
			if m := mdHeading.FindStringSubmatch(strings.TrimRight(lines[j], "\r\n")); m != nil && len(m[1]) <= level {
				end = j
				break
			}
		}
		if reason := reviewLeakReason(lines, i+1, end); reason != "" {
			out = append(out, reviewLeakSection{start: i, end: end, level: level, reason: reason})
			i = end - 1
		}
	}
	return out
}

// stripReviewLeaks removes every confirmed leak section. It returns the input
// byte-for-byte when no section qualifies.
func stripReviewLeaks(markdown string) (string, int, string) {
	leaks := findReviewLeaks(markdown)
	if len(leaks) == 0 {
		return markdown, 0, ""
	}
	lines := strings.SplitAfter(markdown, "\n")
	var b strings.Builder
	removed := 0
	reason := leaks[0].reason
	last := 0
	for _, leak := range leaks {
		for _, line := range lines[last:leak.start] {
			b.WriteString(line)
		}
		for _, line := range lines[leak.start:leak.end] {
			removed += utf8.RuneCountInString(line)
		}
		last = leak.end
	}
	for _, line := range lines[last:] {
		b.WriteString(line)
	}
	return b.String(), removed, reason
}

func reviewLeakPresent(markdown string) (string, bool) {
	leaks := findReviewLeaks(markdown)
	if len(leaks) == 0 {
		return "", false
	}
	return leaks[0].reason, true
}
