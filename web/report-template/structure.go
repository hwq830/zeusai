package reporttemplate

import (
	"encoding/json"
	"fmt"
	"html"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// A structure visual restates cited passages already present in the report; it
// does not infer causal links or introduce new facts.
type structureItem struct {
	Heading string `json:"heading"`
	Excerpt string `json:"excerpt"`
	Source  int    `json:"source"`
}
type structureVisual struct {
	Items []structureItem `json:"items"`
}

var structureBlock = regexp.MustCompile("(?ms)^```zeus-structure[ \\t]*\\r?\\n(.*?)\\r?\\n```[ \\t]*(?:\\r?\\n|$)")
var sectionHeading = regexp.MustCompile(`(?m)^##[ \t]+([^\n]+)$`)

// StructureFromReport builds a visual from cited, verbatim prose in distinct sections.
// Empty means that a meaningful supported summary could not be built.
func StructureFromReport(markdown string, sourceCount int) string {
	markdown = structureBlock.ReplaceAllString(WithoutCharts(markdown), "")
	sections := sectionHeading.FindAllStringSubmatchIndex(markdown, -1)
	var v structureVisual
	for i, m := range sections {
		heading := strings.TrimSpace(markdown[m[2]:m[3]])
		if heading == "" || utf8.RuneCountInString(heading) > 60 || strings.ContainsAny(heading, "`[]<>#") {
			continue
		}
		end := len(markdown)
		if i+1 < len(sections) {
			end = sections[i+1][0]
		}
		for _, p := range strings.Split(markdown[m[1]:end], "\n\n") {
			p = strings.TrimSpace(p)
			if p == "" || strings.ContainsAny(p, "\n|`<>#") || strings.HasPrefix(p, "-") || utf8.RuneCountInString(p) > 250 {
				continue
			}
			refs := citeRe.FindAllStringSubmatch(p, -1)
			if len(refs) != 1 {
				continue
			}
			n, err := strconv.Atoi(refs[0][1])
			if err != nil || n < 1 || n > sourceCount {
				continue
			}
			v.Items = append(v.Items, structureItem{Heading: heading, Excerpt: p, Source: n})
			break
		}
		if len(v.Items) == 4 {
			break
		}
	}
	if len(v.Items) < 2 {
		return ""
	}
	raw, _ := json.Marshal(v)
	return "```zeus-structure\n" + string(raw) + "\n```\n"
}

// renderStructure validates that every excerpt and heading still appears in the
// current report and that each cited source is available before rendering.
func renderStructure(raw, markdown, lang string, valid map[int]bool) string {
	var v structureVisual
	if len(raw) > 8192 || json.Unmarshal([]byte(raw), &v) != nil || len(v.Items) < 2 || len(v.Items) > 4 {
		return ""
	}
	plain := structureBlock.ReplaceAllString(markdown, "")
	var b strings.Builder
	title := "来源要点"
	if lang == "en" {
		title = "Cited highlights"
	} else if lang == "zh-Hant" {
		title = "來源要點"
	}
	fmt.Fprintf(&b, `<aside class="report-highlights"><strong>%s</strong><div class="structure-grid">`, html.EscapeString(title))
	for _, item := range v.Items {
		if !valid[item.Source] || !strings.Contains(plain, "## "+item.Heading) || !strings.Contains(plain, item.Excerpt) || !strings.Contains(item.Excerpt, fmt.Sprintf("[%d]", item.Source)) {
			return ""
		}
		excerpt := html.EscapeString(item.Excerpt)
		cite := fmt.Sprintf("[%d]", item.Source)
		excerpt = strings.Replace(excerpt, cite, fmt.Sprintf(`<sup class="cite"><a href="#src-%d">%d</a></sup>`, item.Source, item.Source), 1)
		fmt.Fprintf(&b, `<div class="structure-item"><strong>%s</strong><p>%s</p></div>`, html.EscapeString(item.Heading), excerpt)
	}
	b.WriteString(`</div></aside>`)
	return b.String()
}
