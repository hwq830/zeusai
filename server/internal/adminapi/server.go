// Package adminapi implements the admin console API (api/admin.yaml) served by
// server/cmd/admin. It reads across users on purpose, so every operation checks the
// operator's role and every write is recorded in admin_audit_log.
package adminapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/quota"
)

type Server struct {
	Log    *slog.Logger
	Q      *db.Queries
	Tokens *Tokens
	Quota  *quota.Service
	Orch   orch.Client
	// LLMProvider and ModelGWConfig mirror the api process settings for the gateway page.
	LLMProvider   string
	ModelGWConfig string
	// PublicBaseURL builds share links.
	PublicBaseURL string
	// StaticDir serves the built console; empty serves only the API.
	StaticDir string
	Now       func() time.Time

	limiter loginLimiter
}

var _ gen.StrictServerInterface = (*Server)(nil)

func (s *Server) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Handler returns the health check, /admin/v1 and, with StaticDir, the console itself.
func (s *Server) Handler() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID, middleware.RealIP, middleware.Recoverer)
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if s.StaticDir != "" {
		r.NotFound(spa(s.StaticDir))
	}
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

// spa serves files from dir and falls back to index.html for client-side routes.
func spa(dir string) http.HandlerFunc {
	fs := http.FileServer(http.Dir(dir))
	return func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/admin/v1/") {
			http.NotFound(w, r)
			return
		}
		p := filepath.Join(dir, filepath.Clean("/"+r.URL.Path))
		if st, err := os.Stat(p); err != nil || st.IsDir() {
			http.ServeFile(w, r, filepath.Join(dir, "index.html"))
			return
		}
		fs.ServeHTTP(w, r)
	}
}

func (s *Server) writeError(w http.ResponseWriter, r *http.Request, err error) {
	e := apperr.As(err)
	if e.Status >= 500 {
		s.Log.Error("admin request failed", "method", r.Method, "path", r.URL.Path, "err", err)
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

// authenticate checks the bearer token and that the operator still exists and is enabled,
// so disabling an operator takes effect immediately.
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
		id, err := s.Tokens.Verify(strings.TrimSpace(tok))
		if err != nil {
			s.writeError(w, r, apperr.Unauthenticated("登录已过期"))
			return
		}
		a, err := s.Q.GetAdminUser(r.Context(), id)
		if err != nil || a.DisabledAt != nil {
			s.writeError(w, r, apperr.Unauthenticated("账号不存在或已停用"))
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, a)))
	})
}

func current(ctx context.Context) db.AdminUser {
	a, _ := ctx.Value(ctxKey{}).(db.AdminUser)
	return a
}

// Permissions. Reads are open to every role; writes need one of these.
const (
	PermUsers      = "users.write"
	PermTemplates  = "templates.write"
	PermConnectors = "connectors.write"
	PermModeration = "moderation.write"
	PermTasks      = "tasks.write"
	PermAdmins     = "admins.write"
)

var rolePerms = map[string][]string{
	"owner":     {PermUsers, PermTemplates, PermConnectors, PermModeration, PermTasks, PermAdmins},
	"operator":  {PermUsers, PermTemplates, PermConnectors, PermModeration, PermTasks},
	"moderator": {PermModeration},
	"viewer":    {},
}

func can(role, perm string) bool {
	for _, p := range rolePerms[role] {
		if p == perm {
			return true
		}
	}
	return false
}

func require(ctx context.Context, perm string) (db.AdminUser, error) {
	a := current(ctx)
	if !can(a.Role, perm) {
		return a, apperr.Forbidden("当前角色没有这个权限")
	}
	return a, nil
}

// audit records a console write. Failures are logged, not returned: the change is already made.
func (s *Server) audit(ctx context.Context, action, targetType, targetID string, detail any) {
	b, _ := json.Marshal(detail)
	if detail == nil {
		b = []byte("{}")
	}
	a := current(ctx)
	var id *uuid.UUID
	if a.ID != uuid.Nil {
		id = &a.ID
	}
	if err := s.Q.InsertAdminAudit(ctx, db.InsertAdminAuditParams{
		AdminID: id, Action: action, TargetType: targetType, TargetID: targetID, Detail: b,
	}); err != nil {
		s.Log.Error("admin audit not recorded", "action", action, "err", err)
	}
}

// page decodes an offset cursor (same encoding as the App API).
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

func nextCursor(n, lim, off int) *string {
	if n < lim {
		return nil
	}
	c := base64.RawURLEncoding.EncodeToString([]byte(strconv.Itoa(off + lim)))
	return &c
}

func parseID(s, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(s)
	if err != nil {
		return id, apperr.NotFound(what + "不存在")
	}
	return id, nil
}

func notFound(err error, what string) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return apperr.NotFound(what + "不存在")
	}
	return apperr.Internal(err)
}

func strPtr(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	t := strings.TrimSpace(*s)
	return &t
}

func idStr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func cny(micros int64) float64 { return float64(micros) / 1e6 }

func maskPhone(p *string) string {
	if p == nil {
		return ""
	}
	s := strings.TrimPrefix(*p, "+86")
	if len(s) < 7 {
		return s
	}
	return s[:3] + "****" + s[len(s)-4:]
}

func deref[T any](p *T) T {
	var z T
	if p == nil {
		return z
	}
	return *p
}

func jsonObject(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]any
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	return m
}
