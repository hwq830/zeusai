package adminapi_test

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/db"
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

	ctx := context.Background()
	// A tier choice made on the old 模型指定 page is folded into the imported route.
	if _, err := h.q.UpsertModelOverride(ctx, db.UpsertModelOverrideParams{Scope: "fast", Provider: "deepseek", Model: "deepseek-v3",
		Fallbacks: []string{"qwen"}}); err != nil {
		t.Fatal(err)
	}

	// First read imports the routing file: providers (with their key variables), prices, routes.
	var rt gen.ModelRouting
	h.do("GET", "/admin/v1/model-routing", nil, 200, &rt)
	if !rt.Configured || !rt.ImportedFromFile || !rt.GatewayOn || len(rt.Providers) != 2 || !rt.Providers[0].KeyPresent ||
		rt.Providers[1].KeyPresent || len(rt.Tiers) != 2 || len(rt.Purposes) != len(modelgw.Purposes) || len(rt.Versions) != 1 ||
		rt.Breaker.Failures != 3 || rt.Breaker.CooldownSeconds != 30 {
		t.Fatalf("routing = %+v", rt)
	}
	strong, fast := rt.Tiers[0], rt.Tiers[1]
	if strong.Tier != "strong" || len(strong.Steps) != 2 || strong.Steps[0].Provider != "qwen" || strong.Steps[0].Model != "qwen-max" ||
		!strong.Steps[0].UsesDefault || !strong.Steps[0].Priced || strong.Steps[0].Price.Output != 9.6 || strong.Steps[1].Priced {
		t.Fatalf("strong = %+v", strong)
	}
	if len(fast.Steps) != 2 || fast.Steps[0].Provider != "deepseek" || fast.Steps[0].Model != "deepseek-v3" || fast.Steps[0].UsesDefault ||
		fast.Steps[1].Provider != "qwen" {
		t.Fatalf("fast = %+v", fast)
	}
	if p := rt.Purposes[0]; p.Override != nil || len(p.Effective) != 2 || p.Effective[0].Provider != "deepseek" {
		t.Fatalf("purpose = %+v", p)
	}
	// File prices are now admin prices on the pricing page.
	var pl gen.ModelPriceList
	h.do("GET", "/admin/v1/model-prices", nil, 200, &pl)
	found := false
	for _, m := range pl.Models {
		if m.Model == "qwen-max" {
			found = m.Origin == "manual" && m.Manual != nil && m.Manual.Output == 9.6
		}
	}
	if !found {
		t.Fatal("file price not imported")
	}

	// Saving tier routes: validation.
	save := func(strong, fast []map[string]any, failures, cooldown, status int) {
		t.Helper()
		rt = gen.ModelRouting{}
		h.do("PUT", "/admin/v1/model-routing", map[string]any{"routes": map[string]any{"strong": strong, "fast": fast},
			"breaker": map[string]any{"failures": failures, "cooldownSeconds": cooldown}}, status, &rt)
	}
	q := []map[string]any{{"provider": "qwen"}}
	save([]map[string]any{{"provider": "nope"}}, q, 3, 30, 400)
	save([]map[string]any{{"provider": "qwen"}, {"provider": "qwen"}}, q, 3, 30, 400)
	save(q, q, 0, 30, 400)
	save(q, q, 3, 1, 400)
	save([]map[string]any{{"provider": "qwen", "model": "qwen3.8-max"}, {"provider": "deepseek"}}, q, 5, 60, 200)
	if s := rt.Tiers[0].Steps; len(s) != 2 || s[0].Model != "qwen3.8-max" || s[0].UsesDefault || rt.Breaker.Failures != 5 ||
		len(rt.Versions) != 2 || rt.Versions[0].Action != "settings" || rt.UpdatedBy == nil {
		t.Fatalf("saved = %+v", rt)
	}

	// Purposes: tiers are no longer set here; prices are not taken any more.
	h.do("PUT", "/admin/v1/model-routing/strong", map[string]any{"provider": "qwen", "model": "qwen-max"}, 400, nil)
	h.do("PUT", "/admin/v1/model-routing/nope", map[string]any{"provider": "qwen", "model": "qwen-max"}, 404, nil)
	h.do("PUT", "/admin/v1/model-routing/report.write", map[string]any{"provider": "nope", "model": "x"}, 400, nil)
	rt = gen.ModelRouting{}
	h.do("PUT", "/admin/v1/model-routing/report.write", map[string]any{"provider": "deepseek", "model": "deepseek-chat"}, 200, &rt)
	var write gen.PurposeRoute
	for _, p := range rt.Purposes {
		if p.Scope == "report.write" {
			write = p
		}
	}
	if write.Override == nil || len(write.Effective) != 2 || write.Effective[0].Provider != "deepseek" ||
		write.Effective[1].Provider != "qwen" || write.Effective[1].Model != "qwen3.8-max" {
		t.Fatalf("purpose route = %+v", write)
	}

	// The gateway built from the same database sees the routing.
	ov := (&modelgw.DBOverrides{Q: h.q}).Overrides(ctx)
	if ov.Settings == nil || ov.Settings.Routes[llm.TierStrong][0].Model != "qwen3.8-max" || ov.Settings.BreakerFailures != 5 ||
		ov.Purposes["report.write"].Provider != "deepseek" || len(ov.Providers) != 2 || ov.Providers[0].APIKeyEnv == "" ||
		ov.Manual["qwen-max"].Output != 9.6 {
		t.Fatalf("overrides = %+v", ov)
	}

	// A provider in the routing can be neither disabled nor deleted.
	h.do("PATCH", "/admin/v1/providers/deepseek", map[string]any{"enabled": false}, 409, nil)
	h.do("DELETE", "/admin/v1/providers/deepseek", nil, 409, nil)

	// Going back to the imported version restores the tier routes and drops the purpose choice.
	first := rt.Versions[len(rt.Versions)-1]
	if first.Action != "import" {
		t.Fatalf("versions = %+v", rt.Versions)
	}
	rt = gen.ModelRouting{}
	h.do("POST", "/admin/v1/model-routing/versions/"+strconv.FormatInt(first.Id, 10)+"/restore", nil, 200, &rt)
	if rt.Tiers[0].Steps[0].Model != "qwen-max" || rt.Breaker.Failures != 3 || rt.Versions[0].Action != "restore" {
		t.Fatalf("restored = %+v", rt)
	}
	for _, p := range rt.Purposes {
		if p.Override != nil {
			t.Fatalf("purpose kept after restore: %+v", p)
		}
	}
	h.do("POST", "/admin/v1/model-routing/versions/99999/restore", nil, 404, nil)

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

	// Imported providers are ordinary providers now: editable, and listed as imported.
	var kp gen.KeyProvider
	h.do("PATCH", "/admin/v1/providers/qwen", map[string]any{"timeoutSeconds": 90}, 200, &kp)
	if kp.Source != "console" || kp.Imported == nil || !*kp.Imported || *kp.TimeoutSeconds != 90 || kp.ApiKeyEnv != "ZEUS_TEST_QWEN_KEY" {
		t.Fatalf("imported provider = %+v", kp)
	}
	var changed gen.ModelRouting
	h.do("GET", "/admin/v1/model-routing", nil, 200, &changed)
	timeoutFound := false
	for _, p := range changed.Providers {
		if p.Name == "qwen" {
			timeoutFound = true
			if p.TimeoutSeconds == nil || *p.TimeoutSeconds != 90 {
				t.Fatalf("routing timeout not updated: %+v", p)
			}
		}
	}
	if !timeoutFound {
		t.Fatal("missing qwen in routing")
	}
}
