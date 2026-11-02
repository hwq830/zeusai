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
