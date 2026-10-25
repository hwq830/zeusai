package remoteconfig

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"sort"
	"strconv"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

// KeyFromRow converts a working-copy row into its snapshot form.
func KeyFromRow(r db.ConfigKey) (Key, error) {
	k := Key{
		Namespace: r.Namespace, Type: r.Type, Default: r.DefaultValue, ApplyMode: r.ApplyMode,
		ClientVisible: r.ClientVisible, Expose: r.Expose, Rules: []Rule{},
	}
	if len(r.Rules) > 0 {
		if err := json.Unmarshal(r.Rules, &k.Rules); err != nil {
			return k, err
		}
	}
	return k, nil
}

// Change is one key that differs between two snapshots.
type Change struct {
	Key    string `json:"key"`
	Kind   string `json:"kind"` // added | removed | changed
	Before *Key   `json:"before,omitempty"`
	After  *Key   `json:"after,omitempty"`
}

// Namespace is the namespace of the newer side (or the removed key's).
func (c Change) Namespace() string {
	if c.After != nil {
		return c.After.Namespace
	}
	return c.Before.Namespace
}

// Diff lists the keys that differ from base to next, sorted by key.
func Diff(base, next *Snapshot) []Change {
	out := []Change{}
	bk, nk := keysOf(base), keysOf(next)
	for name, n := range nk {
		n := n
		b, ok := bk[name]
		switch {
		case !ok:
			out = append(out, Change{Key: name, Kind: "added", After: &n})
		case !sameKey(b, n):
			b := b
			out = append(out, Change{Key: name, Kind: "changed", Before: &b, After: &n})
		}
	}
	for name, b := range bk {
		if _, ok := nk[name]; !ok {
			b := b
			out = append(out, Change{Key: name, Kind: "removed", Before: &b})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

func keysOf(s *Snapshot) map[string]Key {
	if s == nil {
		return nil
	}
	return s.Keys
}

func sameKey(a, b Key) bool {
	ja, _ := json.Marshal(normalize(a))
	jb, _ := json.Marshal(normalize(b))
	return bytes.Equal(ja, jb)
}

// normalize re-encodes raw JSON values so formatting differences are not changes.
func normalize(k Key) Key {
	k.Default = compact(k.Default)
	rules := make([]Rule, len(k.Rules))
	for i, r := range k.Rules {
		r.Value = compact(r.Value)
		if r.Rollout != nil {
			ro := *r.Rollout
			ro.Value = compact(ro.Value)
			r.Rollout = &ro
		}
		if r.When == nil {
			r.When = []Condition{}
		}
		rules[i] = r
	}
	k.Rules = rules
	return k
}

func compact(v json.RawMessage) json.RawMessage {
	if len(v) == 0 {
		return v
	}
	var b bytes.Buffer
	if json.Compact(&b, v) != nil {
		return v
	}
	return b.Bytes()
}

// Releases runs the publish workflow for the admin process.
type Releases struct {
	Pool  *pgxpool.Pool
	Redis *redis.Client // nil: api processes notice new versions by polling
	Log   *slog.Logger
	// RequireApproval is on in prod: a release needs an owner's approval unless it only
	// changes killswitch keys or the submitter is the only active owner.
	RequireApproval bool
}

// Actor is the console user acting on a release.
type Actor struct {
	ID   uuid.UUID
	Role string
}

func (s *Releases) q() *db.Queries { return db.New(s.Pool) }

// Working builds a snapshot from the non-archived keys of the working copy.
func Working(ctx context.Context, q *db.Queries) (*Snapshot, map[string]json.RawMessage, error) {
	rows, err := q.ListConfigKeys(ctx, false)
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	s := &Snapshot{Keys: map[string]Key{}}
	schemas := map[string]json.RawMessage{}
	for _, r := range rows {
		k, err := KeyFromRow(r)
		if err != nil {
			return nil, nil, apperr.Internal(err)
		}
		s.Keys[r.Key], schemas[r.Key] = k, r.ValueSchema
	}
	return s, schemas, nil
}

// Published returns the newest published release and its snapshot; nil before the first one.
func Published(ctx context.Context, q *db.Queries) (*db.ConfigRelease, *Snapshot, error) {
	r, err := q.LatestPublishedConfig(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, &Snapshot{Keys: map[string]Key{}}, nil
	}
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	s, err := ParseSnapshot(*r.Version, r.Snapshot)
	if err != nil {
		return nil, nil, apperr.Internal(err)
	}
	return &r, s, nil
}

// Submit freezes the working copy into a release. It is published at once when no
// approval is needed, otherwise it waits as the single pending release (replacing any
// earlier pending one).
func (s *Releases) Submit(ctx context.Context, a Actor, note string) (db.ConfigRelease, []Change, error) {
	var out db.ConfigRelease
	var changes []Change
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := lock(ctx, tx); err != nil {
			return err
		}
		next, schemas, err := Working(ctx, q)
		if err != nil {
			return err
		}
		for name, k := range next.Keys {
			if err := ValidateKey(name, k, schemas[name]); err != nil {
				return err
			}
		}
		_, cur, err := Published(ctx, q)
		if err != nil {
			return err
		}
		changes = Diff(cur, next)
		if len(changes) == 0 {
			return apperr.Conflict("没有需要发布的改动")
		}
		out, err = s.create(ctx, q, a, next, note, nil, changes)
		return err
	})
	if err != nil {
		return out, nil, err
	}
	s.notify(ctx, out)
	return out, changes, nil
}

// Rollback publishes the snapshot of an earlier published release as a new version (subject
// to the same approval rule) and resets the working copy of those keys to it.
func (s *Releases) Rollback(ctx context.Context, a Actor, id int64, note string) (db.ConfigRelease, []Change, error) {
	var out db.ConfigRelease
	var changes []Change
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := lock(ctx, tx); err != nil {
			return err
		}
		target, err := q.GetConfigRelease(ctx, id)
		if errors.Is(err, pgx.ErrNoRows) {
			return apperr.NotFound("发布记录不存在")
		}
		if err != nil {
			return apperr.Internal(err)
		}
		if target.Status != "published" {
			return apperr.Conflict("只能回滚到已发布的版本")
		}
		next, err := ParseSnapshot(0, target.Snapshot)
		if err != nil {
			return apperr.Internal(err)
		}
		_, cur, err := Published(ctx, q)
		if err != nil {
			return err
		}
		changes = Diff(cur, next)
		if len(changes) == 0 {
			return apperr.Conflict("线上已经是这个版本的内容")
		}
		if note == "" {
			note = "回滚到 v" + strconv.FormatInt(*target.Version, 10)
		}
		out, err = s.create(ctx, q, a, next, note, &target.ID, changes)
		return err
	})
	if err != nil {
		return out, nil, err
	}
	s.notify(ctx, out)
	return out, changes, nil
}

