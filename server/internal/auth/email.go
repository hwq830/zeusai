package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"math/big"
	"net/mail"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/redis/go-redis/v9"
)

type EmailSender interface {
	SendCode(context.Context, string, string) error
}
type DevEmail struct{}

func (DevEmail) SendCode(context.Context, string, string) error { return nil }

func NormalizeEmail(raw string) (string, error) {
	email := strings.ToLower(strings.TrimSpace(raw))
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != email || len(email) > 254 || strings.ContainsAny(email, "\r\n") || !strings.Contains(strings.SplitN(email, "@", 2)[1], ".") {
		return "", apperr.Invalid("邮箱格式不正确")
	}
	return email, nil
}
func emailKey(email string) string {
	h := sha256.Sum256([]byte(email))
	return hex.EncodeToString(h[:])
}
func emailDigest(email, code string) string {
	h := sha256.Sum256([]byte(email + ":" + code))
	return hex.EncodeToString(h[:])
}
func emailNickname(email string) string {
	n := strings.SplitN(email, "@", 2)[0]
	for utf8.RuneCountInString(n) > 20 {
		_, size := utf8.DecodeLastRuneInString(n)
		n = n[:len(n)-size]
	}
	return n
}

func (s *Service) SendEmailCode(ctx context.Context, raw string) error {
	email, err := NormalizeEmail(raw)
	if err != nil {
		return err
	}
	if s.Email == nil {
		return unconfigured()
	}
	key := emailKey(email)
	ok, err := s.Redis.SetNX(ctx, "email:resend:"+key, 1, smsResendWindow).Result()
	if err != nil {
		return apperr.Internal(err)
	}
	if !ok {
		return apperr.RateLimited("验证码发送太频繁，请稍后再试")
	}
	// Keep verification attempts across resends to prevent bypassing the attempt limit.
	code := s.FixedEmailCode
	if code == "" {
		n, e := rand.Int(rand.Reader, big.NewInt(1000000))
		if e != nil {
			return apperr.Internal(e)
		}
		code = fmtCode(n.Int64())
	}
	if err := s.Redis.Set(ctx, "email:code:"+key, emailDigest(email, code), smsCodeTTL).Err(); err != nil {
		return apperr.Internal(err)
	}
	if err := s.Email.SendCode(ctx, email, code); err != nil {
		s.Redis.Del(ctx, "email:code:"+key, "email:resend:"+key)
		return apperr.Unavailable("邮件发送失败，请稍后重试").WithCause(err)
	}
	return nil
}
func fmtCode(n int64) string { s := strconv.FormatInt(n, 10); return strings.Repeat("0", 6-len(s)) + s }

// Atomically check attempts and consume the code. A successful code cannot be replayed.
var consumeEmailCode = redis.NewScript(`
local n=redis.call('INCR',KEYS[2])
if n==1 then redis.call('EXPIRE',KEYS[2],300) end
if n>5 then return 0 end
local expected=redis.call('GET',KEYS[1])
if not expected or expected~=ARGV[1] then return 0 end
redis.call('DEL',KEYS[1],KEYS[2])
return 1
`)

func (s *Service) VerifyEmailCode(ctx context.Context, raw, code string) (Session, error) {
	email, err := NormalizeEmail(raw)
	if err != nil {
		return Session{}, err
	}
	if len(code) != 6 || strings.Trim(code, "0123456789") != "" {
		return Session{}, emailCodeInvalid()
	}
	key := emailKey(email)
	ok, err := consumeEmailCode.Run(ctx, s.Redis, []string{"email:code:" + key, "email:tries:" + key}, emailDigest(email, code)).Int()
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	if ok != 1 {
		return Session{}, emailCodeInvalid()
	}
	u, err := s.Q.GetUserByEmail(ctx, &email)
	fresh := false
	if errors.Is(err, pgx.ErrNoRows) {
		u, err = s.Q.CreateEmailUser(ctx, db.CreateEmailUserParams{Nickname: emailNickname(email), Email: &email, Timezone: defaultTimezone})
		fresh = err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			u, err = s.Q.GetUserByEmail(ctx, &email)
		}
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return s.issue(ctx, u, fresh)
}
func emailCodeInvalid() error {
	return apperr.New(400, "email_code_invalid", "邮箱验证码错误或已过期", false)
}

func (s *Service) LoginGoogle(ctx context.Context, token string) (Session, error) {
	if s.Google == nil {
		return Session{}, unconfigured()
	}
	id, err := s.Google.VerifyGoogle(ctx, token)
	if err != nil {
		return Session{}, providerErr(err, "Google 登录失败")
	}
	u, err := s.Q.GetUserByGoogleSub(ctx, &id.Subject)
	fresh := false
	if errors.Is(err, pgx.ErrNoRows) {
		u, err = s.Q.CreateGoogleUser(ctx, db.CreateGoogleUserParams{Nickname: emailNickname(id.Email), GoogleSub: &id.Subject, GoogleEmail: &id.Email, Timezone: defaultTimezone})
		fresh = err == nil
		if errors.Is(err, pgx.ErrNoRows) {
			u, err = s.Q.GetUserByGoogleSub(ctx, &id.Subject)
		}
	}
	if err != nil {
		return Session{}, apperr.Internal(err)
	}
	return s.issue(ctx, u, fresh)
}
