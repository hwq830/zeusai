// Package auth implements login, token refresh and logout on top of the users table.
package auth

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"regexp"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
)

type Service struct {
	Q          *db.Queries
	Redis      *redis.Client
	Tokens     *Tokens
	RefreshTTL time.Duration
	SMS        SMSSender
	Apple      AppleVerifier
	Wechat     WechatExchanger
	OneClick   OneClickVerifier
	// FixedSMSCode, when set (dev only), is sent instead of a random code.
	FixedSMSCode string
}

type Session struct {
	AccessToken  string
	RefreshToken string
	ExpiresIn    time.Duration
	IsNewUser    bool
	User         db.User
}

const (
	smsCodeTTL      = 5 * time.Minute
	smsResendWindow = time.Minute
	smsMaxAttempts  = 5
	defaultTimezone = "Asia/Shanghai"
)

var e164 = regexp.MustCompile(`^\+[1-9][0-9]{6,14}$`)

func (s *Service) SendSMSCode(ctx context.Context, phone string) error {
	if !e164.MatchString(phone) {
		return apperr.Invalid("手机号格式不正确")
	}
	ok, err := s.Redis.SetNX(ctx, "sms:resend:"+phone, 1, smsResendWindow).Result()
	if err != nil {
		return apperr.Internal(err)
	}
	if !ok {
		return apperr.RateLimited("验证码发送太频繁，请稍后再试")
	}
	code := s.FixedSMSCode
	if code == "" {
		n, _ := rand.Int(rand.Reader, big.NewInt(1_000_000))
		code = fmt.Sprintf("%06d", n.Int64())
	}
	pipe := s.Redis.TxPipeline()
	pipe.Set(ctx, "sms:code:"+phone, code, smsCodeTTL)
	pipe.Del(ctx, "sms:tries:"+phone)
	if _, err := pipe.Exec(ctx); err != nil {
		return apperr.Internal(err)
	}
	if err := s.SMS.SendCode(ctx, phone, code); err != nil {
		return apperr.Unavailable("短信发送失败，请稍后再试").WithCause(err)
	}
	return nil
}

func (s *Service) VerifySMSCode(ctx context.Context, phone, code string) (Session, error) {
	tries, err := s.Redis.Incr(ctx, "sms:tries:"+phone).Result()
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	s.Redis.Expire(ctx, "sms:tries:"+phone, smsCodeTTL)
	want, err := s.Redis.Get(ctx, "sms:code:"+phone).Result()
	if tries > smsMaxAttempts || errors.Is(err, redis.Nil) || want != code {
		return Session{}, apperr.New(400, "sms_code_invalid", "验证码错误或已过期", false)
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	s.Redis.Del(ctx, "sms:code:"+phone, "sms:tries:"+phone)
	return s.loginByPhone(ctx, phone)
}

func (s *Service) LoginOneClick(ctx context.Context, token string) (Session, error) {
	phone, err := s.OneClick.PhoneNumber(ctx, token)
	if err != nil {
		return Session{}, apperr.Unauthenticated("一键登录失败，请改用验证码登录").WithCause(err)
	}
	return s.loginByPhone(ctx, phone)
}

func (s *Service) loginByPhone(ctx context.Context, phone string) (Session, error) {
	u, err := s.Q.GetUserByPhone(ctx, &phone)
	isNew := false
	if errors.Is(err, pgx.ErrNoRows) {
		u, err = s.Q.CreateUser(ctx, db.CreateUserParams{Nickname: maskPhone(phone), Phone: &phone, Timezone: defaultTimezone})
		isNew = true
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return s.issue(ctx, u, isNew)
}

func (s *Service) LoginApple(ctx context.Context, identityToken, nickname string) (Session, error) {
	sub, err := s.Apple.Verify(ctx, identityToken)
	if err != nil {
		return Session{}, apperr.Unauthenticated("Apple 登录失败").WithCause(err)
	}
	u, err := s.Q.GetUserByAppleSub(ctx, &sub)
	isNew := false
	if errors.Is(err, pgx.ErrNoRows) {
		if nickname == "" {
			nickname = "Zeus 用户"
		}
		u, err = s.Q.CreateUser(ctx, db.CreateUserParams{Nickname: nickname, AppleSub: &sub, Timezone: defaultTimezone})
		isNew = true
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return s.issue(ctx, u, isNew)
}

func (s *Service) LoginWechat(ctx context.Context, code string) (Session, error) {
	id, err := s.Wechat.Exchange(ctx, code)
	if err != nil {
		return Session{}, apperr.Unauthenticated("微信登录失败").WithCause(err)
	}
	u, err := s.Q.GetUserByWechatUnionID(ctx, &id.UnionID)
	isNew := false
	if errors.Is(err, pgx.ErrNoRows) {
		u, err = s.Q.CreateUser(ctx, db.CreateUserParams{Nickname: id.Nickname, WechatUnionid: &id.UnionID, WechatOpenid: &id.OpenID, Timezone: defaultTimezone})
		isNew = true
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return s.issue(ctx, u, isNew)
}

// Refresh rotates the refresh token: the old one is revoked atomically.
func (s *Service) Refresh(ctx context.Context, refreshToken string) (Session, error) {
	userID, err := s.Q.ConsumeRefreshToken(ctx, HashRefreshToken(refreshToken))
	if errors.Is(err, pgx.ErrNoRows) {
		return Session{}, apperr.Unauthenticated("登录已过期，请重新登录")
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	u, err := s.Q.GetUser(ctx, userID)
	if err != nil {
		return Session{}, apperr.Unauthenticated("登录已过期，请重新登录")
	}
	return s.issue(ctx, u, false)
}

func (s *Service) Logout(ctx context.Context, userID uuid.UUID, refreshToken string) error {
	if err := s.Q.RevokeRefreshToken(ctx, db.RevokeRefreshTokenParams{TokenHash: HashRefreshToken(refreshToken), UserID: userID}); err != nil {
		return apperr.Internal(err)
	}
	return nil
}

func (s *Service) issue(ctx context.Context, u db.User, isNew bool) (Session, error) {
	access, ttl, err := s.Tokens.SignAccess(u.ID)
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	refresh, hash := NewRefreshToken()
	if err := s.Q.CreateRefreshToken(ctx, db.CreateRefreshTokenParams{UserID: u.ID, TokenHash: hash, ExpiresAt: time.Now().Add(s.RefreshTTL)}); err != nil {
		return Session{}, apperr.Internal(err)
	}
	return Session{AccessToken: access, RefreshToken: refresh, ExpiresIn: ttl, IsNewUser: isNew, User: u}, nil
}

func maskPhone(p string) string {
	if len(p) < 7 {
		return p
	}
	local := p
	if len(p) > 11 {
		local = p[len(p)-11:]
	}
	return local[:3] + "****" + local[len(local)-4:]
}

// MaskPhone is exported for the profile response.
func MaskPhone(p string) string { return maskPhone(p) }