func (s *Releases) create(ctx context.Context, q *db.Queries, a Actor, next *Snapshot, note string, from *int64, changes []Change) (db.ConfigRelease, error) {
	raw, err := json.Marshal(next)
	if err != nil {
		return db.ConfigRelease{}, apperr.Internal(err)
	}
	direct, err := s.PublishesDirectly(ctx, q, a, changes)
	if err != nil {
		return db.ConfigRelease{}, err
	}
	if err := q.SupersedePendingConfig(ctx, &a.ID); err != nil {
		return db.ConfigRelease{}, apperr.Internal(err)
	}
	if !direct {
		r, err := q.InsertPendingConfig(ctx, db.InsertPendingConfigParams{Snapshot: raw, Note: note, RolledBackFrom: from, CreatedBy: &a.ID})
		if err != nil {
			return r, apperr.Internal(err)
		}
		return r, nil
	}
	r, err := q.InsertPublishedConfig(ctx, db.InsertPublishedConfigParams{Snapshot: raw, Note: note, RolledBackFrom: from, CreatedBy: &a.ID})
	if err != nil {
		return r, apperr.Internal(err)
	}
	return r, s.afterPublish(ctx, q, r, a)
}

// PublishesDirectly reports whether a submission of these changes by a goes live without approval.
func (s *Releases) PublishesDirectly(ctx context.Context, q *db.Queries, a Actor, changes []Change) (bool, error) {
	if !s.RequireApproval {
		return true, nil
	}
	onlyKill := true
	for _, c := range changes {
		if c.Namespace() != NSKillswitch || c.Kind != "changed" {
			onlyKill = false
			break
		}
	}
	if onlyKill {
		return true, nil
	}
	if a.Role != "owner" {
		return false, nil
	}
	owners, err := q.CountActiveOwners(ctx)
	if err != nil {
		return false, apperr.Internal(err)
	}
	return owners <= 1, nil
}

