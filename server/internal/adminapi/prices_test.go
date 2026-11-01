package adminapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/modelgw"
)

func TestModelPrices(t *testing.T) {
	page, err := os.ReadFile("../modelgw/testdata/bailian_page.json")
	if err != nil {
		t.Fatal(err)
	}
	fail := false
	bl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "busy", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write(page)
	}))
	t.Cleanup(bl.Close)
	cfgPath := filepath.Join(t.TempDir(), "modelgw.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
routes: { strong: [bailian], fast: [bailian] }
providers:
  - name: bailian
    base_url: https://dashscope.aliyuncs.com/compatible-mode/v1
    api_key_env: ZEUS_TEST_BL_KEY
    models: { strong: qwen3.8-max, fast: qwen-flash }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	h := setupWith(t, func(s *adminapi.Server) {
		s.ModelGWConfig, s.LLMProvider, s.BailianCatalogURL = cfgPath, "gateway", bl.URL
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	// Before any sync the built-in catalog is listed and metered.
	var l gen.ModelPriceList
	l = gen.ModelPriceList{}
	h.do("GET", "/admin/v1/model-prices", nil, 200, &l)
	builtin, _ := modelgw.BailianCatalog()
	if l.Source != "builtin" || len(l.Models) != len(builtin) || l.SyncedAt != nil {
		t.Fatalf("builtin list: source %s, %d models", l.Source, len(l.Models))
	}
	row := find(l, "qwen-plus")
	if row == nil || row.Origin != "builtin" || len(row.Tiers) != 3 || row.Metered.Input != 0.8 {
		t.Fatalf("qwen-plus %+v", row)
	}

	// A Bailian model with a catalog price needs no price on the override.
	var r gen.ModelRoute
	h.do("PUT", "/admin/v1/model-routing/strong", map[string]any{"provider": "bailian", "model": "qwen3.8-max"}, 200, &r)
	if r.Override == nil || !r.Override.Priced {
		t.Fatalf("route %+v", r)
	}
	h.do("PUT", "/admin/v1/model-routing/fast", map[string]any{"provider": "bailian", "model": "no-such-model"}, 400, nil)
	var rt gen.ModelRouting
	h.do("GET", "/admin/v1/model-routing", nil, 200, &rt)
	if !rt.Providers[0].Bailian || rt.Catalog == nil || len(rt.Catalog.Models) != len(builtin) {
		t.Fatalf("routing catalog/bailian missing: %+v", rt.Providers)
	}

	// Manual price: wins, shows who set it, survives syncs.
	h.do("PUT", "/admin/v1/model-prices", map[string]any{"model": "qwen3.8-max",
		"price": map[string]any{"input": 10, "output": 30, "cached": 1}, "note": "协议价"}, 200, &l)
	row = find(l, "qwen3.8-max")
	if row.Origin != "manual" || row.Metered.Output != 30 || row.Note != "协议价" || row.UpdatedBy == nil ||
		len(row.UsedBy) != 1 || row.UsedBy[0] != "strong" {
		t.Fatalf("manual %+v", row)
	}
	h.do("PUT", "/admin/v1/model-prices", map[string]any{"model": "my-custom",
		"price": map[string]any{"input": 1, "output": 2, "cached": 0.5}}, 200, &l)
	h.do("PUT", "/admin/v1/model-prices", map[string]any{"model": "x",
		"price": map[string]any{"input": -1, "output": 2, "cached": 0}}, 400, nil)

	// A failed sync changes nothing.
	fail = true
	var sr gen.ModelPriceSyncResult
	h.do("POST", "/admin/v1/model-prices/sync", nil, 200, &sr)
	if sr.Ok || sr.Message == "" {
		t.Fatalf("failed sync %+v", sr)
	}
	fail = false
	h.do("POST", "/admin/v1/model-prices/sync", nil, 200, &sr)
	if !sr.Ok || *sr.Count != 4 {
		t.Fatalf("sync %+v", sr)
	}
	l = gen.ModelPriceList{}
	h.do("GET", "/admin/v1/model-prices", nil, 200, &l)
	if l.Source != "synced" || l.SyncedAt == nil || len(l.Models) != 5 {
		t.Fatalf("synced list: %s %d", l.Source, len(l.Models))
	}
	if row = find(l, "qwen3.8-max"); row.Origin != "manual" || row.Tiers[0].Price.Output != 36 {
		t.Fatalf("manual should survive sync %+v", row)
	}
	if row = find(l, "decision-model-preview"); row.Origin != "none" || row.Metered != nil {
		t.Fatalf("unpriced %+v", row)
	}

	// The gateway meters with the pricing page.
	ov := (&modelgw.DBOverrides{Q: h.q}).Overrides(context.Background())
	if ov.Prices["qwen3.8-max"].Output != 30 || ov.Prices["qwen-plus"].Input != 0.8 || ov.Prices["my-custom"].Output != 2 {
		t.Fatalf("gateway prices %+v", ov.Prices)
	}

	// Clearing goes back to the synced price; a custom model disappears.
	h.do("DELETE", "/admin/v1/model-prices?model=qwen3.8-max", nil, 204, nil)
	h.do("DELETE", "/admin/v1/model-prices?model=my-custom", nil, 204, nil)
	l = gen.ModelPriceList{}
	h.do("GET", "/admin/v1/model-prices", nil, 200, &l)
	if row = find(l, "qwen3.8-max"); row.Origin != "synced" || row.Metered.Output != 36 || find(l, "my-custom") != nil {
		t.Fatalf("after clear %+v", row)
	}
	h.do("POST", "/admin/v1/model-prices/sync", nil, 200, &sr)
	if !sr.Ok || *sr.Added != 0 || len(*sr.Changed) != 0 {
		t.Fatalf("resync %+v", sr)
	}
}

func find(l gen.ModelPriceList, model string) *gen.ModelPriceRow {
	for i := range l.Models {
		if l.Models[i].Model == model {
			return &l.Models[i]
		}
	}
	return nil
}
