package adminapi_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
	"github.com/hwq830/zeusai/server/internal/secrets"
)

func TestSearchProviders(t *testing.T) {
	tavily := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/search" || r.Header.Get("Authorization") != "Bearer tvly-good-key" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://t.com/1","content":"c"}]}`))
	}))
	defer tavily.Close()
	box, err := secrets.NewBox(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	var srv *adminapi.Server
	h := setupWith(t, func(s *adminapi.Server) {
		s.Keys, s.SearchProvider = &secrets.Keys{Q: s.Q, Box: box}, "console"
		srv = s
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	var rt gen.SearchRouting
	h.do("GET", "/admin/v1/search-routing", nil, 200, &rt)
	if !rt.On || len(rt.Chain) != 1 || rt.Chain[0] != "bocha" || len(rt.Protocols) != 4 {
		t.Fatalf("routing = %+v", rt)
	}

	base := map[string]any{"id": "tavily", "name": "Tavily", "protocol": "tavily", "baseUrl": tavily.URL}
	h.do("POST", "/admin/v1/search-providers", map[string]any{"id": "bocha", "name": "x", "protocol": "bocha"}, 409, nil)
	h.do("POST", "/admin/v1/search-providers", map[string]any{"id": "x1", "name": "x", "protocol": "google"}, 400, nil)
	var p gen.KeyProvider
	h.do("POST", "/admin/v1/search-providers", merge(base, "apiKey", "tvly-good-key", "addToChain", true), 201, &p)
	if p.Kind != "search" || deref(p.Protocol) != "tavily" || p.KeySource != "console" || deref(p.SearchOrder) != 2 || !p.InRoute {
		t.Fatalf("created = %+v", p)
	}
	// The id is taken for model providers too.
	h.do("POST", "/admin/v1/providers", map[string]any{"id": "tavily", "name": "x", "baseUrl": "https://x.com/v1", "models": map[string]any{"fast": "m"}}, 409, nil)

	// Key test goes through the provider's own API.
	var res gen.ProviderKeyTest
	h.do("POST", "/admin/v1/provider-keys/test", map[string]string{"provider": "tavily", "apiKey": "tvly-bad-key0"}, 200, &res)
	if res.Ok || !strings.Contains(res.Message, "Key 无效") {
		t.Fatalf("bad key = %+v", res)
	}
	h.do("POST", "/admin/v1/provider-keys/test", map[string]string{"provider": "tavily", "apiKey": "tvly-good-key"}, 200, &res)
	if !res.Ok || res.Results == nil || len(*res.Results) != 1 {
		t.Fatalf("good key = %+v", res)
	}

	// Switch: Tavily primary, 博查 fallback. The orchestrator's chain follows.
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{}}, 400, nil)
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily", "tavily"}}, 400, nil)
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"nope"}}, 400, nil)
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily", "bocha"}}, 200, &rt)
	if rt.Chain[0] != "tavily" || deref(rt.UpdatedBy) != "owner@zeus.test" {
		t.Fatalf("saved = %+v", rt)
	}
	chain, err := providers.SearchChain(context.Background(), srv.Q)
	if err != nil || len(chain) != 2 || chain[0].Name != "tavily" || chain[0].Protocol != "tavily" || chain[0].BaseURL != tavily.URL ||
		chain[1].KeyEnv != "ZEUS_BOCHA_API_KEY" {
		t.Fatalf("chain = %+v %v", chain, err)
	}

	// In the chain: can't disable or delete; 博查 can't be deleted at all.
	h.do("PATCH", "/admin/v1/search-providers/tavily", map[string]any{"enabled": false}, 409, nil)
	h.do("DELETE", "/admin/v1/search-providers/tavily", nil, 409, nil)
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"bocha"}}, 200, nil)
	h.do("DELETE", "/admin/v1/search-providers/bocha", nil, 400, nil)
	h.do("PATCH", "/admin/v1/search-providers/tavily", map[string]any{"enabled": false, "name": "Tavily 2"}, 200, &p)
	if p.Enabled || p.Name != "Tavily 2" || p.InRoute || p.SearchOrder != nil {
		t.Fatalf("updated = %+v", p)
	}
	h.do("PUT", "/admin/v1/search-routing", map[string]any{"chain": []string{"tavily"}}, 400, nil) // disabled
	h.do("DELETE", "/admin/v1/search-providers/tavily", nil, 204, nil)
	var list gen.ProviderKeyList
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	for _, k := range list.Keys {
		if k.Provider == "tavily" {
			t.Fatal("keys must go with the provider")
		}
	}
}

func merge(m map[string]any, kv ...any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	for i := 0; i+1 < len(kv); i += 2 {
		out[kv[i].(string)] = kv[i+1]
	}
	return out
}
