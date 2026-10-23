package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestAppleVerify(t *testing.T) {
	k, _ := rsa.GenerateKey(rand.Reader, 2048)
	other, _ := rsa.GenerateKey(rand.Reader, 2048)
	var fetches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fetches.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "K1", "kty": "RSA", "alg": "RS256",
			"n": base64.RawURLEncoding.EncodeToString(k.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(k.E)).Bytes()),
		}}})
	}))
	defer srv.Close()
	a := &Apple{ClientIDs: []string{"app.zeus"}, KeysURL: srv.URL}

	sign := func(key *rsa.PrivateKey, kid string, c jwt.MapClaims) string {
		tok := jwt.NewWithClaims(jwt.SigningMethodRS256, c)
		tok.Header["kid"] = kid
		s, _ := tok.SignedString(key)
		return s
	}
	good := jwt.MapClaims{"iss": appleIssuer, "aud": "app.zeus", "sub": "001.abc", "exp": time.Now().Add(time.Hour).Unix()}
	sub, err := a.Verify(context.Background(), sign(k, "K1", good))
	if err != nil || sub != "001.abc" {
		t.Fatal(sub, err)
	}
	bad := map[string]string{
		"wrong key":    sign(other, "K1", good),
		"unknown kid":  sign(k, "K2", good),
		"wrong aud":    sign(k, "K1", jwt.MapClaims{"iss": appleIssuer, "aud": "x", "sub": "s", "exp": time.Now().Add(time.Hour).Unix()}),
		"wrong issuer": sign(k, "K1", jwt.MapClaims{"iss": "https://evil", "aud": "app.zeus", "sub": "s", "exp": time.Now().Add(time.Hour).Unix()}),
		"expired":      sign(k, "K1", jwt.MapClaims{"iss": appleIssuer, "aud": "app.zeus", "sub": "s", "exp": time.Now().Add(-time.Hour).Unix()}),
		"no expiry":    sign(k, "K1", jwt.MapClaims{"iss": appleIssuer, "aud": "app.zeus", "sub": "s"}),
		"not a token":  "dev:123",
	}
	for name, tok := range bad {
		if _, err := a.Verify(context.Background(), tok); !errors.Is(err, ErrProviderRejected) {
			t.Errorf("%s: %v", name, err)
		}
	}
	// Unknown kids within a minute of the last fetch must not hit Apple again.
	if n := fetches.Load(); n != 1 {
		t.Errorf("key fetches: %d", n)
	}
}