// Approve publishes the pending release. Owners may not approve their own submission unless
// they are the only active owner.
func (s *Releases) Approve(ctx context.Context, a Actor, id int64, note string) (db.ConfigRelease, error) {
	var out db.ConfigRelease
	err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
		q := db.New(tx)
		if err := lock(ctx, tx); err != nil {
			return err
		}
		r, err := pending(ctx, q, id)
		if err != nil {
			return err
		}
		if r.CreatedBy != nil && *r.CreatedBy == a.ID {
			owners, err := q.CountActiveOwners(ctx)
			if err != nil {
				return apperr.Internal(err)
			}
			if owners > 1 {
				return apperr.Forbidden("不能审批自己提交的发布，请另一位 owner 审批")
			}
		}
		out, err = q.PublishPendingConfig(ctx, db.PublishPendingConfigParams{ID: id, DecidedBy: &a.ID, DecisionNote: note})
		if err != nil {
			return apperr.Internal(err)
		}
		return s.afterPublish(ctx, q, out, a)
	})
	if err != nil {
		return out, err
	}
	s.notify(ctx, out)
	return out, nil
}

// Reject closes the pending release without publishing it.
func (s *Releases) Reject(ctx context.Context, a Actor, id int64, note string) (db.ConfigRelease, error) {
	q := s.q()
	if _, err := pending(ctx, q, id); err != nil {
		return db.ConfigRelease{}, err
	}
	r, err := q.RejectPendingConfig(ctx, db.RejectPendingConfigParams{ID: id, DecidedBy: &a.ID, DecisionNote: note})
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperr.Conflict("这个发布已经处理过了")
	}
	if err != nil {
		return r, apperr.Internal(err)
	}
	return r, nil
}

func pending(ctx context.Context, q *db.Queries, id int64) (db.ConfigRelease, error) {
	r, err := q.GetConfigReleaseForUpdate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return r, apperr.NotFound("发布记录不存在")
	}
	if err != nil {
		return r, apperr.Internal(err)
	}
	if r.Status != "pending" {
		return r, apperr.Conflict("这个发布已经处理过了")
	}
	return r, nil
}

// afterPublish resets the working copy when a rollback goes live, so the bad change is not
// published again by the next submit.
func (s *Releases) afterPublish(ctx context.Context, q *db.Queries, r db.ConfigRelease, a Actor) error {
	if r.RolledBackFrom == nil {
		return nil
	}
	snap, err := ParseSnapshot(0, r.Snapshot)
	if err != nil {
		return apperr.Internal(err)
	}
	for name, k := range snap.Keys {
		rules, _ := json.Marshal(k.Rules)
		if err := q.ResetConfigKeyRules(ctx, db.ResetConfigKeyRulesParams{Key: name, DefaultValue: k.Default, Rules: rules, UpdatedBy: &a.ID}); err != nil {
			return apperr.Internal(err)
		}
	}
	return nil
}

// lock serialises release writes so two submissions cannot interleave.
func lock(ctx context.Context, tx pgx.Tx) error {
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtext('zeus.config_releases'))"); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

func (s *Releases) notify(ctx context.Context, r db.ConfigRelease) {
	if r.Status != "published" || r.Version == nil || s.Redis == nil {
		return
	}
	if err := s.Redis.Publish(ctx, Channel, strconv.FormatInt(*r.Version, 10)).Err(); err != nil && s.Log != nil {
		s.Log.Warn("config publish notice not sent; api processes will pick it up by polling", "err", err)
	}
}
