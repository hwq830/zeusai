// Package calllog persists one record per provider attempt, independently of user billing.
package calllog

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
)

type Scope struct {
	UserID, TaskID  *uuid.UUID
	Purpose, Origin string
}
type scopeKey struct{}

func WithScope(ctx context.Context, s Scope) context.Context {
	return context.WithValue(ctx, scopeKey{}, s)
}
func FromContext(ctx context.Context) Scope { s, _ := ctx.Value(scopeKey{}).(Scope); return s }

type Begin struct {
	ID, GroupID uuid.UUID
	Scope
	Attempt                                 int
	Kind, Provider, Model, Protocol, Region string
	Query                                   string
	Dispatched                              bool
	StartedAt, DeadlineAt                   time.Time
}
type End struct {
	ResponseModel                           string
	Status, ErrorCode, RequestID            string
	HTTPStatus                              int
	UsageKnown                              bool
	InputTokens, OutputTokens, CachedTokens int
	// ResultCount is the number of valid results: search hits for search calls,
	// accepted charts for report.visual. Zero is written explicitly; nil means
	// no result was observed (for example a transport failure).
	ResultCount *int
	FinishedAt  time.Time
	DurationMS  int64
}
type Store interface {
	Start(context.Context, Begin) error
	Finish(context.Context, uuid.UUID, End) error
}
type Recorder struct {
	Store Store
	Log   *slog.Logger
}
type Call struct {
	r     *Recorder
	begin Begin
	end   End
}

// Writes use their own bounded context so cancellation cannot erase a completed attempt.
// IDs are allocated before retries; both SQL operations are idempotent.
func (r *Recorder) persist(ctx context.Context, fn func(context.Context) error) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	var err error
	for i := 0; i < 3; i++ {
		if err = fn(ctx); err == nil {
			return nil
		}
		if ctx.Err() != nil {
			break
		}
		select {
		case <-ctx.Done():
			return err
		case <-time.After(30 * time.Millisecond):
		}
	}
	return err
}
func (r *Recorder) Start(ctx context.Context, b Begin, timeout time.Duration) (*Call, error) {
	if r == nil || r.Store == nil {
		return nil, nil
	}
	if b.ID == uuid.Nil {
		b.ID = uuid.New()
	}
	if b.GroupID == uuid.Nil {
		b.GroupID = uuid.New()
	}
	if b.Attempt < 1 {
		b.Attempt = 1
	}
	if b.Origin == "" {
		if b.UserID == nil {
			b.Origin = "system"
		} else {
			b.Origin = "app"
		}
	}
	if chars := []rune(b.Query); len(chars) > 400 {
		b.Query = string(chars[:400])
	}
	b.StartedAt = time.Now().UTC()
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}
	b.DeadlineAt = b.StartedAt.Add(timeout)
	if d, ok := ctx.Deadline(); ok && d.Before(b.DeadlineAt) {
		b.DeadlineAt = d
	}
	if err := r.persist(ctx, func(c context.Context) error { return r.Store.Start(c, b) }); err != nil {
		r.logger().Error("external call start persistence failed", "call_id", b.ID, "provider", b.Provider)
		return nil, fmt.Errorf("external call could not be recorded: %w", err)
	}
	return &Call{r: r, begin: b}, nil
}
func (c *Call) Finish(ctx context.Context, e End) {
	if c == nil {
		return
	}
	e.FinishedAt = time.Now().UTC()
	e.DurationMS = max(0, e.FinishedAt.Sub(c.begin.StartedAt).Milliseconds())
	c.end = e
	if err := c.r.persist(ctx, func(ctx context.Context) error { return c.r.Store.Finish(ctx, c.begin.ID, e) }); err != nil {
		// The initial record remains queryable and becomes 'unknown' after its deadline.
		c.r.logger().Error("external call finish persistence failed", "call_id", c.begin.ID, "provider", c.begin.Provider)
	}
}

