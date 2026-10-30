package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
)

// upstreamServer answers /chat/completions with status, counting calls.
func upstreamServer(t *testing.T, status int, content string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/chat/completions" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		if status != http.StatusOK {
			http.Error(w, "upstream says no", status)
			return
		}
		var req struct{ Model string }
		_ = json.NewDecoder(r.Body).Decode(&req)
		_ = json.NewEncoder(w).Encode(map[string]any{
			"model":   req.Model,
			"choices": []any{map[string]any{"message": map[string]string{"role": "assistant", "content": content}}},
			"usage": map[string]any{"prompt_tokens": 1000, "completion_tokens": 500,
				"prompt_tokens_details": map[string]int{"cached_tokens": 400}},
		})
	}))
	t.Cleanup(s.Close)
	return s
}

func gateway(t *testing.T, mod moderation.Checker, urls ...string) *Gateway {
	t.Helper()
	return gatewayWith(t, mod, nil, urls...)
}

func gatewayWith(t *testing.T, mod moderation.Checker, opts []Option, urls ...string) *Gateway {
	t.Helper()
	t.Setenv("TEST_KEY", "k")
	cfg := Config{Routes: map[llm.Tier][]string{}, BreakerFailures: 2, BreakerCooldown: time.Minute}
	for i, u := range urls {
		name := string(rune('a' + i))
		cfg.Providers = append(cfg.Providers, ProviderConfig{
			Name: name, BaseURL: u, APIKeyEnv: "TEST_KEY",
			Models: map[llm.Tier]string{llm.TierStrong: name + "-strong", llm.TierFast: name + "-fast"},
			Prices: map[string]Price{name + "-strong": {Input: 4, Output: 16, Cached: 1}},
		})
		cfg.Routes[llm.TierStrong] = append(cfg.Routes[llm.TierStrong], name)
		cfg.Routes[llm.TierFast] = append(cfg.Routes[llm.TierFast], name)
	}
	g, err := New(cfg, mod, nil, opts...)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

type mapKeys map[string]string

func (m mapKeys) Key(_ context.Context, provider, _ string) string { return m[provider] }

func TestKeysPerCall(t *testing.T) {
	var ca, cb atomic.Int32
	a := upstreamServer(t, http.StatusOK, "from a", &ca)
	b := upstreamServer(t, http.StatusOK, "from b", &cb)
	keys := mapKeys{"b": "k"}
	g := gatewayWith(t, nil, []Option{WithKeys(keys)}, a.URL, b.URL)

	// a has no key: skipped without a request.
	resp, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierStrong})
	if err != nil || resp.Provider != "b" || ca.Load() != 0 {
		t.Fatalf("resp %+v err %v calls a=%d", resp, err, ca.Load())
	}
	// A key added later is used on the next call, no rebuild.
	keys["a"] = "k"
	resp, err = g.Complete(context.Background(), llm.Request{Tier: llm.TierStrong})
	if err != nil || resp.Provider != "a" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	// No keys at all: unavailable, nothing sent.
	delete(keys, "a")
	delete(keys, "b")
	if _, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierStrong}); apperr.As(err).Code != apperr.Unavailable("").Code {
		t.Fatalf("err %v", err)
	}
	if ca.Load() != 1 || cb.Load() != 1 {
		t.Fatalf("calls a=%d b=%d", ca.Load(), cb.Load())
	}
}

func TestFallbackOnServerError(t *testing.T) {
	var ca, cb atomic.Int32
	a := upstreamServer(t, http.StatusServiceUnavailable, "", &ca)
	b := upstreamServer(t, http.StatusOK, "你好", &cb)
	g := gateway(t, nil, a.URL, b.URL)

	resp, err := g.Complete(context.Background(), llm.Request{Purpose: "t", Tier: llm.TierStrong})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Provider != "b" || resp.Model != "b-strong" || resp.Content != "你好" {
		t.Fatalf("resp: %+v", resp)
	}
	// 600 uncached * 4 + 400 cached * 1 + 500 out * 16 micro-CNY.
	if resp.CostMicros != 600*4+400*1+500*16 {
		t.Fatalf("cost %d", resp.CostMicros)
	}

	// After two failures provider a is skipped until the cooldown ends.
	if _, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierStrong}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierStrong}); err != nil {
		t.Fatal(err)
	}
	if ca.Load() != 2 || cb.Load() != 3 {
		t.Fatalf("calls a=%d b=%d", ca.Load(), cb.Load())
	}
	g.breaker.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	if _, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierStrong}); err != nil {
		t.Fatal(err)
	}
	if ca.Load() != 3 {
		t.Fatalf("a not retried after cooldown: %d", ca.Load())
	}
}

func TestNoFallbackOnClientError(t *testing.T) {
	var ca, cb atomic.Int32
	a := upstreamServer(t, http.StatusBadRequest, "", &ca)
	b := upstreamServer(t, http.StatusOK, "x", &cb)
	g := gateway(t, nil, a.URL, b.URL)
	_, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierFast})
	var se *llm.StatusError
	if !errors.As(err, &se) || se.Status != http.StatusBadRequest {
		t.Fatalf("err %v", err)
	}
	if cb.Load() != 0 {
		t.Fatal("a 400 must not fall through to the next provider")
	}
}

func TestAllProvidersDown(t *testing.T) {
	var ca atomic.Int32
	a := upstreamServer(t, http.StatusTooManyRequests, "", &ca)
	g := gateway(t, nil, a.URL)
	_, err := g.Complete(context.Background(), llm.Request{Tier: llm.TierFast})
	if e := apperr.As(err); e.Code != "unavailable" || !e.Retryable {
		t.Fatalf("err %+v", e)
	}
}

func TestOutputModeration(t *testing.T) {
	var ca atomic.Int32
	a := upstreamServer(t, http.StatusOK, "这里有违禁词", &ca)
	mod, _ := moderation.NewKeywords(strings.NewReader("违禁词"))
	g := gateway(t, mod, a.URL)
	_, err := g.Complete(context.Background(), llm.Request{Purpose: "report.write", Tier: llm.TierStrong})
	if e := apperr.As(err); e.Code != "content_blocked" {
		t.Fatalf("err %+v", e)
	}
}

func TestLoadConfigValidates(t *testing.T) {
	if err := (Config{Providers: []ProviderConfig{{Name: "a", BaseURL: "http://x"}},
		Routes: map[llm.Tier][]string{llm.TierStrong: {"a"}, llm.TierFast: {"b"}}}).validate(); err == nil {
		t.Fatal("unknown provider in route must fail")
	}
	if _, err := LoadConfig("../../../deploy/modelgw.example.yaml"); err != nil {
		t.Fatal(err)
	}
}
