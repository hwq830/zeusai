// Package modelgw routes model calls to upstream providers: an ordered fallback chain per
// tier, a small circuit breaker per provider, per-model pricing for cost metering, and output
// moderation. It implements llm.Provider, so the orchestrator does not know which vendor
// answered.
package modelgw

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
)

// Price is CNY per million tokens.
type Price struct {
	Input  float64 `yaml:"input"`
	Output float64 `yaml:"output"`
	Cached float64 `yaml:"cached"`
}

type ProviderConfig struct {
	Name    string `yaml:"name"`
	BaseURL string `yaml:"base_url"`
	// APIKeyEnv names the environment variable holding the key; keys never live in the file.
	APIKeyEnv string              `yaml:"api_key_env"`
	Models    map[llm.Tier]string `yaml:"models"`
	Prices    map[string]Price    `yaml:"prices"` // keyed by model name
	Timeout   time.Duration       `yaml:"timeout"`
}

type Config struct {
	Providers []ProviderConfig `yaml:"providers"`
	// Routes lists provider names per tier, tried in order.
	Routes map[llm.Tier][]string `yaml:"routes"`
	// BreakerFailures consecutive retryable failures open a provider for BreakerCooldown.
	BreakerFailures int           `yaml:"breaker_failures"`
	BreakerCooldown time.Duration `yaml:"breaker_cooldown"`
}

// LoadConfig reads a YAML routing file (see deploy/modelgw.example.yaml).
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	var c Config
	if err := yaml.Unmarshal(b, &c); err != nil {
		return Config{}, fmt.Errorf("modelgw config: %w", err)
	}
	return c, c.validate()
}

func (c Config) validate() error {
	names := map[string]bool{}
	for _, p := range c.Providers {
		if p.Name == "" || p.BaseURL == "" {
			return errors.New("modelgw config: provider needs name and base_url")
		}
		names[p.Name] = true
	}
	for _, t := range []llm.Tier{llm.TierStrong, llm.TierFast} {
		if len(c.Routes[t]) == 0 {
			return fmt.Errorf("modelgw config: no route for tier %q", t)
		}
		for _, n := range c.Routes[t] {
			if !names[n] {
				return fmt.Errorf("modelgw config: route %q names unknown provider %q", t, n)
			}
		}
	}
	return nil
}

type upstream struct {
	llm.Provider
	prices map[string]Price
	// hasKey reports whether a key is configured right now; nil (test providers) means yes.
	hasKey func(context.Context) bool
}

// KeySource resolves a provider's API key per call: a key saved in the admin console, else
// the environment variable named in the routing file.
type KeySource interface {
	Key(ctx context.Context, provider, envName string) string
}

type envKeys struct{}

func (envKeys) Key(_ context.Context, _, envName string) string {
	if envName == "" {
		return ""
	}
	return os.Getenv(envName)
}

type Option func(*options)

type options struct {
	keys      KeySource
	overrides OverrideSource
}

// WithKeys makes the gateway look keys up through ks on every call instead of reading the
// environment once.
func WithKeys(ks KeySource) Option { return func(o *options) { o.keys = ks } }

type Gateway struct {
	ups       map[string]upstream
	routes    map[llm.Tier][]string
	overrides OverrideSource
	mod       moderation.Checker
	breaker   *breaker
}

// New builds a gateway. Providers can be overridden (tests) by passing them in extra.
// Providers with no key at call time are skipped, so the route falls through to the next one.
func New(cfg Config, mod moderation.Checker, extra map[string]llm.Provider, opts ...Option) (*Gateway, error) {
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	o := options{keys: envKeys{}}
	for _, opt := range opts {
		opt(&o)
	}
	g := &Gateway{ups: map[string]upstream{}, routes: cfg.Routes, overrides: o.overrides, mod: mod,
		breaker: newBreaker(cfg.BreakerFailures, cfg.BreakerCooldown)}
	for _, pc := range cfg.Providers {
		up := upstream{Provider: extra[pc.Name], prices: pc.Prices}
		if up.Provider == nil {
			timeout := pc.Timeout
			if timeout == 0 {
				timeout = 120 * time.Second
			}
			name, env, keys := pc.Name, pc.APIKeyEnv, o.keys
			key := func(ctx context.Context) string { return keys.Key(ctx, name, env) }
			up.Provider = llm.OpenAICompat{BaseURL: pc.BaseURL, KeyFunc: key, Name: pc.Name,
				Models: pc.Models, HTTP: &http.Client{Timeout: timeout}}
			up.hasKey = func(ctx context.Context) bool { return key(ctx) != "" }
		}
		g.ups[pc.Name] = up
	}
	return g, nil
}

