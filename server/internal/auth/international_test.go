package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

func TestNormalizeEmail(t *testing.T) {
	email, err := NormalizeEmail(" User+tag@Example.com \n")
	if err != nil || email != "user+tag@example.com" {
		t.Fatal(email, err)
	}
	for _, s := range []string{"", "bad", "@example.com", "name@", "name@localhost", "A <a@example.com>", "a@example.com\nBcc: x@example.com"} {
		if _, err := NormalizeEmail(s); err == nil {
			t.Errorf("accepted %q", s)
		}
	}
	if len(emailNickname(strings.Repeat("a", 30)+"@example.com")) != 20 {
		t.Fatal("nickname length")
	}
}

type testEmailSender struct {
	count int
	err   error
}

func (e *testEmailSender) SendCode(context.Context, string, string) error { e.count++; return e.err }
func TestEmailCodeExpiryReplayAndAttemptLimit(t *testing.T) {
	ctx := context.Background()
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	sender := &testEmailSender{}
	s := Service{Redis: client, Email: sender, FixedEmailCode: "123456"}
	if err := s.SendEmailCode(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	if err := s.SendEmailCode(ctx, "user@example.com"); err == nil {
		t.Fatal("resend must be limited")
	}
	if sender.count != 1 {
		t.Fatal(sender.count)
	}
	key := emailKey("user@example.com")
	keys := []string{"email:code:" + key, "email:tries:" + key}
	consume := func(code string) int {
		v, err := consumeEmailCode.Run(ctx, client, keys, emailDigest("user@example.com", code)).Int()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
	if r.Get(keys[0]); !r.Exists(keys[0]) {
		t.Fatal("code missing")
	}
	if v := consume("123456"); v != 1 {
		t.Fatal("correct code rejected")
	}
	if v := consume("123456"); v != 0 {
		t.Fatal("code replay accepted")
	}
	r.FastForward(time.Minute)
	if err := s.SendEmailCode(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	r.FastForward(6 * time.Minute)
	if v := consume("123456"); v != 0 {
		t.Fatal("expired code accepted")
	}
	r.FlushAll()
	if err := s.SendEmailCode(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		if consume("000000") != 0 {
			t.Fatal("incorrect code accepted")
		}
	}
	if consume("123456") != 0 {
		t.Fatal("attempt limit bypassed")
	}
	r.FastForward(time.Minute)
	if err := s.SendEmailCode(ctx, "user@example.com"); err != nil {
		t.Fatal(err)
	}
	if consume("123456") != 0 {
		t.Fatal("resend reset attempt limit")
	}
}
func TestFailedEmailSendAllowsRetryWithoutUsableCode(t *testing.T) {
	ctx := context.Background()
	r := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer client.Close()
	sender := &testEmailSender{err: errors.New("offline")}
	s := Service{Redis: client, Email: sender, FixedEmailCode: "123456"}
	if err := s.SendEmailCode(ctx, "a@example.com"); err == nil {
		t.Fatal("send error ignored")
	}
	key := emailKey("a@example.com")
	if r.Exists("email:code:"+key) || r.Exists("email:resend:"+key) {
		t.Fatal("failed delivery left usable code or resend lock")
	}
}

func TestGoogleIDTokenValidation(t *testing.T) {
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	g := Google{ClientIDs: []string{"backend"}, Keys: Apple{keys: map[string]*rsa.PublicKey{"one": &key.PublicKey}, fetched: time.Now()}}
	good := googleClaims{RegisteredClaims: jwt.RegisteredClaims{Issuer: "https://accounts.google.com", Subject: "google-user", Audience: jwt.ClaimStrings{"backend"}, ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour))}, Email: "user@gmail.com", EmailVerified: true}
	sign := func(c googleClaims) string {
		token := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		token.Header["kid"] = "one"
		v, _ := token.SignedString(key)
		return v
	}
	id, err := g.VerifyGoogle(context.Background(), sign(good))
	if err != nil || id.Subject != "google-user" || id.Email != "user@gmail.com" {
		t.Fatal(id, err)
	}
	for _, change := range []func(*googleClaims){func(c *googleClaims) { c.Audience = jwt.ClaimStrings{"other"} }, func(c *googleClaims) { c.Issuer = "https://evil.example" }, func(c *googleClaims) { c.Subject = "" }, func(c *googleClaims) { c.EmailVerified = false }, func(c *googleClaims) { c.ExpiresAt = nil }, func(c *googleClaims) { c.ExpiresAt = jwt.NewNumericDate(time.Now().Add(-time.Hour)) }} {
		c := good
		change(&c)
		if _, err := g.VerifyGoogle(context.Background(), sign(c)); !errors.Is(err, ErrProviderRejected) {
			t.Fatal("invalid token accepted", err)
		}
	}
	if _, err := g.VerifyGoogle(context.Background(), "dev:user@gmail.com"); !errors.Is(err, ErrProviderRejected) {
		t.Fatal("dev token accepted by real verifier")
	}
	if _, err := (Unconfigured{}).VerifyGoogle(context.Background(), "dev:user@gmail.com"); err == nil {
		t.Fatal("unconfigured verifier accepted token")
	}
}
