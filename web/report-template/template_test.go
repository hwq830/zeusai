package reporttemplate

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	out, err := Render(Data{
		Title: "标题 <x>", Summary: "摘要", Markdown: "## 一\n\n事实 [1]，无效引用 [9]。\n\n<script>alert(1)</script>\n",
		Sources: []Source{{Index: 1, URL: "https://a.example/1", Title: "来源一"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	html := string(out)
	for _, want := range []string{"<h1>标题 &lt;x&gt;</h1>", `<a href="#src-1">1</a>`, "[9]", `id="src-1"`} {
		if !strings.Contains(html, want) {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(html, "<script>alert") {
		t.Error("raw HTML from markdown must not be rendered")
	}
}
