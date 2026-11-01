package modelgw

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/llm"
)

type staticOverrides struct{ o *Overrides }

func (s staticOverrides) Overrides(context.Context) Overrides { return *s.o }

// dbProvider is what the database would hold for a test upstream named name.
func dbProvider(name, url string) ProviderConfig {
	return ProviderConfig{Name: name, BaseURL: url, APIKeyEnv: "TEST_KEY",
		Models: map[llm.Tier]string{llm.TierStrong: name + "-strong", llm.TierFast: name + "-fast"}}
}

func TestOverrides(t *testing.T) {
	var ca, cb, cc atomic.Int32
	a := upstreamServer(t, http.StatusOK, "from a", &ca)
	b := upstreamServer(t, http.StatusServiceUnavailable, "", &cb)
	c := upstreamServer(t, http.StatusOK, "from c", &cc)
	ov := &Overrides{}
	g := gatewayWith(t, nil, []Option{WithOverrides(staticOverrides{ov})}, a.URL, b.URL, c.URL)
	ctx := context.Background()

	// Before the routing is in the database: the routing file's order, model and price.
	resp, err := g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "a" || resp.Model != "a-strong" {
		t.Fatalf("resp %+v err %v", resp, err)
	}

	// Database routing: c with a chosen model first, then a; priced by the pricing page.
	ov.Providers = []ProviderConfig{dbProvider("a", a.URL), dbProvider("b", b.URL), dbProvider("c", c.URL)}
	ov.Settings = &Settings{Routes: map[llm.Tier][]RouteStep{
		llm.TierStrong: {{Provider: "c", Model: "c-new"}, {Provider: "a"}},
		llm.TierFast:   {{Provider: "a"}},
	}, BreakerFailures: 1, BreakerCooldown: time.Minute}
	ov.Manual = map[string]Price{"c-new": {Input: 1, Output: 2}}
	resp, err = g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "c" || resp.Model != "c-new" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if want := int64(600*1 + 500*2); resp.CostMicros != want {
		t.Fatalf("cost %d want %d", resp.CostMicros, want)
	}
	resp, _ = g.Complete(ctx, llm.Request{Tier: llm.TierFast})
	if resp.Provider != "a" || resp.Model != "a-fast" {
		t.Fatalf("fast resp %+v", resp)
	}

	// The purpose's choice goes first; when it fails the tier's route follows, each provider once.
	ov.Purposes = map[string]Override{"report.write": {Provider: "b", Model: "b-x"}}
	ov.Settings.Routes[llm.TierStrong] = []RouteStep{{Provider: "b"}, {Provider: "c"}}
	resp, err = g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "c" || resp.Model != "c-strong" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
	if cb.Load() != 1 {
		t.Fatalf("b should be tried once (deduplicated), got %d", cb.Load())
	}
	// The breaker threshold comes from the settings: one failure opened b, so it is skipped.
	if _, err := g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"}); err != nil || cb.Load() != 1 {
		t.Fatalf("b called %d times, err %v", cb.Load(), err)
	}

	// Unknown providers are ignored rather than breaking the route; providers the database no
	// longer has are not used even though the routing file still lists them.
	ov.Purposes = map[string]Override{"report.write": {Provider: "gone", Model: "x"}}
	ov.Settings.Routes[llm.TierStrong] = []RouteStep{{Provider: "a"}, {Provider: "c"}}
	ov.Providers = []ProviderConfig{dbProvider("c", c.URL)}
	resp, err = g.Complete(ctx, llm.Request{Tier: llm.TierStrong, Purpose: "report.write"})
	if err != nil || resp.Provider != "c" {
		t.Fatalf("resp %+v err %v", resp, err)
	}
}

func TestConsoleProviders(t *testing.T) {
	var cx, cy atomic.Int32
	x := upstreamServer(t, http.StatusOK, "from console", &cx)
	y := upstreamServer(t, http.StatusOK, "from moved console", &cy)
	ov := &Overrides{}
	keys := mapKeys{"kimi": "k", "a": "k"}
	// No routing file at all: everything comes from the database.
	g, err := New(Config{}, nil, nil, WithKeys(keys), WithOverrides(staticOverrides{ov}))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	strong := func() (llm.Response, error) {
		return g.Complete(ctx, llm.Request{Tier: llm.TierStrong})
	}
	if _, err := strong(); err == nil {
		t.Fatal("no routing should fail")
	}

	kimi := ProviderConfig{Name: "kimi", BaseURL: x.URL, Models: map[llm.Tier]string{llm.TierStrong: "k3", llm.TierFast: "k3-mini"}}
	ov.Providers = []ProviderConfig{kimi}
	ov.Settings = &Settings{Routes: map[llm.Tier][]RouteStep{llm.TierStrong: {{Provider: "kimi"}}}}
	if r, err := strong(); err != nil || r.Content != "from console" || r.Model != "k3" {
		t.Fatalf("got %+v %v", r, err)
	}
	// Address changes are picked up; disabling (dropping it) leaves nothing to call.
	kimi.BaseURL = y.URL
	ov.Providers = []ProviderConfig{kimi}
	if r, err := strong(); err != nil || r.Content != "from moved console" {
		t.Fatalf("got %+v %v", r, err)
	}
	ov.Providers = nil
	if _, err := strong(); err == nil {
		t.Fatal("disabled provider was called")
	}
}
