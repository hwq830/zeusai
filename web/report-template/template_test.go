package reporttemplate

import (
	"strings"
	"testing"
)

func TestRender(t *testing.T) {
	out, err := Render(Data{
		Title: "标题 <x>", Summary: "摘要", Markdown: "## 一\n\n事实 [1]，无效引用 [9]。\n\n<script>alert(1)</script>\n",
		Sources: []Source{{Index: 1, URL: "https://a.example/1", Title: "来源一"}, {Index: 2, URL: "mcp://amap/place_search", Title: "高德地图：place_search"}},
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
	if !strings.Contains(html, `<li id="src-2">高德地图：place_search`) || strings.Contains(html, "ZgotmplZ") {
		t.Error("connector sources must be listed without a link")
	}
	if strings.Contains(html, "<script>alert") {
		t.Error("raw HTML from markdown must not be rendered")
	}
}
