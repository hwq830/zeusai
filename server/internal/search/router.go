package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Provider is one configured search provider: a name (its keys are saved under it), the API
// it speaks, and where to call it.
type Provider struct {
	Name, Protocol, BaseURL, KeyEnv string
}

// KeySource supplies provider keys (secrets.Keys: console key first, then the env variable).
type KeySource interface {
	Key(ctx context.Context, provider, envName string) string
	BaseURL(ctx context.Context, provider string) string
}

// Routing is an immutable snapshot shared by all content regions.
type Routing struct {
	Default []Provider
	Regions map[string][]Provider
}

func (c Routing) Chain(country string) []Provider {
	region := strings.TrimSpace(country)
	if strings.EqualFold(region, "global") {
		return c.Default
	}
	if strings.EqualFold(region, "other") {
		region = "other"
	} else {
		region = strings.ToUpper(region)
	}
	if chain, ok := c.Regions[region]; ok {
		return chain
	}
	return c.Default
}

// Router selects a regional chain and tries providers within a bounded time budget.
// Configuration reloads about once a minute without restarting.
type Router struct {
	LoadRouting func(context.Context) (Routing, error)
	routing     *Routing
	// Per-attempt and total search budgets; defaults 8s and 30s.
	AttemptTimeout time.Duration
	TotalTimeout   time.Duration
	// Load returns the chain, primary first. On error the last good chain is kept, and
	// Fallback is used when there is none yet.
	Load     func(ctx context.Context) ([]Provider, error)
	Fallback []Provider
	Keys     KeySource
	HTTP     *http.Client
	Log      *slog.Logger
	Every    time.Duration // reload interval, default 1 minute
	Cooldown time.Duration // how long a failed provider is skipped while others remain, default 30s
	Now      func() time.Time

	mu       sync.Mutex
	chain    []Provider
	loaded   time.Time
	failedAt map[string]time.Time
}

func (r *Router) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

func (r *Router) current(ctx context.Context, country string) []Provider {
	every := r.Every
	if every <= 0 {
		every = time.Minute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.LoadRouting != nil {
		if r.routing == nil || r.now().Sub(r.loaded) >= every {
			c, err := r.LoadRouting(ctx)
			if err == nil {
				r.routing, r.loaded = &c, r.now()
			} else if r.Log != nil {
				r.Log.Warn("search routing reload failed")
			}
		}
		if r.routing != nil {
			return r.routing.Chain(country)
		}
		return r.Fallback
	}
	if r.chain != nil && r.now().Sub(r.loaded) < every {
		return r.chain
	}
	if r.Load != nil {
		c, err := r.Load(ctx)
		if err == nil {
			r.chain, r.loaded = c, r.now()
			return r.chain
		}
		if r.Log != nil {
			r.Log.Warn("search routing reload failed", "err", err)
		}
	}
	if r.chain != nil {
		return r.chain
	}
	return r.Fallback
}

// Invalidate forces the next search to reload the chain.
func (r *Router) Invalidate() {
	r.mu.Lock()
	r.loaded = time.Time{}
	r.mu.Unlock()
}

func (r *Router) coolingDown(name string) bool {
	cd := r.Cooldown
	if cd <= 0 {
		cd = 30 * time.Second
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	t, ok := r.failedAt[name]
	return ok && r.now().Sub(t) < cd
}

func (r *Router) mark(name string, failed bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !failed {
		delete(r.failedAt, name)
		return
	}
	if r.failedAt == nil {
		r.failedAt = map[string]time.Time{}
	}
	r.failedAt[name] = r.now()
}

// ErrNoProvider means the chain is empty or no provider in it has a key.
var ErrNoProvider = errors.New("search: no search provider is configured")

func (r *Router) Search(ctx context.Context, q Query) ([]Result, error) {
	total := r.TotalTimeout
	if total <= 0 {
		total = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, total)
	defer cancel()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	region := q.Region
	if region == "" {
		region = q.Country
	}
	if strings.EqualFold(region, "global") {
		q.Country = ""
	}
	chain := r.current(ctx, region)
	// Providers that failed recently go last, so a dead primary does not slow every search.
	ordered := make([]Provider, 0, len(chain))
	var cooling []Provider
	for _, p := range chain {
		if r.coolingDown(p.Name) {
			cooling = append(cooling, p)
		} else {
			ordered = append(ordered, p)
		}
	}
	ordered = append(ordered, cooling...)
	var errs []error
	attempts, empties, maxAttempts := 0, 0, 5
	for _, p := range ordered {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if attempts >= maxAttempts {
			break
		}
		var key, base string
		if r.Keys != nil {
			key, base = r.Keys.Key(ctx, p.Name, p.KeyEnv), r.Keys.BaseURL(ctx, p.Name)
		}
		if key == "" {
			errs = append(errs, fmt.Errorf("%s: no API key", p.Name))
			continue
		}
		if base == "" {
			base = p.BaseURL
		}
		s, err := New(p.Protocol, key, base, r.HTTP)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		attempt := r.AttemptTimeout
		if attempt <= 0 {
			attempt = 8 * time.Second
		}
		callCtx, stop := context.WithTimeout(ctx, attempt)
		started := time.Now()
		attempts++
		res, err := s.Search(callCtx, q)
		stop()
		outcome := "success"
		if err != nil {
			outcome = "error"
		} else if len(res) == 0 {
			outcome = "empty"
		}
		if r.Log != nil {
			r.Log.Info("search attempt", "region", region, "country", q.Country, "provider", p.Name, "attempt", attempts, "outcome", outcome, "results", len(res), "duration_ms", time.Since(started).Milliseconds())
		}
		if err == nil {
			r.mark(p.Name, false)
			if len(res) > 0 {
				return res, nil
			}
			empties++
			if empties == 1 {
				maxAttempts = min(maxAttempts, attempts+1)
			}
			continue
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		r.mark(p.Name, true)
		errs = append(errs, err)
		if r.Log != nil {
			r.Log.Warn("search provider failed; trying the next one", "provider", p.Name)
		}
	}
	if empties > 0 {
		return []Result{}, nil
	}
	if len(errs) == 0 {
		return nil, ErrNoProvider
	}
	return nil, errors.Join(errs...)
}
