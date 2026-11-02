package template_test

import (
	"context"
	"testing"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/template"
)

func TestOfficialTemplatesByMarket(t *testing.T) {
	q := db.New(testdb.New(t))
	ctx := context.Background()
	s := &template.Service{Q: q}
	if _, err := s.SeedOfficial(ctx); err != nil {
		t.Fatal(err)
	}
	names := func(m string) map[string]template.Template {
		ts, _, err := s.List(ctx, uuid.New(), template.ListParams{Market: m, Limit: 50})
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]template.Template{}
		for _, x := range ts {
			out[x.Name] = x
		}
		return out
	}
	other, tw, hk, us := names("other"), names("TW"), names("HK"), names("US")
	for _, local := range []string{"台股早报", "港股早报", "新马财经早报", "美股早报"} {
		if _, ok := other[local]; ok {
			t.Errorf("%s shown to other", local)
		}
	}
	if _, ok := tw["台股早报"]; !ok || len(tw) != len(other)+1 {
		t.Errorf("TW list: %d vs other %d", len(tw), len(other))
	}
	if _, ok := hk["港股早报"]; !ok {
		t.Error("HK template missing")
	}
	if _, ok := us["美股早报"]; !ok {
		t.Error("US template missing")
	}
	tech := tw["科技早报"]
	if src := tech.SourcesFor("TW"); src[0].Value != "ithome.com.tw" {
		t.Errorf("TW sources %+v", src)
	}
	if src := tech.SourcesFor("MO"); src[0].Value != "unwire.hk" {
		t.Errorf("MO sources (yaml alias) %+v", src)
	}
	if src := tech.SourcesFor("other"); src[0].Value != "科技 新闻 今日" {
		t.Errorf("default sources %+v", src)
	}
	// Re-seeding is stable (no version bump without changes).
	v := tech.Version
	if _, err := s.SeedOfficial(ctx); err != nil {
		t.Fatal(err)
	}
	if again := names("TW")["科技早报"]; again.Version != v {
		t.Errorf("version %d -> %d", v, again.Version)
	}
}

func TestMarketValidation(t *testing.T) {
	base := template.Input{Name: "x", Sections: []template.Section{{Title: "a"}}}
	in := base
	in.Markets = []string{"TW", "XX"}
	if in.Validate() == nil {
		t.Error("bad market accepted")
	}
	in = base
	in.Markets = []string{"other"}
	if in.Validate() == nil {
		t.Error("other is not a target market")
	}
	in = base
	in.MarketSources = map[string][]template.Source{"HK": {{Type: "bogus", Value: "x"}}}
	if in.Validate() == nil {
		t.Error("bad source accepted")
	}
	in = base
	in.MarketSources = map[string][]template.Source{"HK": {}, "US": {{Type: "site", Value: "cnbc.com"}}}
	if err := in.Validate(); err != nil || len(in.MarketSources) != 1 {
		t.Errorf("empty market list should be dropped: %v %+v", err, in.MarketSources)
	}
	if got := in.ForUser(); got.MarketSources != nil || got.Markets != nil {
		t.Error("ForUser keeps market settings")
	}
}
