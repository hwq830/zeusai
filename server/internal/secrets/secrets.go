// Package secrets seals provider API keys saved from the admin console and resolves the key
// each upstream call should use: the active key in the database, else the environment variable.
package secrets

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
)

// Box encrypts with AES-256-GCM under a key derived from ZEUS_SECRETS_KEY. The provider name is
// bound as additional data, so a sealed key cannot be moved to another provider's row.
type Box struct {
	aead cipher.AEAD
}

// NewBox returns nil for an empty secret: saving keys from the console is then disabled.
func NewBox(secret string) (*Box, error) {
	if secret == "" {
		return nil, nil
	}
	if len(secret) < 32 {
		return nil, errors.New("ZEUS_SECRETS_KEY must be at least 32 characters (openssl rand -hex 32)")
	}
	k := sha256.Sum256([]byte(secret))
	block, err := aes.NewCipher(k[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Box{aead: aead}, nil
}

// Seal returns nonce || ciphertext.
func (b *Box) Seal(provider, plain string) []byte {
	nonce := make([]byte, b.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return b.aead.Seal(nonce, nonce, []byte(plain), []byte(provider))
}

func (b *Box) Open(provider string, sealed []byte) (string, error) {
	n := b.aead.NonceSize()
	if len(sealed) < n {
		return "", errors.New("secrets: sealed value too short")
	}
	p, err := b.aead.Open(nil, sealed[:n], sealed[n:], []byte(provider))
	if err != nil {
		return "", errors.New("secrets: cannot decrypt (was ZEUS_SECRETS_KEY changed?)")
	}
	return string(p), nil
}

// Last4 is what the console shows of a key.
func Last4(key string) string {
	r := []rune(key)
	if len(r) <= 4 {
		return string(r)
	}
	return string(r[len(r)-4:])
}

// Keys resolves provider keys. Active database keys are reloaded at most every Every (default
// one minute), so a key switched in the console takes effect without a restart; a failed
// reload keeps the previous set.
type Keys struct {
	Q     *db.Queries
	Box   *Box
	Log   *slog.Logger
	Every time.Duration

	mu     sync.Mutex
	keys   map[string]string
	loaded time.Time
}

// Key returns the console key for provider, else the value of envName.
func (k *Keys) Key(ctx context.Context, provider, envName string) string {
	if k != nil && k.Box != nil && k.Q != nil {
		if v := k.current(ctx)[provider]; v != "" {
			return v
		}
	}
	if envName == "" {
		return ""
	}
	return os.Getenv(envName)
}

// Source reports where Key would take provider's key from: "console", "env" or "".
func (k *Keys) Source(ctx context.Context, provider, envName string) string {
	if k != nil && k.Box != nil && k.Q != nil && k.current(ctx)[provider] != "" {
		return "console"
	}
	if envName != "" && os.Getenv(envName) != "" {
		return "env"
	}
	return ""
}

// Invalidate forces the next Key call to reload (the admin process calls it after a change).
func (k *Keys) Invalidate() {
	if k == nil {
		return
	}
	k.mu.Lock()
	k.loaded = time.Time{}
	k.mu.Unlock()
}

func (k *Keys) current(ctx context.Context) map[string]string {
	every := k.Every
	if every <= 0 {
		every = time.Minute
	}
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.keys != nil && time.Since(k.loaded) < every {
		return k.keys
	}
	rows, err := k.Q.ListActiveProviderKeys(ctx)
	if err != nil {
		if k.Log != nil {
			k.Log.Warn("provider keys reload failed", "err", err)
		}
		if k.keys == nil {
			return map[string]string{}
		}
		return k.keys
	}
	keys := make(map[string]string, len(rows))
	for _, r := range rows {
		v, err := k.Box.Open(r.Provider, r.Ciphertext)
		if err != nil {
			if k.Log != nil {
				k.Log.Error("provider key unreadable", "provider", r.Provider, "err", err)
			}
			continue
		}
		keys[r.Provider] = v
	}
	k.keys, k.loaded = keys, time.Now()
	return k.keys
}
