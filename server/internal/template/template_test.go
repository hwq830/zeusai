package template

import (
	"strings"
	"testing"
)

func TestExpand(t *testing.T) {
	vals := map[string]any{"companies": []any{"宁德时代", "比亚迪"}, "n": float64(3), "empty": ""}
	if got := Expand("{{companies}} 的新闻，前 {{ n }} 条{{empty}}{{missing}}", vals); got != "宁德时代、比亚迪 的新闻，前 3 条" {
		t.Fatalf("got %q", got)
	}
}

func TestValidateFillsDefaults(t *testing.T) {
	in := Input{Name: "周报", Sections: []Section{{Title: "动态"}}, Fields: []Field{{Key: "repos", Label: "项目", Type: "tags"}}}
	if err := in.Validate(); err != nil {
		t.Fatal(err)
	}
	if in.Sections[0].Key != "s1" || in.Sections[0].MaxItems != 5 || in.DefaultLength != "standard" {
		t.Fatalf("defaults not applied: %+v", in)
	}
	bad := []Input{
		{Name: "", Sections: in.Sections},
		{Name: "x"},
		{Name: "x", Sections: in.Sections, Fields: []Field{{Key: "Bad Key", Label: "l", Type: "text"}}},
		{Name: "x", Sections: in.Sections, Fields: []Field{{Key: "a", Label: "l", Type: "select"}}},
		{Name: strings.Repeat("长", 41), Sections: in.Sections},
	}
	for i, b := range bad {
		if b.Validate() == nil {
			t.Errorf("case %d should be invalid", i)
		}
	}
}
