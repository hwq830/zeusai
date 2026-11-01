package adminapi_test

import (
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
)

func TestModelRouting(t *testing.T) {
	var calls atomic.Int32
	up := fakeUpstream(t, &calls)
	cfgPath := filepath.Join(t.TempDir(), "modelgw.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
routes: { strong: [qwen, deepseek], fast: [qwen] }
providers:
  - name: qwen
    base_url: `+up.URL+`
    api_key_env: ZEUS_TEST_QWEN_KEY
    models: { strong: qwen-max, fast: qwen-turbo }
    prices: { qwen-max: { input: 2.4, output: 9.6 }, qwen-turbo: { input: 0.3, output: 0.6 } }
  - name: deepseek
    base_url: `+up.URL+`
    api_key_env: ZEUS_TEST_DS_KEY
    models: { strong: deepseek-chat, fast: deepseek-chat }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZEUS_TEST_QWEN_KEY", "good-key-1234")
	t.Setenv("ZEUS_TEST_DS_KEY", "")
	h := setupWith(t, func(s *adminapi.Server) { s.ModelGWConfig, s.LLMProvider = cfgPath, "gateway" })
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	var rt gen.ModelRouting
	h.do("GET", "/admin/v1/model-routing", nil, 200, &rt)
	if !rt.Configured || len(rt.Providers) != 2 || !rt.Providers[0].KeyPresent || rt.Providers[1].KeyPresent ||
		len(rt.Tiers) != 2 || len(rt.Purposes) != len(modelgw.Purposes) {
		t.Fatalf("routing = %+v", rt)
	}
	strong := rt.Tiers[0]
	if strong.Scope != "strong" || strong.Effective.Provider != "qwen" || strong.Effective.Model != "qwen-max" ||
		len(strong.Effective.Fallbacks) != 1 || strong.Override != nil {
		t.Fatalf("strong = %+v", strong)
	}

	// A model without a price in the file needs one.
	h.do("PUT", "/admin/v1/model-routing/strong", map[string]any{"provider": "qwen", "model": "qwen3.8-max"}, 400, nil)
	h.do("PUT", "/admin/v1/model-routing/strong", map[string]any{"provider": "nope", "model": "x"}, 400, nil)
	h.do("PUT", "/admin/v1/model-routing/nope", map[string]any{"provider": "qwen", "model": "qwen-max"}, 404, nil)
	var r gen.ModelRoute
	h.do("PUT", "/admin/v1/model-routing/strong", map[string]any{"provider": "qwen", "model": "qwen3.8-max",
		"price": map[string]any{"input": 3, "output": 12, "cached": 1}, "fallbacks": []string{}}, 200, &r)
	if r.Override == nil || !r.Override.Priced || r.Effective.Model != "qwen3.8-max" || len(r.Effective.Fallbacks) != 0 {
		t.Fatalf("route = %+v", r)
	}
	// Purposes follow their tier until overridden; fallbacks are tier-only.
	h.do("PUT", "/admin/v1/model-routing/report.write", map[string]any{"provider": "deepseek", "model": "deepseek-chat",
		"fallbacks": []string{"qwen"}}, 400, nil)
	h.do("PUT", "/admin/v1/model-routing/report.write", map[string]any{"provider": "deepseek", "model": "deepseek-chat",
		"price": map[string]any{"input": 2, "output": 8, "cached": 0.5}}, 200, &r)
	if r.Kind != "purpose" || r.Default.Model != "qwen3.8-max" || r.Effective.Provider != "deepseek" ||
		len(r.Effective.Fallbacks) != 1 || r.Effective.Fallbacks[0] != "qwen" {
		t.Fatalf("purpose route = %+v", r)
	}

	// The gateway built from the same database sees the overrides.
	ov := (&modelgw.DBOverrides{Q: h.q}).Overrides(context.Background())
	if ov.Tiers[llm.TierStrong].Model != "qwen3.8-max" || ov.Tiers[llm.TierStrong].Fallbacks == nil ||
		ov.Tiers[llm.TierStrong].Price.Output != 12 || ov.Purposes["report.write"].Provider != "deepseek" {
		t.Fatalf("overrides = %+v", ov)
	}

	// Test with the key the gateway would use.
	var res gen.ProviderKeyTest
	h.do("POST", "/admin/v1/model-routing/test", map[string]any{"provider": "qwen", "model": "qwen3.8-max"}, 200, &res)
	if !res.Ok {
		t.Fatalf("test = %+v", res)
	}
	h.do("POST", "/admin/v1/model-routing/test", map[string]any{"provider": "deepseek", "model": "deepseek-chat"}, 200, &res)
	if res.Ok {
		t.Fatalf("test without key = %+v", res)
	}

	// Live model list with the gateway's key; no key is reported, not an error.
	var lm struct {
		Models []string `json:"models"`
		Error  *string  `json:"error"`
	}
	h.do("GET", "/admin/v1/model-routing/providers/qwen/models", nil, 200, &lm)
	if len(lm.Models) != 2 || lm.Models[0] != "qwen-max" || lm.Error != nil {
		t.Fatalf("models = %+v", lm)
	}
	h.do("GET", "/admin/v1/model-routing/providers/deepseek/models", nil, 200, &lm)
	if len(lm.Models) != 0 || lm.Error == nil {
		t.Fatalf("models without key = %+v", lm)
	}
	h.do("GET", "/admin/v1/model-routing/providers/nope/models", nil, 404, nil)

	// Reset goes back to the file.
	h.do("DELETE", "/admin/v1/model-routing/strong", nil, 204, nil)
	h.do("GET", "/admin/v1/model-routing", nil, 200, &rt)
	if rt.Tiers[0].Override != nil || rt.Tiers[0].Effective.Model != "qwen-max" {
		t.Fatalf("after reset = %+v", rt.Tiers[0])
	}
}
