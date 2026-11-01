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
