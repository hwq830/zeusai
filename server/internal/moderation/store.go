package moderation

import (
	"context"
	"log/slog"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
)

// Terms checks against the moderation_terms table that operators edit in the admin console.
// The list is reloaded at most every Every (default one minute); a failed reload keeps the
// previous list.
type Terms struct {
	Q     *db.Queries
	Log   *slog.Logger
	Every time.Duration

	mu     sync.Mutex
	kw     *Keywords
	loaded time.Time
}

func (t *Terms) current(ctx context.Context) *Keywords {
	every := t.Every
	if every <= 0 {
		every = time.Minute
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.kw != nil && time.Since(t.loaded) < every {
		return t.kw
	}
	terms, err := t.Q.ListModerationTerms(ctx)
	if err != nil {
		if t.Log != nil {
			t.Log.Warn("moderation terms reload failed", "err", err)
		}
		if t.kw == nil {
			return KeywordList(nil)
		}
		return t.kw
	}
	t.kw, t.loaded = KeywordList(terms), time.Now()
	return t.kw
}

func (t *Terms) Check(ctx context.Context, kind Kind, text string) (Verdict, error) {
	return t.current(ctx).Check(ctx, kind, text)
}

type userKey struct{}

// WithUser tags ctx with the user whose text is being checked, for the block log.
func WithUser(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, userKey{}, id)
}

func userFrom(ctx context.Context) *uuid.UUID {
	if id, ok := ctx.Value(userKey{}).(uuid.UUID); ok {
		return &id
	}
	return nil
}

// Recorder logs every block to moderation_events so operators can review them.
type Recorder struct {
	Checker
	Q   *db.Queries
	Log *slog.Logger
}

func (r Recorder) Check(ctx context.Context, kind Kind, text string) (Verdict, error) {
	v, err := r.Checker.Check(ctx, kind, text)
	if err == nil && v.Blocked {
		if lerr := r.Q.InsertModerationEvent(ctx, db.InsertModerationEventParams{
			UserID: userFrom(ctx), Kind: string(kind), Reason: v.Reason, Excerpt: excerpt(text, 200),
		}); lerr != nil && r.Log != nil {
			r.Log.Warn("moderation event not recorded", "err", lerr)
		}
	}
	return v, err
}

func excerpt(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n]) + "…"
}
