package adminapi_test

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/modelgw"
	"github.com/hwq830/zeusai/server/internal/secrets"
)

func TestModelProviders(t *testing.T) {
	var calls atomic.Int32
	up := fakeUpstream(t, &calls)
	cfgPath := filepath.Join(t.TempDir(), "modelgw.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
routes: { strong: [qwen], fast: [qwen] }
providers:
  - name: qwen
    display_name: 千问（文件）
    base_url: `+up.URL+`
    api_key_env: ZEUS_TEST_QWEN_KEY
    models: { strong: qwen-max, fast: qwen-turbo }
    prices: { qwen-max: { input: 1, output: 2 } }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZEUS_TEST_QWEN_KEY", "")
	box, err := secrets.NewBox(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	h := setupWith(t, func(s *adminapi.Server) {
		s.Keys, s.ModelGWConfig, s.LLMProvider = &secrets.Keys{Q: s.Q, Box: box}, cfgPath, "gateway"
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	// Validation: id format, clashes with file and search providers, address, a model.
	base := map[string]any{"id": "moonshot", "name": "月之暗面", "baseUrl": up.URL, "models": map[string]any{"strong": "kimi-k3"}}
	with := func(k string, v any) map[string]any {
		m := map[string]any{}
		for kk, vv := range base {
			m[kk] = vv
		}
		m[k] = v
		return m
	}
	h.do("POST", "/admin/v1/providers", with("id", "Bad Id"), 400, nil)
	h.do("POST", "/admin/v1/providers", with("id", "qwen"), 409, nil)
	h.do("POST", "/admin/v1/providers", with("id", "bocha"), 409, nil)
	h.do("POST", "/admin/v1/providers", with("baseUrl", "ftp://x"), 400, nil)
	h.do("POST", "/admin/v1/providers", with("models", map[string]any{}), 400, nil)

	// Create with a key: listed as a console LLM provider with an active console key.
	var p gen.KeyProvider
	h.do("POST", "/admin/v1/providers", with("apiKey", "good-key-1234"), 201, &p)
	if p.Source != "console" || p.Kind != "llm" || !p.Enabled || p.KeySource != "console" || *p.Models.Strong != "kimi-k3" ||
		*p.TimeoutSeconds != 120 || p.InRoute {
		t.Fatalf("created %+v", p)
	}
	h.do("POST", "/admin/v1/providers", base, 409, nil)
	var list gen.ProviderKeyList
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	if len(list.Providers) != 3 || list.Providers[0].Name != "千问（文件）" || list.Providers[0].Source != "file" ||
		list.Providers[1].Id != "moonshot" || list.Providers[2].Source != "builtin" || list.Providers[2].Kind != "search" {
		t.Fatalf("providers %+v", list.Providers)
	}

	// Its key tests with its own address and fast model (falls back to strong when empty).
	var res gen.ProviderKeyTest
	h.do("POST", "/admin/v1/provider-keys/"+list.Keys[0].Id.String()+"/test", map[string]any{}, 200, &res)
	if !res.Ok {
		t.Fatalf("test %+v", res)
	}

	// Usable in model routing: listed, can be chosen (with a price, it is not Bailian), then in route.
	var rt gen.ModelRouting
	h.do("GET", "/admin/v1/model-routing", nil, 200, &rt)
	if len(rt.Providers) != 2 || rt.Providers[1].Name != "moonshot" || rt.Providers[1].DisplayName != "月之暗面" {
		t.Fatalf("routing providers %+v", rt.Providers)
	}
	h.do("PUT", "/admin/v1/model-routing/report.write", map[string]any{"provider": "moonshot", "model": "kimi-k3",
		"price": map[string]any{"input": 4, "output": 16, "cached": 1}}, 200, nil)
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	if !list.Providers[1].InRoute {
		t.Fatalf("should be in route %+v", list.Providers[1])
	}
	ov := (&modelgw.DBOverrides{Q: h.q}).Overrides(context.Background())
	if len(ov.Providers) != 1 || ov.Providers[0].BaseURL != up.URL || ov.Providers[0].Models["strong"] != "kimi-k3" {
		t.Fatalf("gateway providers %+v", ov.Providers)
	}

	// Edit; disabling or deleting is refused while model routing uses it.
	h.do("PATCH", "/admin/v1/providers/moonshot", map[string]any{"name": "Kimi", "timeoutSeconds": 60,
		"models": map[string]any{"fast": "kimi-mini"}}, 200, &p)
	if p.Name != "Kimi" || *p.TimeoutSeconds != 60 || *p.Models.Strong != "kimi-k3" || *p.Models.Fast != "kimi-mini" {
		t.Fatalf("updated %+v", p)
	}
	h.do("PATCH", "/admin/v1/providers/moonshot", map[string]any{"enabled": false}, 409, nil)
	h.do("DELETE", "/admin/v1/providers/moonshot", nil, 409, nil)
	h.do("PATCH", "/admin/v1/providers/qwen", map[string]any{"name": "x"}, 400, nil)
	h.do("DELETE", "/admin/v1/providers/nope", nil, 404, nil)

	// Once unused: disable (gone from routing choices), then delete with its keys.
	h.do("DELETE", "/admin/v1/model-routing/report.write", nil, 204, nil)
	h.do("PATCH", "/admin/v1/providers/moonshot", map[string]any{"enabled": false}, 200, &p)
	h.do("GET", "/admin/v1/model-routing", nil, 200, &rt)
	if len(rt.Providers) != 1 {
		t.Fatalf("disabled provider still offered %+v", rt.Providers)
	}
	h.do("PUT", "/admin/v1/model-routing/strong", map[string]any{"provider": "moonshot", "model": "kimi-k3",
		"price": map[string]any{"input": 4, "output": 16, "cached": 1}}, 400, nil)
	h.do("DELETE", "/admin/v1/providers/moonshot", nil, 204, nil)
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	if len(list.Providers) != 2 || len(list.Keys) != 0 {
		t.Fatalf("after delete %+v keys %d", list.Providers, len(list.Keys))
	}
}
