// Package remoteconfig is the config center: typed keys with targeting rules, published as
// immutable versioned snapshots. The api process evaluates the newest snapshot per request
// (GET /v1/config and server-side checks such as quotas and kill switches); the admin process
// edits keys and publishes releases. Rules are evaluated on the server only, so the App never
// sees rules, allow-lists or keys that are not client-visible.
package remoteconfig

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Namespaces group keys in the console. killswitch keys may be published without approval.
const (
	NSFeature    = "feature"
	NSUI         = "ui"
	NSQuota      = "quota"
	NSEndpoint   = "endpoint"
	NSKillswitch = "killswitch"
	NSApp        = "app"
)

var Namespaces = []string{NSFeature, NSUI, NSQuota, NSEndpoint, NSKillswitch, NSApp}

// Value types.
const (
	TypeBool   = "bool"
	TypeInt    = "int"
	TypeFloat  = "float"
	TypeString = "string"
	TypeJSON   = "json"
)

var Types = []string{TypeBool, TypeInt, TypeFloat, TypeString, TypeJSON}

// Apply modes tell the App when a new value may take effect.
const (
	ApplyImmediate  = "immediate"
	ApplyNextLaunch = "next_launch"
)

// Condition attributes.
const (
	AttrUserID       = "user_id"
	AttrAppVersion   = "app_version"
	AttrPlatform     = "platform"
	AttrOSVersion    = "os_version"
	AttrPlan         = "plan"
	AttrRegion       = "region"
	AttrLocale       = "locale"
	AttrChannel      = "channel"
	AttrRegisteredAt = "registered_at"
)

// Condition operators. semver_* compare dotted numeric versions; before/after compare
// RFC 3339 times or YYYY-MM-DD dates.
const (
	OpEq        = "eq"
	OpNeq       = "neq"
	OpIn        = "in"
	OpNotIn     = "not_in"
	OpSemverGte = "semver_gte"
	OpSemverLt  = "semver_lt"
	OpBefore    = "before"
	OpAfter     = "after"
)

var Attrs = []string{AttrUserID, AttrAppVersion, AttrPlatform, AttrOSVersion, AttrPlan, AttrRegion, AttrLocale, AttrChannel, AttrRegisteredAt}

var Ops = []string{OpEq, OpNeq, OpIn, OpNotIn, OpSemverGte, OpSemverLt, OpBefore, OpAfter}

// Condition is one test on the request context. A rule matches when all its conditions do.
type Condition struct {
	Attr   string   `json:"attr"`
	Op     string   `json:"op"`
	Values []string `json:"values"`
}

// Rollout serves Value to Percent% of the units (users, or installs before login) that
// match the rule; the rest fall through to the next rule. Buckets are stable per key, so
// raising the percentage keeps everyone who already had the value.
type Rollout struct {
	Percent float64         `json:"percent"`
	Value   json.RawMessage `json:"value"`
	// Salt defaults to the key; changing it reshuffles who is in.
	Salt string `json:"salt,omitempty"`
}

// Rule is one targeting rule. Exactly one of Value and Rollout is set.
type Rule struct {
	ID      string          `json:"id"`
	Note    string          `json:"note,omitempty"`
	When    []Condition     `json:"when"`
	Value   json.RawMessage `json:"value,omitempty"`
	Rollout *Rollout        `json:"rollout,omitempty"`
}

// Key is a key as frozen in a snapshot.
type Key struct {
	Namespace     string          `json:"namespace"`
	Type          string          `json:"type"`
	Default       json.RawMessage `json:"default"`
	Rules         []Rule          `json:"rules"`
	ApplyMode     string          `json:"applyMode"`
	ClientVisible bool            `json:"clientVisible"`
	Expose        bool            `json:"expose"`
}

// Snapshot is the content of a release. Version is 0 for a draft built from the working copy.
type Snapshot struct {
	Version int64          `json:"-"`
	Keys    map[string]Key `json:"keys"`
}

// Context is what rules can target. Empty fields never match a positive condition.
type Context struct {
	UserID       string
	InstallID    string
	AppVersion   string
	Platform     string
	OSVersion    string
	Plan         string
	Region       string
	Locale       string
	Channel      string
	RegisteredAt *time.Time
}

// Resolved is a key's value for one context. RuleID is empty when the default was used.
type Resolved struct {
	Value     json.RawMessage `json:"value"`
	RuleID    string          `json:"ruleId,omitempty"`
	ApplyMode string          `json:"applyMode"`
	Expose    bool            `json:"expose"`
}

// Evaluate resolves every key for c. clientOnly drops keys that are not client-visible.
func (s *Snapshot) Evaluate(c Context, clientOnly bool) map[string]Resolved {
	out := map[string]Resolved{}
	if s == nil {
		return out
	}
	for name, k := range s.Keys {
		if clientOnly && !k.ClientVisible {
			continue
		}
		out[name] = k.resolve(name, c)
	}
	return out
}

