package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
)

// RouteStep is one provider in a tier's route; Model "" means the provider's default model
// for the tier.
type RouteStep struct {
	Provider string `json:"provider"`
	Model    string `json:"model"`
}

// Settings is the routing kept in gateway_settings: the providers each tier tries, in order,
// and the circuit breaker.
type Settings struct {
	Routes          map[llm.Tier][]RouteStep
	BreakerFailures int
	BreakerCooldown time.Duration
}

const (
	DefaultBreakerFailures = 3
	DefaultBreakerCooldown = 30 * time.Second
)

// ParseSettings reads a gateway_settings row.
func ParseSettings(r db.GatewaySetting) Settings {
	s := Settings{Routes: map[llm.Tier][]RouteStep{}, BreakerFailures: int(r.BreakerFailures),
		BreakerCooldown: time.Duration(r.BreakerCooldownSeconds) * time.Second}
	var raw map[llm.Tier][]RouteStep
	_ = json.Unmarshal(r.Routes, &raw)
	for _, t := range Tiers {
		s.Routes[t] = raw[t]
	}
	return s
}

// RoutesJSON encodes routes for gateway_settings.routes, every tier present.
func RoutesJSON(routes map[llm.Tier][]RouteStep) json.RawMessage {
	out := map[llm.Tier][]RouteStep{}
	for _, t := range Tiers {
		out[t] = routes[t]
		if out[t] == nil {
			out[t] = []RouteStep{}
		}
	}
	b, _ := json.Marshal(out)
	return b
}

// FileRoutes turns a routing file's routes into settings routes (each provider's default model).
func FileRoutes(cfg Config) map[llm.Tier][]RouteStep {
	out := map[llm.Tier][]RouteStep{}
	for _, t := range Tiers {
		for _, n := range cfg.Routes[t] {
			out[t] = append(out[t], RouteStep{Provider: n})
		}
	}
	return out
}

// Bootstrap moves the routing file into the database the first time any process starts with
// the gateway: its providers (with their key variables), its prices (as admin prices on the
// pricing page) and its routes, with any tier choice made on the old 模型指定 page put first.
// It does nothing once gateway_settings exists, so the file is read only once.
func Bootstrap(ctx context.Context, q *db.Queries, cfg Config, log *slog.Logger) error {
	if _, err := q.GetGatewaySettings(ctx); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if len(cfg.Providers) == 0 {
		return nil
	}
	for _, p := range cfg.Providers {
		timeout := int32(p.Timeout / time.Second)
		if timeout <= 0 {
			timeout = 120
		}
		name := p.DisplayName
		if name == "" {
			name = KnownNames[p.Name]
		}
		if name == "" {
			name = p.Name
		}
		n, err := q.ImportModelProvider(ctx, db.ImportModelProviderParams{Name: p.Name, DisplayName: name, BaseUrl: p.BaseURL,
			ModelStrong: p.Models[llm.TierStrong], ModelFast: p.Models[llm.TierFast], TimeoutSeconds: timeout, ApiKeyEnv: p.APIKeyEnv})
		if err != nil {
			return fmt.Errorf("import provider %s: %w", p.Name, err)
		}
		if n == 0 && log != nil {
			log.Warn("routing file provider not imported: the console already has one with this name", "provider", p.Name)
		}
		for model, pr := range p.Prices {
			in, out, cached := pr.Input, pr.Output, pr.Cached
			if err := q.ImportModelPrice(ctx, db.ImportModelPriceParams{Model: model, ManualInput: &in, ManualOutput: &out, ManualCached: &cached}); err != nil {
				return fmt.Errorf("import price %s: %w", model, err)
			}
		}
	}
	routes := FileRoutes(cfg)
	if rows, err := q.ListModelOverrides(ctx); err == nil {
		for _, r := range rows {
			t := llm.Tier(r.Scope)
			if t != llm.TierStrong && t != llm.TierFast {
				continue
			}
			steps := []RouteStep{{Provider: r.Provider, Model: r.Model}}
			fallbacks := cfg.Routes[t]
			if r.Fallbacks != nil {
				fallbacks = r.Fallbacks
			}
			for _, n := range fallbacks {
				if n != r.Provider {
					steps = append(steps, RouteStep{Provider: n})
				}
			}
			routes[t] = steps
		}
	}
	failures, cooldown := cfg.BreakerFailures, cfg.BreakerCooldown
	if failures <= 0 {
		failures = DefaultBreakerFailures
	}
	if cooldown <= 0 {
		cooldown = DefaultBreakerCooldown
	}
	n, err := q.InsertGatewaySettings(ctx, db.InsertGatewaySettingsParams{Routes: RoutesJSON(routes),
		BreakerFailures: int32(failures), BreakerCooldownSeconds: int32(cooldown / time.Second), ImportedFromFile: true})
	if err != nil {
		return fmt.Errorf("import routes: %w", err)
	}
	if n == 0 {
		return nil // another process imported first
	}
	if log != nil {
		log.Info("model routing imported from the routing file into the database")
	}
	snap, err := LoadSnapshot(ctx, q)
	if err != nil {
		return err
	}
	_, err = q.InsertRoutingVersion(ctx, db.InsertRoutingVersionParams{Snapshot: snap, Action: "import"})
	return err
}

// KnownNames are display names for providers the routing file usually names.
var KnownNames = map[string]string{"qwen": "通义千问", "deepseek": "DeepSeek"}

// Snapshot is a routing version: the settings and the per-purpose choices.
type Snapshot struct {
	Routes                 map[llm.Tier][]RouteStep `json:"routes"`
	BreakerFailures        int                      `json:"breakerFailures"`
	BreakerCooldownSeconds int                      `json:"breakerCooldownSeconds"`
	Purposes               map[string]RouteStep     `json:"purposes"`
}

// LoadSnapshot reads the current routing as a version snapshot.
func LoadSnapshot(ctx context.Context, q *db.Queries) (json.RawMessage, error) {
	row, err := q.GetGatewaySettings(ctx)
	if err != nil {
		return nil, err
	}
	st := ParseSettings(row.GatewaySetting)
	s := Snapshot{Routes: map[llm.Tier][]RouteStep{}, BreakerFailures: st.BreakerFailures,
		BreakerCooldownSeconds: int(st.BreakerCooldown / time.Second), Purposes: map[string]RouteStep{}}
	json.Unmarshal(RoutesJSON(st.Routes), &s.Routes)
	ps, err := q.ListPurposeOverrides(ctx)
	if err != nil {
		return nil, err
	}
	for _, p := range ps {
		s.Purposes[p.Scope] = RouteStep{Provider: p.Provider, Model: p.Model}
	}
	b, _ := json.Marshal(s)
	return b, nil
}
