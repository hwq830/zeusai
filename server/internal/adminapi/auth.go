package adminapi

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/bcrypt"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

// audience keeps App tokens and admin tokens apart even if the secrets were ever reused.
const audience = "zeus-admin"

// Tokens signs operator session JWTs.
type Tokens struct {
	secret []byte
	ttl    time.Duration
	now    func() time.Time
}

func NewTokens(secret string, ttl time.Duration) *Tokens {
	return &Tokens{secret: []byte(secret), ttl: ttl, now: time.Now}
}

func (t *Tokens) Sign(id uuid.UUID) (string, time.Duration, error) {
	now := t.now()
	claims := jwt.RegisteredClaims{
		Issuer: "zeus", Audience: jwt.ClaimStrings{audience}, Subject: id.String(),
		IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(t.ttl)),
	}
	s, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(t.secret)
	return s, t.ttl, err
}

func (t *Tokens) Verify(token string) (uuid.UUID, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(token, &claims, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithAudience(audience),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return uuid.Nil, err
	}
	return uuid.Parse(claims.Subject)
}

// HashPassword is exported for `zeusctl admin-create`.
func HashPassword(pw string) (string, error) {
	if len([]rune(pw)) < 10 {
		return "", apperr.Invalid("密码至少 10 位")
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	return string(b), err
}

// NewPassword returns a random 16-character password for new or reset accounts.
func NewPassword() string {
	b := make([]byte, 12)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// loginLimiter allows 5 failed logins per email in 15 minutes (per process; the admin
// process runs as a single replica).
type loginLimiter struct {
	mu    sync.Mutex
	fails map[string][]time.Time
}

const (
	maxFails   = 5
	failWindow = 15 * time.Minute
)

func (l *loginLimiter) blocked(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	recent := l.fails[key][:0]
	for _, t := range l.fails[key] {
		if now.Sub(t) < failWindow {
			recent = append(recent, t)
		}
	}
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	l.fails[key] = recent
	return len(recent) >= maxFails
}

func (l *loginLimiter) fail(key string, now time.Time) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.fails == nil {
		l.fails = map[string][]time.Time{}
	}
	l.fails[key] = append(l.fails[key], now)
}

func (l *loginLimiter) reset(key string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.fails, key)
}

// dummyHash keeps a login for an unknown email as slow as one with a wrong password.
var dummyHash, _ = bcrypt.GenerateFromPassword([]byte("zeus-admin-dummy-password"), bcrypt.DefaultCost)

func toAdmin(a db.AdminUser) gen.AdminUser {
	perms := rolePerms[a.Role]
	if perms == nil {
		perms = []string{}
	}
	return gen.AdminUser{
		Id: a.ID.String(), Email: a.Email, Name: a.Name, Role: gen.Role(a.Role), Disabled: a.DisabledAt != nil,
		Permissions: append([]string{}, perms...), LastLoginAt: a.LastLoginAt, CreatedAt: a.CreatedAt,
	}
}

func (s *Server) AdminLogin(ctx context.Context, req gen.AdminLoginRequestObject) (gen.AdminLoginResponseObject, error) {
	email := strings.ToLower(strings.TrimSpace(string(req.Body.Email)))
	now := s.now()
	if s.limiter.blocked(email, now) {
		return nil, apperr.RateLimited("登录失败次数过多，请 15 分钟后再试")
	}
	a, err := s.Q.GetAdminUserByEmail(ctx, email)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, apperr.Internal(err)
	}
	hash := dummyHash
	if err == nil {
		hash = []byte(a.PasswordHash)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte(req.Body.Password)) != nil || err != nil || a.DisabledAt != nil {
		s.limiter.fail(email, now)
		return nil, apperr.Unauthenticated("邮箱或密码不正确")
	}
	s.limiter.reset(email)
	tok, ttl, err := s.Tokens.Sign(a.ID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	_ = s.Q.TouchAdminLogin(ctx, a.ID)
	s.audit(context.WithValue(ctx, ctxKey{}, a), "admin.login", "admin", a.ID.String(), nil)
	return gen.AdminLogin200JSONResponse{Token: tok, ExpiresIn: int(ttl.Seconds()), Admin: toAdmin(a)}, nil
}

func (s *Server) GetAdminMe(ctx context.Context, _ gen.GetAdminMeRequestObject) (gen.GetAdminMeResponseObject, error) {
	return gen.GetAdminMe200JSONResponse(toAdmin(current(ctx))), nil
}

func (s *Server) ChangeAdminPassword(ctx context.Context, req gen.ChangeAdminPasswordRequestObject) (gen.ChangeAdminPasswordResponseObject, error) {
	a := current(ctx)
	if bcrypt.CompareHashAndPassword([]byte(a.PasswordHash), []byte(req.Body.CurrentPassword)) != nil {
		return nil, apperr.Invalid("当前密码不正确")
	}
	h, err := HashPassword(req.Body.NewPassword)
	if err != nil {
		return nil, err
	}
	if err := s.Q.SetAdminPassword(ctx, db.SetAdminPasswordParams{ID: a.ID, PasswordHash: h}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "admin.change_password", "admin", a.ID.String(), nil)
	return gen.ChangeAdminPassword204Response{}, nil
}

