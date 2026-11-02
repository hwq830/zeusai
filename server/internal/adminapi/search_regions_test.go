package adminapi_test

import (
	"context"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
)

func TestSearchRegionalConfigurationCompatibility(t *testing.T) {
	var srv *adminapi.Server
	h := setupWith(t, func(s *adminapi.Server) { s.SearchProvider = "console"; srv = s })
	h.createOwner("region@zeus.test", "correct-horse-battery")
	h.login("region@zeus.test", "correct-horse-battery")
	h.do("POST", "/admin/v1/search-providers", map[string]any{"id": "tavily", "name": "Tavily", "protocol": "tavily"}, 201, nil)
	var rt gen.SearchRouting
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily"}, "regionalChains": map[string][]string{"CN": {"bocha", "tavily"}, "other": {"bocha"}}}, 200, &rt)
	if len(rt.RegionalChains["CN"]) != 2 {
		t.Fatalf("routing %+v", rt)
	}
	// Old clients save only the default chain; regional overrides survive.
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily"}}, 200, &rt)
	if len(rt.RegionalChains) != 2 {
		t.Fatalf("legacy save erased overrides: %+v", rt)
	}
	cfg, err := providers.SearchRouting(context.Background(), srv.Q)
	if err != nil || len(cfg.Chain("cn")) != 2 || cfg.Chain("tw")[0].Name != "tavily" || cfg.Chain("other")[0].Name != "bocha" {
		t.Fatalf("effective routes %+v %v", cfg, err)
	}
	// Providers used only by a regional override cannot be disabled.
	h.do("PATCH", "/admin/v1/search-providers/bocha", map[string]any{"enabled": false}, 409, nil)
	for _, regions := range []any{
		map[string][]string{"global": {"tavily"}}, map[string][]string{"cn": {"bocha"}}, map[string][]string{"CN": {}}, map[string][]string{"CN": {"bocha", "bocha"}}, map[string][]string{"CN": {"missing"}},
	} {
		h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily"}, "regionalChains": regions}, 400, nil)
	}
	// Decode into a fresh map: encoding/json intentionally retains keys in reused maps.
	var cleared gen.SearchRouting
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily"}, "regionalChains": map[string][]string{}}, 200, &cleared)
	if len(cleared.RegionalChains) != 0 {
		t.Fatal("explicit empty map must clear overrides")
	}
	cfg, err = providers.SearchRouting(context.Background(), srv.Q)
	if err != nil || len(cfg.Regions) != 0 {
		t.Fatalf("stored overrides were not cleared: %+v %v", cfg, err)
	}
	h.do("PATCH", "/admin/v1/search-providers/bocha", map[string]any{"enabled": false}, 200, nil)
}
