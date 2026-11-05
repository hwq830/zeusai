package workflows

import (
	"regexp"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

var sourceNavigation = regexp.MustCompile(`(?i)^(?:首页|首頁|返回首页|返回首頁|网站导航|網站導航|home)(?:\s*[|>›]|\s*$)|^skip to content\s*$`)
var sourceFileName = regexp.MustCompile(`(?i)^[^。！？!?；;|]{1,200}\.(?:docx?|xlsx?|pdf)(?:\s+(?:vip|下载|下載))*\s*$`)

func navigationMenu(s string) bool {
	parts := strings.Split(s, "|")
	if len(parts) < 3 {
		return false
	}
	labels := map[string]bool{"首页": true, "首頁": true, "登录": true, "登錄": true, "注册": true, "註冊": true, "搜索": true, "帮助": true, "幫助": true, "产品导航": true, "產品導航": true, "收藏": true, "分享": true, "home": true, "login": true, "search": true, "help": true}
	matches := 0
	for _, part := range parts {
		if labels[strings.ToLower(strings.TrimSpace(part))] {
			matches++
		}
	}
	return matches >= 2 && matches*2 >= len(parts)
}

// Keep the numeric chart budget independent of the general source budget.
// Interleave general excerpts so one source cannot consume all eight slots.
func collectResearchExcerpts(full []string, readOK []bool, query string) ([]NumericExcerpt, []SourceExcerpt) {
	var numeric []NumericExcerpt
	generalBySource := make([][]SourceExcerpt, len(full))
	for i, text := range full {
		if text == "" || i >= len(readOK) || !readOK[i] {
			continue
		}
		clipped := utf8.RuneCountInString(text) > 40000
		page := clip(text, 40000)
		if len(numeric) < 8 {
			numeric = append(numeric, numericSourceExcerptsForQuery(page, i+1, clipped, query)...)
			if len(numeric) > 8 {
				numeric = numeric[:8]
			}
		}
		generalBySource[i] = sourceExcerptsForQuery(page, i+1, clipped, query)
	}
	var general []SourceExcerpt
	total := 0
	for round := 0; round < 2; round++ {
		for _, excerpts := range generalBySource {
			if round >= len(excerpts) || len(general) >= 8 {
				continue
			}
			excerpt := excerpts[round]
			n := utf8.RuneCountInString(excerpt.Text)
			if total+n <= 8000 {
				general = append(general, excerpt)
				total += n
			}
		}
	}
	return numeric, general
}

// sourceExcerptsForQuery chooses at most two complete, contiguous pieces of
// extracted page text. It never uses snippets or model-generated notes.
func sourceExcerptsForQuery(text string, source int, clipped bool, query string) []SourceExcerpt {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var candidates []SourceExcerpt
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		n := utf8.RuneCountInString(s)
		if n < 12 || n > 1200 || navigationMenu(s) || sourceNavigation.MatchString(s) || sourceFileName.MatchString(s) || documentNavigationPattern.MatchString(s) || registrationNumberPattern.MatchString(s) {
			return
		}
		if seen[s] {
			return
		}
		seen[s] = true
		candidates = append(candidates, SourceExcerpt{Source: source, Text: s})
	}
	for lineIndex, line := range lines {
		lastClippedLine := clipped && lineIndex == len(lines)-1
		if utf8.RuneCountInString(line) <= 1200 && !lastClippedLine {
			add(line)
			continue
		}
		// Long lines are split only at sentence boundaries; an unfinished tail
		// is discarded rather than treated as a complete statement.
		runes := []rune(line)
		start := 0
		for i, r := range runes {
			end := strings.ContainsRune("。！？!?；;", r) || (r == '.' && i+1 < len(runes) && unicode.IsSpace(runes[i+1]))
			if !end {
				continue
			}
			add(string(runes[start : i+1]))
			start = i + 1
		}
	}
	terms := map[string]bool{}
	for _, token := range excerptQueryTerms.FindAllString(strings.ToLower(query), 30) {
		r := []rune(token)
		if len(r) > 0 && unicode.Is(unicode.Han, r[0]) {
			for i := 0; i+1 < len(r) && i < 20; i++ {
				terms[string(r[i:i+2])] = true
			}
		} else {
			terms[token] = true
		}
	}
	score := func(s string) int {
		s = strings.ToLower(s)
		n := 0
		for term := range terms {
			if strings.Contains(s, term) {
				n++
			}
		}
		return n
	}
	sort.SliceStable(candidates, func(i, j int) bool { return score(candidates[i].Text) > score(candidates[j].Text) })
	if len(candidates) > 2 {
		candidates = candidates[:2]
	}
	return candidates
}

