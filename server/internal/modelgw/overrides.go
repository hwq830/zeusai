package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

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
	{"report.visual", llm.TierFast},
	{"report.review", llm.TierStrong},
	{"report.revise", llm.TierStrong},
	{"report.followup", llm.TierStrong},
	{"report.tools", llm.TierFast},
	{"template.draft", llm.TierStrong},
	{"subscription.draft", llm.TierFast},
}

// Tiers are the scopes a tier override can take.
var Tiers = []llm.Tier{llm.TierStrong, llm.TierFast}

// Override pins a provider and model for a call purpose; it is tried first, then the tier's route.
type Override struct {
	Provider string
	Model    string
}

// Overrides is the routing in effect, as kept in the database.
type Overrides struct {
	// Settings is nil until the routing exists in the database (first start, or the database
	// cannot be read before the first load); the gateway then uses the routing file.
	Settings *Settings
	Purposes map[string]Override
	// Manual are the admin prices from the pricing page, used for any provider.
	Manual map[string]Price
	// Prices are the pricing page's Bailian list prices (synced), used on Bailian endpoints.
	Prices map[string]Price
	// Providers are the enabled providers in the database.
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
	rows, err := d.Q.ListPurposeOverrides(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("model overrides reload failed", "err", err)
		}
		if d.cur == nil {
			return Overrides{}
		}
		return *d.cur
	}
	o := Overrides{Purposes: map[string]Override{}}
	for _, r := range rows {
		o.Purposes[r.Scope] = Override{Provider: r.Provider, Model: r.Model}
	}
	if row, err := d.Q.GetGatewaySettings(ctx); err == nil {
		st := ParseSettings(row.GatewaySetting)
		o.Settings = &st
	} else if !errors.Is(err, pgx.ErrNoRows) {
		if d.Log != nil {
			d.Log.Warn("gateway settings reload failed", "err", err)
		}
		if d.cur != nil {
			o.Settings = d.cur.Settings
		}
	}
	prices, err := d.Q.ListModelPrices(ctx)
	if err != nil {
		if d.Log != nil {
			d.Log.Warn("model prices reload failed", "err", err)
		}
		if d.cur != nil {
			o.Manual, o.Prices = d.cur.Manual, d.cur.Prices
		}
	} else {
		o.Manual, o.Prices = map[string]Price{}, map[string]Price{}
		for _, r := range prices {
			if p, ok := ManualPrice(r); ok {
				o.Manual[r.Model] = p
			}
			if p, ok := SyncedPrice(r); ok {
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

// ConsoleProvider turns a provider row into routing config; its key comes from the console,
// else (providers imported from the routing file) the file's environment variable.
func ConsoleProvider(r db.ModelProvider) ProviderConfig {
	return ProviderConfig{Name: r.Name, DisplayName: r.DisplayName, BaseURL: r.BaseUrl, APIKeyEnv: r.ApiKeyEnv,
		Models:  map[llm.Tier]string{llm.TierStrong: r.ModelStrong, llm.TierFast: r.ModelFast},
		Timeout: time.Duration(r.TimeoutSeconds) * time.Second}
}

// ManualPrice is a pricing-page row's admin price.
func ManualPrice(r db.ListModelPricesRow) (Price, bool) {
	if r.ManualInput == nil {
		return Price{}, false
	}
	return Price{Input: *r.ManualInput, Output: deref(r.ManualOutput), Cached: deref(r.ManualCached)}, true
}

// SyncedPrice is a pricing-page row's Bailian list price: the first synced tier.
func SyncedPrice(r db.ListModelPricesRow) (Price, bool) {
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
	ov              *Overrides
}

// chain resolves the ordered attempts for a call: the purpose's choice first, then the tier's
// route (from the database, or the routing file before the first import). Each provider
// appears once, and unknown or disabled providers are dropped.
func (g *Gateway) chain(ctx context.Context, tier llm.Tier, purpose string) []attempt {
	var ov Overrides
	if g.overrides != nil {
		ov = g.overrides.Overrides(ctx)
	}
	if ov.Settings != nil {
		g.breaker.set(ov.Settings.BreakerFailures, ov.Settings.BreakerCooldown)
	}
	var out []attempt
	seen := map[string]bool{}
	add := func(a attempt) {
		up, ok := g.upstreamFor(ov, a.provider)
		if !ok || seen[a.provider] {
			return
		}
		a.up, a.ov = up, &ov
		seen[a.provider] = true
		out = append(out, a)
	}
	if p, ok := ov.Purposes[purpose]; ok {
		add(attempt{provider: p.Provider, model: p.Model})
	}
	var steps []RouteStep
	if ov.Settings != nil {
		steps = ov.Settings.Routes[tier]
	} else {
		for _, n := range g.routes[tier] {
			steps = append(steps, RouteStep{Provider: n})
		}
	}
	for _, s := range steps {
		add(attempt{provider: s.Provider, model: s.Model})
	}
	return out
}
