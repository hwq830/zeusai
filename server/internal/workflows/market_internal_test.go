package workflows

import (
	"testing"

	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/i18n"
	tpl "github.com/hwq830/zeusai/server/internal/template"
)

func TestBuildSectionsUsesMarket(t *testing.T) {
	tp := &tpl.Template{Input: tpl.Input{
		Sections: []tpl.Section{{Key: "a", Title: "要闻", Instruction: "{{topics}} 最新"}},
		Sources:  []tpl.Source{{Type: "search", Value: "默认 新闻"}},
		MarketSources: map[string][]tpl.Source{
			"TW": {{Type: "site", Value: "ithome.com.tw"}, {Type: "search", Value: "科技 新闻"}},
		},
	}}
	sub := briefing.Subscription{FieldValues: map[string]any{"topics": []any{"芯片"}}}

	tw := buildSections(sub, tp, 2, i18n.ZH, "TW")[0].Queries
	if len(tw) != 3 || tw[1].Site != "ithome.com.tw" || tw[2].Q != "科技 新聞" || tw[0].Q != "晶片 最新" && tw[0].Q != "芯片 最新" {
		t.Fatalf("TW queries %+v", tw)
	}
	for _, q := range tw {
		if q.Country != "tw" || q.Lang != "zh-hant" {
			t.Fatalf("TW locale %+v", q)
		}
	}
	other := buildSections(sub, tp, 2, i18n.ZH, "other")[0].Queries
	if len(other) != 2 || other[1].Q != "默认 新闻" || other[0].Country != "" || other[0].Lang != "" {
		t.Fatalf("other queries %+v", other)
	}
	us := buildSections(sub, tp, 2, i18n.ZH, "US")[0].Queries
	if us[0].Country != "us" || us[0].Lang != "" || us[1].Q != "默认 新闻" {
		t.Fatalf("US queries %+v", us)
	}
}

func TestBuildSectionsContentRegionOverridesUser(t *testing.T) {
	tp := &tpl.Template{Input: tpl.Input{SearchRegion: "US", Sections: []tpl.Section{
		{Key: "us", Title: "美国"}, {Key: "cn", Title: "大陆", SearchRegion: "CN"}, {Key: "world", Title: "全球", SearchRegion: "global"},
	}, Sources: []tpl.Source{{Type: "site", Value: "example.com"}, {Type: "search", Value: "大陆 新闻"}, {Type: "search", Value: "global news"}}}}
	sections := buildSections(briefing.Subscription{}, tp, 1, i18n.ZHHant, "TW")
	for i, want := range []string{"US", "CN", "global"} {
		for _, q := range sections[i].Queries {
			if q.Region != want {
				t.Errorf("section %d: %+v", i, q)
			}
		}
	}
	if sections[1].Queries[0].Lang != "zh-hans" || sections[2].Queries[0].Country != "" {
		t.Fatal("search locale must follow content, not output language")
	}
}

func TestReportQuestionRegions(t *testing.T) {
	for _, tc := range []struct {
		values   []string
		fallback string
		want     string
	}{
		{nil, "CN", "CN"}, {[]string{"US"}, "CN", "US"}, {[]string{"global"}, "CN", "global"}, {[]string{"bogus"}, "TW", "TW"},
	} {
		got := questionRegions(tc.values, tc.fallback)
		if len(got) != 1 || got[0] != tc.want {
			t.Errorf("%+v got %+v", tc, got)
		}
	}
	got := questionRegions([]string{"CN", "CN", "TW", "US", "HK"}, "other")
	if len(got) != 3 || got[0] != "CN" || got[1] != "TW" || got[2] != "US" {
		t.Fatalf("invalid bounded region plan: %v", got)
	}
}