// Lookup resolves one key; ok is false when the snapshot does not have it.
func (s *Snapshot) Lookup(key string, c Context) (Resolved, bool) {
	if s == nil {
		return Resolved{}, false
	}
	k, ok := s.Keys[key]
	if !ok {
		return Resolved{}, false
	}
	return k.resolve(key, c), true
}

func (k Key) resolve(name string, c Context) Resolved {
	r := Resolved{Value: k.Default, ApplyMode: k.ApplyMode, Expose: k.Expose}
	for _, rule := range k.Rules {
		if !matchesAll(rule.When, c) {
			continue
		}
		if rule.Rollout == nil {
			r.Value, r.RuleID = rule.Value, rule.ID
			return r
		}
		unit := c.UserID
		if unit == "" {
			unit = c.InstallID
		}
		if unit == "" {
			continue // nothing stable to bucket on
		}
		salt := rule.Rollout.Salt
		if salt == "" {
			salt = name
		}
		if float64(Bucket(salt, unit)) < rule.Rollout.Percent*100 {
			r.Value, r.RuleID = rule.Rollout.Value, rule.ID
			return r
		}
	}
	return r
}

// Bucket maps a unit to 0..9999: the first 8 bytes of sha256(salt + ":" + unit), mod 10000.
// The App and analytics may rely on this exact formula.
func Bucket(salt, unit string) int {
	h := sha256.Sum256([]byte(salt + ":" + unit))
	return int(binary.BigEndian.Uint64(h[:8]) % 10000)
}

func matchesAll(conds []Condition, c Context) bool {
	for _, cond := range conds {
		if !matches(cond, c) {
			return false
		}
	}
	return true
}

func matches(cond Condition, c Context) bool {
	if cond.Attr == AttrRegisteredAt {
		if c.RegisteredAt == nil || len(cond.Values) == 0 {
			return false
		}
		t, ok := parseTime(cond.Values[0])
		if !ok {
			return false
		}
		switch cond.Op {
		case OpBefore:
			return c.RegisteredAt.Before(t)
		case OpAfter:
			return !c.RegisteredAt.Before(t)
		}
		return false
	}
	v := attr(cond.Attr, c)
	switch cond.Op {
	case OpEq, OpIn:
		return v != "" && slices.ContainsFunc(cond.Values, func(x string) bool { return strings.EqualFold(x, v) })
	case OpNeq, OpNotIn:
		return !slices.ContainsFunc(cond.Values, func(x string) bool { return strings.EqualFold(x, v) })
	case OpSemverGte, OpSemverLt:
		if v == "" || len(cond.Values) == 0 {
			return false
		}
		cmp, ok := CompareVersions(v, cond.Values[0])
		if !ok {
			return false
		}
		if cond.Op == OpSemverGte {
			return cmp >= 0
		}
		return cmp < 0
	}
	return false
}

func attr(name string, c Context) string {
	switch name {
	case AttrUserID:
		return c.UserID
	case AttrAppVersion:
		return c.AppVersion
	case AttrPlatform:
		return c.Platform
	case AttrOSVersion:
		return c.OSVersion
	case AttrPlan:
		return c.Plan
	case AttrRegion:
		return c.Region
	case AttrLocale:
		return c.Locale
	case AttrChannel:
		return c.Channel
	}
	return ""
}

func parseTime(s string) (time.Time, bool) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, true
	}
	if t, err := time.Parse(time.DateOnly, s); err == nil {
		return t, true
	}
	return time.Time{}, false
}

// CompareVersions compares dotted numeric versions ("1.2" == "1.2.0"); a pre-release or
// build suffix after '-' or '+' is ignored. ok is false when either side is not a version.
func CompareVersions(a, b string) (int, bool) {
	pa, oka := versionParts(a)
	pb, okb := versionParts(b)
	if !oka || !okb {
		return 0, false
	}
	for i := 0; i < max(len(pa), len(pb)); i++ {
		var x, y int
		if i < len(pa) {
			x = pa[i]
		}
		if i < len(pb) {
			y = pb[i]
		}
		if x != y {
			if x < y {
				return -1, true
			}
			return 1, true
		}
	}
	return 0, true
}

func versionParts(v string) ([]int, bool) {
	v = strings.TrimPrefix(strings.TrimSpace(v), "v")
	if i := strings.IndexAny(v, "-+ "); i >= 0 {
		v = v[:i]
	}
	if v == "" {
		return nil, false
	}
	parts := strings.Split(v, ".")
	out := make([]int, len(parts))
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil, false
		}
		out[i] = n
	}
	return out, true
}
