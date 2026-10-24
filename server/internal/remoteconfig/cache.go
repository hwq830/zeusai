package remoteconfig

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/hwq830/zeusai/server/internal/db"
)

// Channel carries the new version number whenever a release is published.
const Channel = "zeus:config:published"

// ParseSnapshot decodes a release's snapshot column.
func ParseSnapshot(version int64, raw json.RawMessage) (*Snapshot, error) {
	s := &Snapshot{Version: version}
	if err := json.Unmarshal(raw, s); err != nil {
		return nil, err
	}
	if s.Keys == nil {
		s.Keys = map[string]Key{}
	}
	return s, nil
}

// Cache holds the newest published snapshot in memory. Reads never touch the database, and
// when PostgreSQL or Redis is down the last loaded version keeps serving.
type Cache struct {
	Q     *db.Queries
	Redis *redis.Client // nil: rely on polling only
	Log   *slog.Logger
	// Poll is the fallback interval for noticing a new version; default 30s.
	Poll time.Duration
	// Region is filled into every evaluation context (china | international).
	Region string

	cur atomic.Pointer[Snapshot]
}

// Current returns the loaded snapshot; before the first release it is empty with version 0.
func (c *Cache) Current() *Snapshot {
	if s := c.cur.Load(); s != nil {
		return s
	}
	return &Snapshot{Keys: map[string]Key{}}
}

// Version is the loaded version, 0 before the first release.
func (c *Cache) Version() int64 { return c.Current().Version }

// Load reads the newest published release.
func (c *Cache) Load(ctx context.Context) error {
	r, err := c.Q.LatestPublishedConfig(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if cur := c.cur.Load(); cur != nil && r.Version != nil && cur.Version == *r.Version {
		return nil
	}
	s, err := ParseSnapshot(*r.Version, r.Snapshot)
	if err != nil {
		return err
	}
	c.cur.Store(s)
	if c.Log != nil {
		c.Log.Info("remote config loaded", "version", s.Version, "keys", len(s.Keys))
	}
	return nil
}

// Run keeps the cache fresh until ctx ends: it reloads on every published notice and polls
// the version as a fallback for missed notices.
func (c *Cache) Run(ctx context.Context) {
	poll := c.Poll
	if poll <= 0 {
		poll = 30 * time.Second
	}
	var notices <-chan *redis.Message
	if c.Redis != nil {
		sub := c.Redis.Subscribe(ctx, Channel)
		defer sub.Close()
		notices = sub.Channel()
	}
	t := time.NewTicker(poll)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case m, ok := <-notices:
			if !ok {
				notices = nil
				continue
			}
			if v, _ := strconv.ParseInt(m.Payload, 10, 64); v > 0 && v == c.Version() {
				continue
			}
			c.reload(ctx)
		case <-t.C:
			v, err := c.Q.LatestConfigVersion(ctx)
			if err == nil && v != c.Version() {
				c.reload(ctx)
			}
		}
	}
}

func (c *Cache) reload(ctx context.Context) {
	if err := c.Load(ctx); err != nil && c.Log != nil {
		c.Log.Warn("remote config reload failed; keeping current version", "version", c.Version(), "err", err)
	}
}

// For server-side reads: the user's own context without request headers.
func UserContext(u db.User, region string) Context {
	t := u.CreatedAt
	return Context{UserID: u.ID.String(), Plan: u.PlanCode, Region: region, RegisteredAt: &t}
}

// Bool reads a bool key for c, falling back to def when the key is missing or malformed.
func (c *Cache) Bool(ctx Context, key string, def bool) bool {
	return read(c, ctx, key, def)
}

// Int reads an int key for ctx.
func (c *Cache) Int(ctx Context, key string, def int) int {
	return read(c, ctx, key, def)
}

// String reads a string key for ctx.
func (c *Cache) String(ctx Context, key string, def string) string {
	return read(c, ctx, key, def)
}

func read[T any](c *Cache, ctx Context, key string, def T) T {
	if c == nil {
		return def
	}
	if ctx.Region == "" {
		ctx.Region = c.Region
	}
	r, ok := c.Current().Lookup(key, ctx)
	if !ok {
		return def
	}
	var v T
	if json.Unmarshal(r.Value, &v) != nil {
		return def
	}
	return v
}