func (s *Server) ListAdmins(ctx context.Context, _ gen.ListAdminsRequestObject) (gen.ListAdminsResponseObject, error) {
	if _, err := require(ctx, PermAdmins); err != nil {
		return nil, err
	}
	rows, err := s.Q.ListAdminUsers(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make(gen.ListAdmins200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAdmin(r))
	}
	return out, nil
}

func validRole(r gen.Role) bool { _, ok := rolePerms[string(r)]; return ok }

func (s *Server) CreateAdmin(ctx context.Context, req gen.CreateAdminRequestObject) (gen.CreateAdminResponseObject, error) {
	if _, err := require(ctx, PermAdmins); err != nil {
		return nil, err
	}
	if !validRole(req.Body.Role) {
		return nil, apperr.Invalid("角色无效")
	}
	pw := NewPassword()
	h, err := HashPassword(pw)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	email := strings.ToLower(strings.TrimSpace(string(req.Body.Email)))
	a, err := s.Q.CreateAdminUser(ctx, db.CreateAdminUserParams{
		Email: email, Name: strings.TrimSpace(deref(req.Body.Name)), PasswordHash: h, Role: string(req.Body.Role),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, apperr.Conflict("该邮箱已是管理员")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "admin.create", "admin", a.ID.String(), map[string]any{"email": email, "role": a.Role})
	return gen.CreateAdmin201JSONResponse{Admin: toAdmin(a), Password: pw}, nil
}

func (s *Server) UpdateAdmin(ctx context.Context, req gen.UpdateAdminRequestObject) (gen.UpdateAdminResponseObject, error) {
	me, err := require(ctx, PermAdmins)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.AdminId, "管理员")
	if err != nil {
		return nil, err
	}
	target, err := s.Q.GetAdminUser(ctx, id)
	if err != nil {
		return nil, notFound(err, "管理员")
	}
	b := req.Body
	if b.Role != nil && !validRole(*b.Role) {
		return nil, apperr.Invalid("角色无效")
	}
	demotes := b.Role != nil && *b.Role != "owner"
	disables := b.Disabled != nil && *b.Disabled
	if id == me.ID && (demotes || disables) {
		return nil, apperr.Invalid("不能停用自己或降低自己的角色")
	}
	if target.Role == "owner" && target.DisabledAt == nil && (demotes || disables) {
		n, err := s.Q.CountActiveOwners(ctx)
		if err != nil {
			return nil, apperr.Internal(err)
		}
		if n <= 1 {
			return nil, apperr.Invalid("至少保留一个可用的 owner")
		}
	}
	var role *string
	if b.Role != nil {
		r := string(*b.Role)
		role = &r
	}
	a, err := s.Q.UpdateAdminUser(ctx, db.UpdateAdminUserParams{ID: id, Name: b.Name, Role: role, Disabled: b.Disabled})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "admin.update", "admin", id.String(), b)
	return gen.UpdateAdmin200JSONResponse(toAdmin(a)), nil
}

func (s *Server) ResetAdminPassword(ctx context.Context, req gen.ResetAdminPasswordRequestObject) (gen.ResetAdminPasswordResponseObject, error) {
	if _, err := require(ctx, PermAdmins); err != nil {
		return nil, err
	}
	id, err := parseID(req.AdminId, "管理员")
	if err != nil {
		return nil, err
	}
	a, err := s.Q.GetAdminUser(ctx, id)
	if err != nil {
		return nil, notFound(err, "管理员")
	}
	pw := NewPassword()
	h, err := HashPassword(pw)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if err := s.Q.SetAdminPassword(ctx, db.SetAdminPasswordParams{ID: id, PasswordHash: h}); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "admin.reset_password", "admin", id.String(), nil)
	return gen.ResetAdminPassword200JSONResponse{Admin: toAdmin(a), Password: pw}, nil
}

func (s *Server) ListAuditLog(ctx context.Context, req gen.ListAuditLogRequestObject) (gen.ListAuditLogResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	rows, err := s.Q.ListAdminAudit(ctx, db.ListAdminAuditParams{
		TargetType: strPtr(p.TargetType), TargetID: strPtr(p.TargetId), Limit: int32(lim), Offset: int32(off),
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AuditEntry, 0, len(rows))
	for _, r := range rows {
		items = append(items, gen.AuditEntry{
			Id: r.ID, AdminEmail: r.AdminEmail, Action: r.Action, TargetType: r.TargetType, TargetId: r.TargetID,
			Detail: orEmptyMap(jsonObject(r.Detail)), CreatedAt: r.CreatedAt,
		})
	}
	return gen.ListAuditLog200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func orEmptyMap(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}
