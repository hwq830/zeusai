package adminapi_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/secrets"
)

// fakeUpstream accepts only "Bearer good-key-1234" on both the chat and the search endpoint.
func fakeUpstream(t *testing.T, calls *atomic.Int32) *httptest.Server {
	return fakeUpstreamBodies(t, calls, nil)
}

// fakeUpstreamBodies also keeps the last request body in last, when given.
func fakeUpstreamBodies(t *testing.T, calls *atomic.Int32, last *atomic.Value) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if last != nil {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			last.Store(body)
		}
		if r.Header.Get("Authorization") != "Bearer good-key-1234" {
			http.Error(w, `{"error":"invalid api key"}`, http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/chat/completions":
			_ = json.NewEncoder(w).Encode(map[string]any{"model": "qwen-turbo",
				"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": "p"}}}})
		case "/models":
			_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
				map[string]string{"id": "qwen-turbo"}, map[string]string{"id": "qwen-max"}}})
		case "/v1/web-search":
			_ = json.NewEncoder(w).Encode(map[string]any{"code": 200, "data": map[string]any{"webPages": map[string]any{
				"value": []any{map[string]string{"name": "n", "url": "https://a.cn/x", "snippet": "s"}}}}})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(s.Close)
	return s
}

func TestProviderKeys(t *testing.T) {
	var calls atomic.Int32
	up := fakeUpstream(t, &calls)
	cfgPath := filepath.Join(t.TempDir(), "modelgw.yaml")
	if err := os.WriteFile(cfgPath, []byte(`
routes: { strong: [qwen], fast: [qwen] }
providers:
  - name: qwen
    base_url: `+up.URL+`
    api_key_env: ZEUS_TEST_QWEN_KEY
    models: { strong: qwen-max, fast: qwen-turbo }
`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ZEUS_TEST_QWEN_KEY", "")
	box, err := secrets.NewBox(strings.Repeat("k", 32))
	if err != nil {
		t.Fatal(err)
	}
	var keys *secrets.Keys
	h := setupWith(t, func(s *adminapi.Server) {
		keys = &secrets.Keys{Q: s.Q, Box: box}
		s.Keys, s.ModelGWConfig, s.SearchProvider, s.BochaBaseURL = keys, cfgPath, "bocha", up.URL
	})
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	var list gen.ProviderKeyList
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	if !list.EncryptionEnabled || len(list.Providers) != 2 || list.Providers[0].Id != "qwen" || list.Providers[0].KeySource != "none" ||
		!list.Providers[0].InRoute || list.Providers[1].Id != "bocha" || !list.Providers[1].InRoute {
		t.Fatalf("list = %+v", list)
	}

	// Test before saving: a bad key fails with a readable reason, a good one passes.
	var res gen.ProviderKeyTest
	h.do("POST", "/admin/v1/provider-keys/test", map[string]string{"provider": "qwen", "apiKey": "bad-key-0000"}, 200, &res)
	if res.Ok || !strings.Contains(res.Message, "Key 无效") {
		t.Fatalf("bad key test = %+v", res)
	}
	h.do("POST", "/admin/v1/provider-keys/test", map[string]string{"provider": "bocha", "apiKey": "good-key-1234"}, 200, &res)
	if !res.Ok {
		t.Fatalf("good key test = %+v", res)
	}
	h.do("POST", "/admin/v1/provider-keys", map[string]string{"provider": "nope", "apiKey": "good-key-1234"}, 400, nil)

	// Save and activate: stored sealed, shown as last 4, resolved by the api processes.
	var k1 gen.ProviderKey
	h.do("POST", "/admin/v1/provider-keys", map[string]any{"provider": "qwen", "label": "主账号", "apiKey": "good-key-1234"}, 201, &k1)
	if !k1.Active || k1.Last4 != "1234" {
		t.Fatalf("created = %+v", k1)
	}
	row, err := h.q.GetProviderKey(ctx, k1.Id)
	if err != nil || strings.Contains(string(row.Ciphertext), "good-key") {
		t.Fatalf("stored in clear: %v", err)
	}
	reader := &secrets.Keys{Q: h.q, Box: box}
	if got := reader.Key(ctx, "qwen", "ZEUS_TEST_QWEN_KEY"); got != "good-key-1234" {
		t.Fatalf("resolved key = %q", got)
	}
	h.do("POST", "/admin/v1/provider-keys/"+k1.Id.String()+"/test", map[string]any{}, 200, &res)
	if !res.Ok {
		t.Fatalf("saved key test = %+v", res)
	}

	// An API address saved with the key is used for tests and by the api processes.
	var calls2 atomic.Int32
	up2 := fakeUpstream(t, &calls2)
	h.do("POST", "/admin/v1/provider-keys/test", map[string]string{"provider": "qwen", "apiKey": "good-key-1234", "baseUrl": up2.URL + "/"}, 200, &res)
	if !res.Ok || calls2.Load() != 1 {
		t.Fatalf("test with address = %+v, calls %d", res, calls2.Load())
	}
	h.do("PATCH", "/admin/v1/provider-keys/"+k1.Id.String(), map[string]string{"baseUrl": "ftp://x"}, 400, nil)
	var k1u gen.ProviderKey
	h.do("PATCH", "/admin/v1/provider-keys/"+k1.Id.String(), map[string]string{"baseUrl": up2.URL + "/"}, 200, &k1u)
	if k1u.BaseUrl != up2.URL || k1u.Label != "主账号" {
		t.Fatalf("updated = %+v", k1u)
	}
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	if len(list.Keys) != 1 || list.Keys[0].BaseUrl != up2.URL {
		t.Fatalf("listed = %+v", list.Keys)
	}
	if got := (&secrets.Keys{Q: h.q, Box: box}).BaseURL(ctx, "qwen"); got != up2.URL {
		t.Fatalf("resolved address = %q", got)
	}
	h.do("POST", "/admin/v1/provider-keys/"+k1.Id.String()+"/test", map[string]any{}, 200, &res)
	if !res.Ok || calls2.Load() != 2 {
		t.Fatalf("saved key test with address = %+v, calls %d", res, calls2.Load())
	}
	h.do("PATCH", "/admin/v1/provider-keys/"+k1.Id.String(), map[string]string{"baseUrl": ""}, 200, &k1u)
	if k1u.BaseUrl != "" {
		t.Fatalf("cleared = %+v", k1u)
	}

	// Test parameters reach the upstream, and the reply comes back.
	var calls3 atomic.Int32
	var last atomic.Value
	up3 := fakeUpstreamBodies(t, &calls3, &last)
	h.do("POST", "/admin/v1/provider-keys/test", map[string]any{"provider": "qwen", "apiKey": "good-key-1234", "baseUrl": up3.URL,
		"params": map[string]any{"model": "qwen-plus", "system": "be brief", "prompt": "你好", "maxTokens": 20, "temperature": 0.5}}, 200, &res)
	body, _ := last.Load().(map[string]any)
	if !res.Ok || res.Reply == nil || *res.Reply != "p" || body["model"] != "qwen-plus" || body["max_tokens"] != float64(20) ||
		body["temperature"] != 0.5 || len(body["messages"].([]any)) != 2 {
		t.Fatalf("test with params = %+v, upstream got %v", res, body)
	}
	h.do("POST", "/admin/v1/provider-keys/test", map[string]any{"provider": "qwen", "apiKey": "good-key-1234", "params": map[string]any{"maxTokens": 5000}}, 400, nil)
	h.do("POST", "/admin/v1/provider-keys/test", map[string]any{"provider": "bocha", "apiKey": "good-key-1234",
		"params": map[string]any{"query": "AI 新闻", "count": 3}}, 200, &res)
	if !res.Ok || res.Results == nil || len(*res.Results) != 1 || (*res.Results)[0].Url != "https://a.cn/x" {
		t.Fatalf("search test = %+v", res)
	}
	h.do("POST", "/admin/v1/provider-keys/"+k1.Id.String()+"/test", map[string]any{"params": map[string]any{"prompt": "hi"}}, 200, &res)
	if !res.Ok || res.Reply == nil {
		t.Fatalf("saved key test with params = %+v", res)
	}

	// A second key takes over; the first is deactivated, then can be deleted.
	var k2 gen.ProviderKey
	h.do("POST", "/admin/v1/provider-keys", map[string]any{"provider": "qwen", "apiKey": "other-key-5678"}, 201, &k2)
	h.do("GET", "/admin/v1/provider-keys", nil, 200, &list)
	active := 0
	for _, k := range list.Keys {
		if k.Active {
			active++
			if k.Id != k2.Id {
				t.Fatalf("wrong key active: %+v", k)
			}
		}
		if k.Id == k1.Id && (k.LastTestOk == nil || !*k.LastTestOk || k.CreatedBy == nil) {
			t.Fatalf("k1 = %+v", k)
		}
	}
	if active != 1 || list.Providers[0].KeySource != "console" {
		t.Fatalf("list = %+v", list)
	}
	h.do("DELETE", "/admin/v1/provider-keys/"+k2.Id.String(), nil, 409, nil)
	h.do("POST", "/admin/v1/provider-keys/"+k2.Id.String()+"/deactivate", nil, 200, nil)
	h.do("DELETE", "/admin/v1/provider-keys/"+k1.Id.String(), nil, 204, nil)

	// The routing page reports whether each provider has a key.
	var gw gen.ModelRouting
	h.do("GET", "/admin/v1/model-routing", nil, 200, &gw)
	if len(gw.Providers) != 1 || gw.Providers[0].KeyPresent {
		t.Fatalf("model-routing = %+v", gw.Providers)
	}

	// Only owners manage keys; everyone can read the masked list.
	hash, _ := adminapi.HashPassword("operator-password-1")
	if _, err := h.q.CreateAdminUser(ctx, db.CreateAdminUserParams{Email: "op@zeus.test", PasswordHash: hash, Role: "operator"}); err != nil {
		t.Fatal(err)
	}
	h.login("op@zeus.test", "operator-password-1")
	h.do("GET", "/admin/v1/provider-keys", nil, 200, nil)
	h.do("POST", "/admin/v1/provider-keys", map[string]string{"provider": "qwen", "apiKey": "good-key-1234"}, 403, nil)
	h.do("POST", "/admin/v1/provider-keys/"+k2.Id.String()+"/activate", nil, 403, nil)

	var audit struct {
		Items []struct{ Action string } `json:"items"`
	}
	h.login("owner@zeus.test", "correct-horse-battery")
	h.do("GET", "/admin/v1/audit-log", nil, 200, &audit)
	seen := map[string]bool{}
	for _, a := range audit.Items {
		seen[a.Action] = true
	}
	for _, a := range []string{"provider_key.create", "provider_key.activate", "provider_key.deactivate", "provider_key.delete"} {
		if !seen[a] {
			t.Fatalf("audit missing %s: %+v", a, audit.Items)
		}
	}
}
