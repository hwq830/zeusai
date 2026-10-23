// Package reporttemplate is the single HTML template for reports: the App's WebView, the H5
// share page and (later) the PDF export all render through Render.
package reporttemplate

import (
	"bytes"
	_ "embed"
	"html/template"
	"regexp"
	"strconv"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
)

//go:embed report.html.tmpl
var src string

var page = template.Must(template.New("report").Parse(src))

var md = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough))

type Source struct {
	Index    int
	URL      string
	Title    string
	SiteName string
}

type Data struct {
	Title       string
	Summary     string
	Markdown    string // empty for summary-only shares
	DepthLabel  string
	SourceCount int
	CompletedAt string
	Sources     []Source
	Share       bool
	AppURL      string
}

// citeRe matches [n] citations; goldmark leaves unmatched [n] as literal text.
var citeRe = regexp.MustCompile(`\[(\d{1,3})\]`)

// Render produces the full HTML document. Raw HTML in the Markdown is not rendered
// (goldmark's default), so model output cannot inject markup.
func Render(d Data) ([]byte, error) {
	var body bytes.Buffer
	if d.Markdown != "" {
		if err := md.Convert([]byte(d.Markdown), &body); err != nil {
			return nil, err
		}
	}
	valid := map[int]bool{}
	for _, s := range d.Sources {
		valid[s.Index] = true
	}
	linked := citeRe.ReplaceAllFunc(body.Bytes(), func(m []byte) []byte {
		n, _ := strconv.Atoi(string(m[1 : len(m)-1]))
		if !valid[n] {
			return m
		}
		return []byte(`<sup class="cite"><a href="#src-` + strconv.Itoa(n) + `">` + strconv.Itoa(n) + `</a></sup>`)
	})
	var out bytes.Buffer
	err := page.Execute(&out, struct {
		Data
		Body template.HTML
	}{d, template.HTML(linked)}) // #nosec G203 -- goldmark output with raw HTML disabled
	return out.Bytes(), err
}
