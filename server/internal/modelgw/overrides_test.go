package modelgw

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/hwq830/zeusai/server/internal/llm"
)

type staticOverrides struct{ o *Overrides }

func (s staticOverrides) Overrides(context.Context) Overrides { return *s.o }

func TestOverrides(t *testing.T) {
	var ca, cb, cc atomic.Int32
	a := upstreamServer(t, http.StatusOK, "from a", &ca)
	b := upstreamServer(t, http.StatusServiceUnavailable, "", &cb)
	c := upstreamServer(t, http.StatusOK, "from c", &cc)
	ov := &Overrides{}
	g := gatewayWith(t, nil, []Option{WithOverrides(staticOverrides{ov})}, a.URL, b.URL, c.URL)
	ctx := context.Background()

	// No overrides: the routing file's order and model.
	resp, err := g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "a" || resp.Model != "a-strong" {
		t.Fatalf("resp %+v err %v", resp, err)
	}

	// Tier override: c with a new model, priced by the override.
	price := Price{Input: 1, Output: 2}
	ov.Tiers = map[llm.Tier]Override{llm.TierStrong: {Provider: "c", Model: "c-new", Price: &price}}
	resp, err = g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "c" || resp.Model != "c-new" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if want := int64(600*1 + 500*2); resp.CostMicros != want {
		t.Fatalf("cost %d want %d", resp.CostMicros, want)
	}
	// The fast tier is untouched.
	resp, _ = g.Complete(ctx, llm.Request{Tier: llm.TierFast})
	if resp.Provider != "a" || resp.Model != "a-fast" {
		t.Fatalf("fast resp %+v", resp)
	}

	// Purpose override goes first; when it fails the tier's chain follows (b → c, a is
	// dropped by the explicit fallback list).
	ov.Purposes = map[string]Override{"report.write": {Provider: "b", Model: "b-x"}}
	ov.Tiers = map[llm.Tier]Override{llm.TierStrong: {Provider: "b", Model: "b-strong", Fallbacks: []string{"c"}}}
	resp, err = g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "c" || resp.Model != "c-strong" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if cb.Load() != 1 {
		t.Fatalf("b should be tried once (deduplicated), got %d", cb.Load())
	}

	// Unknown providers in overrides are ignored rather than breaking the route.
	ov.Purposes = map[string]Override{"report.write": {Provider: "gone", Model: "x"}}
	ov.Tiers = nil
	resp, err = g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "a" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
}

func TestConsoleProviders(t *testing.T) {
	var ca, cx, cy atomic.Int32
	a := upstreamServer(t, http.StatusOK, "from a", &ca)
	x := upstreamServer(t, http.StatusOK, "from console", &cx)
	y := upstreamServer(t, http.StatusOK, "from moved console", &cy)
	ov := &Overrides{}
	keys := mapKeys{"a": "k", "kimi": "k"}
	g := gatewayWith(t, nil, []Option{WithKeys(keys), WithOverrides(staticOverrides{ov})}, a.URL)
	ctx := context.Background()
	strong := func() llm.Response {
		t.Helper()
		resp, err := g.Complete(ctx, llm.Request{Tier: llm.TierStrong})
		if err != nil {
			t.Fatal(err)
		}
		return resp
	}

	// A console provider is unused until a tier or purpose names it.
	kimi := ProviderConfig{Name: "kimi", BaseURL: x.URL, Models: map[llm.Tier]string{llm.TierStrong: "k3", llm.TierFast: "k3-mini"}}
	ov.Providers = []ProviderConfig{kimi}
	if r := strong(); r.Content != "from a" {
		t.Fatalf("got %+v", r)
	}
	// Named by the tier override: its default model for the tier when none is given.
	ov.Tiers = map[llm.Tier]Override{llm.TierStrong: {Provider: "kimi", Fallbacks: []string{"a"}}}
	if r := strong(); r.Content != "from console" || r.Model != "k3" {
		t.Fatalf("got %+v", r)
	}
	// Address changes are picked up; disabling (dropping it) falls back to the file.
	kimi.BaseURL = y.URL
	ov.Providers = []ProviderConfig{kimi}
	if r := strong(); r.Content != "from moved console" {
		t.Fatalf("got %+v", r)
	}
	ov.Providers = nil
	if r := strong(); r.Content != "from a" {
		t.Fatalf("got %+v", r)
	}
	// A console provider cannot shadow a file provider.
	if c := (Config{Providers: []ProviderConfig{{Name: "a"}}}).WithProviders([]ProviderConfig{{Name: "a", BaseURL: "x"}, kimi}); len(c.Providers) != 2 || c.Providers[0].BaseURL != "" {
		t.Fatalf("merged %+v", c.Providers)
	}
}
