// Package httpapi implements the generated StrictServerInterface on top of the domain
// services. Handlers return *apperr.Error; errorHandler renders it as the contract's Error.
package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/report"
	"github.com/hwq830/zeusai/server/internal/task"
	"github.com/hwq830/zeusai/server/internal/template"
)

type Server struct {
	Log        *slog.Logger
	Q          *db.Queries
	Auth       *auth.Service
	Tokens     *auth.Tokens
	Quota      *quota.Service
	Templates  *template.Service
	Briefings  *briefing.Service
	Tasks      *task.Service
	Connectors *connector.Service
	Reports    *report.Service
	Bus        *events.Bus
	// SSEPing is the keep-alive interval for event streams.
	SSEPing time.Duration
}

var _ gen.StrictServerInterface = (*Server)(nil)

// Handler returns the full HTTP handler: health check plus the /v1 API.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	r.Get("/s/{token}", s.sharePage)

	strict := gen.NewStrictHandlerWithOptions(s, nil, gen.StrictHTTPServerOptions{
		RequestErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.writeError(w, r, apperr.Invalid("请求格式不正确").WithCause(err))
		},
		ResponseErrorHandlerFunc: s.writeError,
	})
	return gen.HandlerWithOptions(strict, gen.ChiServerOptions{
		BaseRouter:  r,
		Middlewares: []gen.MiddlewareFunc{s.authenticate},
		ErrorHandlerFunc: func(w http.ResponseWriter, r *http.Request, err error) {
			s.writeError(w, r, apperr.Invalid("参数不正确").WithCause(err))
		},
	})
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.As(err)
	if e.Status >= 500 {
		s.Log.Error("request failed", "method", r.Method, "path", r.URL.Path, "err", err)
	}
	body := gen.Error{Code: e.Code, Message: e.Message, Retryable: e.Retryable}
	if e.Details != nil {
		d := e.Details
		body.Details = &d
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(body)
}

type ctxKey struct{}

// authenticate enforces bearer auth on operations the contract marks as secured.
func (s *Server) authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Context().Value(gen.BearerAuthScopes) == nil {
			next.ServeHTTP(w, r)
			return
		}
		tok, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok {
			s.writeError(w, r, apperr.Unauthenticated("请先登录"))
			return
		}
		uid, err := s.Tokens.VerifyAccess(strings.TrimSpace(tok))
		if err != nil {
			s.writeError(w, r, apperr.Unauthenticated("登录已过期"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, uid)))
	})
}

func userID(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(ctxKey{}).(uuid.UUID)
	return id
}

func (s *Server) currentUser(ctx context.Context) (db.User, error) {
	u, err := s.Q.GetUser(ctx, userID(ctx))
	if err != nil {
		return u, apperr.Unauthenticated("账号不存在或已注销")
	}
	return u, nil
}

// recode converts between domain structs and generated types that share the contract's JSON shape.
func recode[T any](v any) T {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	if err != nil {
		panic("httpapi: recode: " + err.Error()) // shapes are fixed by the contract; a mismatch is a bug
	}
	return out
}

// page decodes an offset cursor.
func page(cursor *gen.Cursor, limit *gen.Limit) (lim, off int) {
	lim = 20
	if limit != nil && *limit > 0 && *limit <= 100 {
		lim = *limit
	}
	if cursor != nil {
		if b, err := base64.RawURLEncoding.DecodeString(*cursor); err == nil {
			off, _ = strconv.Atoi(string(b))
		}
	}
	return lim, max(off, 0)
}

// nextCursor returns the cursor for the following page, or nil when n < lim.
func nextCursor(n, lim, off int) *string {
	if n < lim {
		return nil
	}
	c := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(off + lim)))
	return &c
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func isNotFound(err error) bool {
	var e *apperr.Error
	return errors.As(err, &e) && e.Code == "not_found"
}
