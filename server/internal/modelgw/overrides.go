package modelgw

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
)

// Purpose is a call site whose model can be chosen in the console.
type Purpose struct {
	Name string
	Tier llm.Tier
}

// Purposes lists every call site the workflows make, with the tier it asks for.
var Purposes = []Purpose{
	{"briefing.section", llm.TierFast},
	{"briefing.headline", llm.TierFast},
	{"report.plan", llm.TierStrong},
	{"report.research", llm.TierFast},
	{"report.write", llm.TierStrong},
	{"report.followup", llm.TierStrong},
	{"report.tools", llm.TierFast},
	{"template.draft", llm.TierStrong},
	{"subscription.draft", llm.TierFast},
}

// Tiers are the scopes a tier override can take.
var Tiers = []llm.Tier{llm.TierStrong, llm.TierFast}

// Override pins a provider and model for a tier or purpose. Fallbacks (tiers only) replaces
// the tier's fallback order when non-nil; nil keeps the routing file's order. Price, when
// set, is used for models the routing file has no price for.
type Override struct {
	Provider  string
	Model     string
	Fallbacks []string
	Price     *Price
}

// Overrides are the console choices in effect, keyed by tier and purpose.
type Overrides struct {
	Tiers    map[llm.Tier]Override
	Purposes map[string]Override
	// Prices are the pricing page's Bailian prices by model, used on Bailian endpoints.
	Prices map[string]Price
	// Providers are the enabled providers added in the console.
	Providers []ProviderConfig
}

// OverrideSource supplies the current overrides per call.
type OverrideSource interface {
	Overrides(ctx context.Context) Overrides
}

// WithOverrides layers console model choices over the routing file.
func WithOverrides(src OverrideSource) Option { return func(o *options) { o.overrides = src } }

// DBOverrides reads model_overrides, reloading at most every Every (default one minute) so a
// change in the console takes effect without a restart; a failed reload keeps the last set.
type DBOverrides struct {
	Q     *db.Queries
	Log   *slog.Logger
	Every time.Duration

	mu     sync.Mutex
	cur    *Overrides
	loaded time.Time
}

func (d *DBOverrides) Overrides(ctx context.Context) Overrides {
	if d == nil || d.Q == nil {
		return Overrides{}
	}
	every := d.Every
	if every <= 0 {
		every = time.Minute
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.cur != nil && time.Since(d.loaded) < every {
		return *d.cur
	}
	rows, err := d.Q.ListModelOverrides(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("model overrides reload failed", "err", err)
		}
		if d.cur == nil {
			return Overrides{}
		}
		return *d.cur
	}
	o := Overrides{Tiers: map[llm.Tier]Override{}, Purposes: map[string]Override{}}
	for _, r := range rows {
		ov := Override{Provider: r.Provider, Model: r.Model, Fallbacks: r.Fallbacks}
		if r.PriceInput != nil || r.PriceOutput != nil || r.PriceCached != nil {
			ov.Price = &Price{Input: deref(r.PriceInput), Output: deref(r.PriceOutput), Cached: deref(r.PriceCached)}
		}
		if t := llm.Tier(r.Scope); t == llm.TierStrong || t == llm.TierFast {
			o.Tiers[t] = ov
		} else {
			o.Purposes[r.Scope] = ov
		}
	}
	prices, err := d.Q.ListModelPrices(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("model prices reload failed", "err", err)
		}
		if d.cur != nil {
			o.Prices = d.cur.Prices
		}
	} else {
		o.Prices = map[string]Price{}
		for _, r := range prices {
			if p, ok := StoredPrice(r); ok {
				o.Prices[r.Model] = p
			}
		}
	}
	provs, err := d.Q.ListModelProviders(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("model providers reload failed", "err", err)
		}
		if d.cur != nil {
			o.Providers = d.cur.Providers
		}
	} else {
		for _, r := range provs {
			if r.ModelProvider.Enabled {
				o.Providers = append(o.Providers, ConsoleProvider(r.ModelProvider))
			}
		}
	}
	d.cur, d.loaded = &o, time.Now()
	return o
}

// ConsoleProvider turns a provider added in the console into routing config; its key comes
// only from the console (no environment variable).
func ConsoleProvider(r db.ModelProvider) ProviderConfig {
	return ProviderConfig{Name: r.Name, DisplayName: r.DisplayName, BaseURL: r.BaseUrl,
		Models:  map[llm.Tier]string{llm.TierStrong: r.ModelStrong, llm.TierFast: r.ModelFast},
		Timeout: time.Duration(r.TimeoutSeconds) * time.Second}
}

// StoredPrice is the metered price of a pricing-page row: the admin's price, else the first
// synced tier.
func StoredPrice(r db.ListModelPricesRow) (Price, bool) {
	if r.ManualInput != nil {
		return Price{Input: *r.ManualInput, Output: deref(r.ManualOutput), Cached: deref(r.ManualCached)}, true
	}
	var tiers []PriceTier
	if json.Unmarshal(r.Tiers, &tiers) == nil && len(tiers) > 0 {
		return tiers[0].Price, true
	}
	return Price{}, false
}

func deref(f *float64) float64 {
	if f == nil {
		return 0
	}
	return *f
}

// attempt is one provider call in a chain; model "" means the provider's model for the tier.
type attempt struct {
	provider, model string
	up              upstream
	price           *Price
	// bailian holds the pricing page's prices, consulted on Bailian endpoints.
	bailian map[string]Price
}

// chain resolves the ordered attempts for a call: a purpose override first, then the tier's
// override (or the routing file's first provider), then the fallbacks. Each provider appears
// once, and providers missing from the routing file are dropped.
func (g *Gateway) chain(ctx context.Context, tier llm.Tier, purpose string) []attempt {
	var ov Overrides
	if g.overrides != nil {
		ov = g.overrides.Overrides(ctx)
	}
	var out []attempt
	seen := map[string]bool{}
	add := func(a attempt) {
		up, ok := g.upstreamFor(ov, a.provider)
		if !ok || seen[a.provider] {
			return
		}
		a.up = up
		seen[a.provider] = true
		a.bailian = ov.Prices
		out = append(out, a)
	}
	if p, ok := ov.Purposes[purpose]; ok {
		add(attempt{provider: p.Provider, model: p.Model, price: p.Price})
	}
	t, ok := ov.Tiers[tier]
	if !ok {
		for _, n := range g.routes[tier] {
			add(attempt{provider: n})
		}
		return out
	}
	add(attempt{provider: t.Provider, model: t.Model, price: t.Price})
	fallbacks := t.Fallbacks
	if fallbacks == nil {
		fallbacks = g.routes[tier]
	}
	for _, n := range fallbacks {
		add(attempt{provider: n})
	}
	return out
}
