package auth

import (
	"context"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const appleIssuer = "https://appleid.apple.com"

// Apple verifies Sign in with Apple identity tokens against Apple's published keys.
type Apple struct {
	// ClientIDs are the accepted audiences: the iOS bundle id (and a Services ID for web).
	ClientIDs []string
	KeysURL   string // default https://appleid.apple.com/auth/keys
	HTTP      *http.Client

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

func (a *Apple) fetchKeys(ctx context.Context) (map[string]*rsa.PublicKey, error) {
	u := a.KeysURL
	if u == "" {
		u = appleIssuer + "/auth/keys"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	hc := a.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("apple keys: HTTP %d", resp.StatusCode)
	}
	var set struct {
		Keys []struct{ Kid, Kty, N, E string } `json:"keys"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, err
	}
	out := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil {
			continue
		}
		out[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	return out, nil
}

// key returns the key for kid, refetching at most once a minute when it is unknown
// (Apple rotates keys) and otherwise once a day.
func (a *Apple) key(ctx context.Context, kid string) (*rsa.PublicKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if k, ok := a.keys[kid]; ok && time.Since(a.fetched) < 24*time.Hour {
		return k, nil
	}
	if a.keys == nil || time.Since(a.fetched) > time.Minute {
		keys, err := a.fetchKeys(ctx)
		if err != nil {
			return nil, err
		}
		a.keys, a.fetched = keys, time.Now()
	}
	if k, ok := a.keys[kid]; ok {
		return k, nil
	}
	return nil, ErrProviderRejected
}

func (a *Apple) Verify(ctx context.Context, identityToken string) (string, error) {
	var claims jwt.RegisteredClaims
	_, err := jwt.ParseWithClaims(identityToken, &claims, func(t *jwt.Token) (any, error) {
		kid, _ := t.Header["kid"].(string)
		return a.key(ctx, kid)
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(appleIssuer), jwt.WithExpirationRequired(), jwt.WithLeeway(time.Minute))
	if err != nil {
		if errors.Is(err, ErrProviderRejected) || errors.Is(err, jwt.ErrTokenInvalidClaims) || errors.Is(err, jwt.ErrTokenSignatureInvalid) ||
			errors.Is(err, jwt.ErrTokenMalformed) || errors.Is(err, jwt.ErrTokenExpired) || errors.Is(err, jwt.ErrTokenInvalidIssuer) {
			return "", ErrProviderRejected
		}
		return "", err
	}
	for _, aud := range claims.Audience {
		for _, id := range a.ClientIDs {
			if aud == id && claims.Subject != "" {
				return claims.Subject, nil
			}
		}
	}
	return "", ErrProviderRejected
}