type reportEvidenceQuestion struct {
	Question string `json:"question"`
	Sources  []int  `json:"sources"`
}

type reportEvidenceSource struct {
	Index      int      `json:"index"`
	URL        string   `json:"url"`
	Title      string   `json:"title,omitempty"`
	Site       string   `json:"site,omitempty"`
	ReadStatus string   `json:"readStatus"`
	HasExcerpt bool     `json:"hasExcerpt"`
	Excerpts   []string `json:"excerpts"`
	Gap        string   `json:"gap,omitempty"`
}

type reportEvidence struct {
	Questions []reportEvidenceQuestion `json:"questions"`
	Sources   []reportEvidenceSource   `json:"sources"`
	Notice    string                   `json:"notice"`
}

// buildReportEvidence uses the same first-seen URL numbering as mergeNotes,
// while omitting generated notes, snippets and unverified search timestamps.
func buildReportEvidence(findings []QuestionNotes) reportEvidence {
	out := reportEvidence{Questions: []reportEvidenceQuestion{}, Sources: []reportEvidenceSource{}, Notice: "原文均为部分摘录；无摘录不代表原网页或其他材料没有相关内容。来源名称和读取状态不证明具体事实。"}
	index := map[string]int{}
	priority := map[string]int{"unknown": 0, "not_attempted": 1, "failed": 2, "read": 3}
	for _, f := range findings {
		q := reportEvidenceQuestion{Question: f.Question, Sources: []int{}}
		local := map[int]int{}
		for i, source := range f.Sources {
			idx, ok := index[source.URL]
			if !ok {
				idx = len(out.Sources)
				index[source.URL] = idx
				out.Sources = append(out.Sources, reportEvidenceSource{Index: idx + 1, URL: source.URL, Title: source.Title, Site: source.SiteName, ReadStatus: "unknown", Excerpts: []string{}})
			}
			local[i+1] = idx
			q.Sources = append(q.Sources, idx+1)
			status := "unknown"
			if i < len(f.SourceReadStatus) {
				if _, ok := priority[f.SourceReadStatus[i]]; ok {
					status = f.SourceReadStatus[i]
				}
			}
			if priority[status] > priority[out.Sources[idx].ReadStatus] {
				out.Sources[idx].ReadStatus = status
			}
			if out.Sources[idx].Title == "" {
				out.Sources[idx].Title = source.Title
			}
			if out.Sources[idx].Site == "" {
				out.Sources[idx].Site = source.SiteName
			}
		}
		add := func(source int, text string) {
			idx, ok := local[source]
			text = strings.TrimSpace(text)
			if !ok || text == "" {
				return
			}
			s := &out.Sources[idx]
			for i, existing := range s.Excerpts {
				if strings.Contains(existing, text) {
					return
				}
				if strings.Contains(text, existing) {
					s.Excerpts[i] = text
					return
				}
			}
			s.Excerpts = append(s.Excerpts, text)
			s.HasExcerpt = true
		}
		for _, excerpt := range f.SourceExcerpts {
			add(excerpt.Source, excerpt.Text)
		}
		for _, excerpt := range f.NumericExcerpts {
			add(excerpt.Source, excerpt.Text)
		}
		out.Questions = append(out.Questions, q)
	}
	for i := range out.Sources {
		s := &out.Sources[i]
		if s.HasExcerpt {
			continue
		}
		switch s.ReadStatus {
		case "read":
			s.Gap = "已读取页面，但没有保留可用原文摘录。"
		case "failed":
			s.Gap = "未能读取页面，缺少原文摘录。"
		case "not_attempted":
			s.Gap = "尚未读取页面，缺少原文摘录。"
		default:
			s.Gap = "读取情况未知，缺少原文摘录。"
		}
	}
	return out
}