func (g *Gateway) Complete(ctx context.Context, req llm.Request) (llm.Response, error) {
	tier := req.Tier
	if tier == "" {
		tier = llm.TierStrong
	}
	var lastErr error
	for _, a := range g.chain(ctx, tier, req.Purpose) {
		name := a.provider
		if !g.breaker.allow(name) {
			continue
		}
		up := g.ups[name]
		if up.hasKey != nil && !up.hasKey(ctx) {
			if lastErr == nil {
				lastErr = fmt.Errorf("%s: no API key configured", name)
			}
			continue
		}
		call := req
		call.Model = a.model
		resp, err := up.Complete(ctx, call)
		if err != nil {
			if ctx.Err() != nil {
				return llm.Response{}, ctx.Err()
			}
			if !retryable(err) {
				return llm.Response{}, err
			}
			g.breaker.failure(name)
			lastErr = err
			continue
		}
		g.breaker.success(name)
		resp.CostMicros = cost(a.priceFor(up.prices, resp.Model), resp.Usage)
		if g.mod != nil {
			verdict, err := g.mod.Check(ctx, moderation.Output, resp.Content)
			if err != nil {
				return llm.Response{}, err
			}
			if verdict.Blocked {
				return llm.Response{}, apperr.ContentBlocked().WithDetails(map[string]any{"purpose": req.Purpose})
			}
		}
		return resp, nil
	}
	if lastErr == nil {
		lastErr = errors.New("all providers are cooling down")
	}
	return llm.Response{}, apperr.Unavailable("模型服务繁忙，请稍后再试").WithCause(lastErr)
}

// priceFor prefers the routing file's price for the model that answered, then the price set
// with an override, then the file's price for the model asked for.
func (a attempt) priceFor(prices map[string]Price, answered string) Price {
	if p, ok := prices[answered]; ok {
		return p
	}
	if a.price != nil {
		return *a.price
	}
	return prices[a.model]
}

func retryable(err error) bool {
	var se *llm.StatusError
	if errors.As(err, &se) {
		return se.Retryable()
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	// Transport errors (connection refused, reset, EOF) are worth another provider.
	return !errors.Is(err, context.Canceled)
}

func cost(p Price, u llm.Usage) int64 {
	uncached := u.InputTokens - u.CachedTokens
	if uncached < 0 {
		uncached = 0
	}
	// CNY per million tokens == micro-CNY per token.
	return int64(p.Input*float64(uncached) + p.Cached*float64(u.CachedTokens) + p.Output*float64(u.OutputTokens))
}

type breaker struct {
	mu        sync.Mutex
	threshold int
	cooldown  time.Duration
	fails     map[string]int
	openUntil map[string]time.Time
	now       func() time.Time
}

func newBreaker(threshold int, cooldown time.Duration) *breaker {
	if threshold <= 0 {
		threshold = 3
	}
	if cooldown <= 0 {
		cooldown = 30 * time.Second
	}
	return &breaker{threshold: threshold, cooldown: cooldown, fails: map[string]int{},
		openUntil: map[string]time.Time{}, now: time.Now}
}

func (b *breaker) allow(name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !b.now().Before(b.openUntil[name])
}

func (b *breaker) failure(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails[name]++
	if b.fails[name] >= b.threshold {
		b.openUntil[name] = b.now().Add(b.cooldown)
		b.fails[name] = 0
	}
}

func (b *breaker) success(name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails[name] = 0
}
