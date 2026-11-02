package auth

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type GoogleIdentity struct{ Subject, Email string }
type GoogleVerifier interface {
	VerifyGoogle(context.Context, string) (GoogleIdentity, error)
}

// Google uses the same RSA JWK cache as Apple, with a separate Google key source.
// Accounts are keyed by sub, never implicitly linked using an email claim.
type Google struct {
	ClientIDs []string
	Keys      Apple
}

type googleClaims struct {
	jwt.RegisteredClaims
	Email         string `json:"email"`
	EmailVerified bool   `json:"email_verified"`
}

func (g *Google) VerifyGoogle(ctx context.Context, token string) (GoogleIdentity, error) {
	var c googleClaims
	_, err := jwt.ParseWithClaims(token, &c, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return g.Keys.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithExpirationRequired(), jwt.WithLeeway(time.Minute))
	if err != nil {
		if errors.Is(err, jwt.ErrTokenSignatureInvalid) || errors.Is(err, jwt.ErrTokenMalformed) || errors.Is(err, jwt.ErrTokenInvalidClaims) || errors.Is(err, ErrProviderRejected) {
			return GoogleIdentity{}, ErrProviderRejected
		}
		return GoogleIdentity{}, err
	}
	if c.Issuer != "https://accounts.google.com" && c.Issuer != "accounts.google.com" {
		return GoogleIdentity{}, ErrProviderRejected
	}
	allowed := false
	for _, aud := range c.Audience {
		for _, id := range g.ClientIDs {
			if id != "" && aud == id {
				allowed = true
			}
		}
	}
	email, err := NormalizeEmail(c.Email)
	if !allowed || c.Subject == "" || !c.EmailVerified || err != nil {
		return GoogleIdentity{}, ErrProviderRejected
	}
	return GoogleIdentity{Subject: c.Subject, Email: email}, nil
}

type DevGoogle struct{}

func (DevGoogle) VerifyGoogle(_ context.Context, token string) (GoogleIdentity, error) {
	sub, ok := strings.CutPrefix(token, "dev:")
	email, err := NormalizeEmail(sub)
	if !ok || err != nil {
		return GoogleIdentity{}, ErrProviderRejected
	}
	return GoogleIdentity{Subject: sub, Email: email}, nil
}
func (Unconfigured) VerifyGoogle(context.Context, string) (GoogleIdentity, error) {
	return GoogleIdentity{}, unconfigured()
}
