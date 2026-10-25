package template

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/hwq830/zeusai/prompts"
	"github.com/hwq830/zeusai/server/internal/i18n"
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

// Every official template ships an English translation that covers all of its texts.
func TestOfficialTranslations(t *testing.T) {
	files, err := fs.Glob(prompts.FS, "briefing-templates/*.yaml")
	if err != nil || len(files) == 0 {
		t.Fatal(files, err)
	}
	han := regexp.MustCompile(`\p{Han}`)
	for _, f := range files {
		b, _ := prompts.FS.ReadFile(f)
		var sf seedFile
		if err := yaml.Unmarshal(b, &sf); err != nil {
			t.Fatal(f, err)
		}
		if err := ValidateTranslations(sf.Translations); err != nil {
			t.Fatal(f, err)
		}
		tr := sf.Translations["en"]
		for k := range tr.Fields {
			if !slices.ContainsFunc(sf.Fields, func(x Field) bool { return x.Key == k }) {
				t.Errorf("%s: translation for unknown field %q", f, k)
			}
		}
		for k := range tr.Sections {
			if !slices.ContainsFunc(sf.Sections, func(x Section) bool { return x.Key == k }) {
				t.Errorf("%s: translation for unknown section %q", f, k)
			}
		}
		en := Template{Input: sf.Input, Translations: sf.Translations}.Localize(i18n.EN)
		texts := []string{en.Name, en.Description, en.Category}
		for _, fl := range en.Fields {
			texts = append(texts, fl.Label, fl.Placeholder, fl.Help)
			texts = append(texts, fl.Options...)
		}
		for _, s := range en.Sections {
			texts = append(texts, s.Title, s.Instruction)
		}
		for _, x := range texts {
			if han.MatchString(x) {
				t.Errorf("%s: untranslated text %q", f, x)
			}
		}
		if zh := (Template{Input: sf.Input, Translations: sf.Translations}).Localize(i18n.ZH); zh.Name != sf.Name {
			t.Errorf("%s: Chinese must stay as written", f)
		}
	}
}
