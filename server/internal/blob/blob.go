// Package blob stores generated files (report PDFs) and hands out short-lived download
// links. Production uses Aliyun OSS with presigned URLs; Local serves files from disk through
// the api's /files route with an HMAC-signed query, for dev and tests.
package blob

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Store interface {
	Put(ctx context.Context, key string, data []byte, contentType string) error
	// Delete removes a file; a missing file is not an error.
	Delete(ctx context.Context, key string) error
	// SignedURL returns a download link valid until the returned time.
	SignedURL(ctx context.Context, key string, ttl time.Duration) (string, time.Time, error)
}

var ErrNotFound = errors.New("blob: not found")

type Local struct {
	Dir     string
	BaseURL string // public api base, e.g. http://localhost:8080
	Secret  []byte
	Now     func() time.Time
}

func (l *Local) now() time.Time {
	if l.Now != nil {
		return l.Now()
	}
	return time.Now()
}

func validKey(key string) bool {
	return key != "" && !strings.Contains(key, "..") && !strings.HasPrefix(key, "/") && !strings.Contains(key, "\\")
}

func (l *Local) Put(_ context.Context, key string, data []byte, _ string) error {
	if !validKey(key) {
		return errors.New("blob: invalid key")
	}
	p := filepath.Join(l.Dir, filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

func (l *Local) Delete(_ context.Context, key string) error {
	if !validKey(key) {
		return errors.New("blob: invalid key")
	}
	err := os.Remove(filepath.Join(l.Dir, filepath.FromSlash(key)))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func (l *Local) sign(key string, exp int64) string {
	m := hmac.New(sha256.New, l.Secret)
	m.Write([]byte(key + "\n" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(m.Sum(nil))
}

func (l *Local) SignedURL(_ context.Context, key string, ttl time.Duration) (string, time.Time, error) {
	if !validKey(key) {
		return "", time.Time{}, errors.New("blob: invalid key")
	}
	exp := l.now().Add(ttl).Truncate(time.Second)
	q := url.Values{"exp": {strconv.FormatInt(exp.Unix(), 10)}, "sig": {l.sign(key, exp.Unix())}}
	return strings.TrimRight(l.BaseURL, "/") + "/files/" + key + "?" + q.Encode(), exp, nil
}

// Handler serves GET /files/{key...}; mount it with the "/files/" prefix stripped.
func (l *Local) Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/")
		exp, err := strconv.ParseInt(r.URL.Query().Get("exp"), 10, 64)
		if err != nil || !validKey(key) || l.now().Unix() > exp ||
			!hmac.Equal([]byte(l.sign(key, exp)), []byte(r.URL.Query().Get("sig"))) {
			http.Error(w, "link expired or invalid", http.StatusForbidden)
			return
		}
		f, err := os.Open(filepath.Join(l.Dir, filepath.FromSlash(key)))
		if err != nil {
			http.NotFound(w, r)
			return
		}
		defer f.Close()
		if strings.HasSuffix(key, ".pdf") {
			w.Header().Set("Content-Type", "application/pdf")
		}
		w.Header().Set("Cache-Control", "private, max-age=300")
		http.ServeContent(w, r, "", time.Time{}, f)
	})
}
