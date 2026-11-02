package search

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
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

// Router sends each search to the first provider of the current chain (primary, then the
// fallbacks in order) and moves on when one fails. The chain is reloaded about once a minute,
// so a switch in the admin console needs no restart.
type Router struct {
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

func (r *Router) current(ctx context.Context) []Provider {
	every := r.Every
	if every <= 0 {
		every = time.Minute
	}
	r.mu.Lock()
	defer r.mu.Unlock()
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
	chain := r.current(ctx)
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
	for _, p := range ordered {
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
		res, err := s.Search(ctx, q)
		if err == nil {
			r.mark(p.Name, false)
			return res, nil
		}
		if ctx.Err() != nil {
			return nil, err
		}
		r.mark(p.Name, true)
		errs = append(errs, err)
		if r.Log != nil {
			r.Log.Warn("search provider failed; trying the next one", "provider", p.Name, "err", err)
		}
	}
	if len(errs) == 0 {
		return nil, ErrNoProvider
	}
	return nil, errors.Join(errs...)
}