// SetResultCount updates the effective result count after the caller has
// validated the response. Model calls use it for accepted charts; search calls
// already set it on Finish. Zero is persisted as 0, not NULL.
func (c *Call) SetResultCount(ctx context.Context, count int) {
	if c == nil || c.end.FinishedAt.IsZero() {
		return
	}
	e := c.end
	e.ResultCount = &count
	if err := c.r.persist(ctx, func(ctx context.Context) error { return c.r.Store.Finish(ctx, c.begin.ID, e) }); err != nil {
		c.r.logger().Error("external call result count persistence failed", "call_id", c.begin.ID, "provider", c.begin.Provider)
	}
}
func (r *Recorder) Skip(ctx context.Context, b Begin, reason string) error {
	b.Dispatched = false
	c, err := r.Start(ctx, b, time.Second)
	if err != nil {
		return err
	}
	c.Finish(ctx, End{Status: "skipped", ErrorCode: reason})
	return nil
}
func (r *Recorder) logger() *slog.Logger {
	if r.Log != nil {
		return r.Log
	}
	return slog.Default()
}

// Failure deliberately excludes raw error bodies, URLs, credentials and user content.
func Failure(err error, httpStatus int) (string, string) {
	if err == nil {
		return "success", ""
	}
	if errors.Is(err, context.Canceled) {
		return "cancelled", "context_cancelled"
	}
	var ne net.Error
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		return "timeout", "deadline_exceeded"
	}
	if httpStatus >= 400 {
		return "failed", fmt.Sprintf("http_%d", httpStatus)
	}
	if httpStatus == http.StatusOK {
		return "failed", "invalid_response"
	}
	return "failed", "network_or_upstream_error"
}

type DBStore struct{ Q *db.Queries }

func (s DBStore) Start(ctx context.Context, b Begin) error {
	return s.Q.StartExternalCall(ctx, db.StartExternalCallParams{
		ID: b.ID, GroupID: b.GroupID, Attempt: int32(b.Attempt), Kind: b.Kind, Provider: b.Provider, Model: b.Model, Protocol: b.Protocol,
		Purpose: b.Purpose, Origin: b.Origin, UserID: b.UserID, TaskID: b.TaskID, Region: b.Region, QueryText: b.Query, Dispatched: b.Dispatched, StartedAt: b.StartedAt, DeadlineAt: b.DeadlineAt,
	})
}
func (s DBStore) Finish(ctx context.Context, id uuid.UUID, e End) error {
	p := db.FinishExternalCallParams{ID: id, ResponseModel: e.ResponseModel, Status: e.Status, ErrorCode: e.ErrorCode, RequestID: e.RequestID, FinishedAt: &e.FinishedAt, DurationMs: &e.DurationMS}
	if e.HTTPStatus > 0 {
		n := int32(e.HTTPStatus)
		p.HttpStatus = &n
	}
	if e.UsageKnown {
		in, out, cache := int32(e.InputTokens), int32(e.OutputTokens), int32(e.CachedTokens)
		p.InputTokens, p.OutputTokens, p.CachedTokens = &in, &out, &cache
	}
	if e.ResultCount != nil {
		n := int32(*e.ResultCount)
		p.ResultCount = &n
	}
	rows, err := s.Q.FinishExternalCall(ctx, p)
	if err != nil {
		return err
	}
	if rows != 1 {
		return errors.New("external call record missing")
	}
	return nil
}
func New(q *db.Queries, log *slog.Logger) *Recorder {
	if q == nil {
		return nil
	}
	return &Recorder{Store: DBStore{Q: q}, Log: log}
}

// HTTPInfo carries transport metadata back to the search wrapper without changing result data.
type HTTPInfo struct {
	Status    int
	RequestID string
}
type httpKey struct{}

func WithHTTPInfo(ctx context.Context, info *HTTPInfo) context.Context {
	return context.WithValue(ctx, httpKey{}, info)
}
func SetHTTPInfo(ctx context.Context, status int, id string) {
	if info, ok := ctx.Value(httpKey{}).(*HTTPInfo); ok {
		info.Status = status
		info.RequestID = id
	}
}
