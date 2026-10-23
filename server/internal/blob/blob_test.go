package blob

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLocal(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	l := &Local{Dir: t.TempDir(), Secret: []byte("s"), Now: func() time.Time { return now }}
	srv := httptest.NewServer(http.StripPrefix("/files", l.Handler()))
	defer srv.Close()
	l.BaseURL = srv.URL
	ctx := context.Background()
	if err := l.Put(ctx, "reports/u/r.pdf", []byte("%PDF-1.7"), "application/pdf"); err != nil {
		t.Fatal(err)
	}
	if err := l.Put(ctx, "../x", nil, ""); err == nil {
		t.Fatal("path traversal accepted")
	}
	u, exp, err := l.SignedURL(ctx, "reports/u/r.pdf", time.Minute)
	if err != nil || !exp.Equal(now.Add(time.Minute)) {
		t.Fatal(err, exp)
	}
	get := func(u string) (int, string) {
		resp, err := http.Get(u)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(b)
	}
	if code, body := get(u); code != 200 || body != "%PDF-1.7" {
		t.Fatalf("%d %q", code, body)
	}
	if code, _ := get(strings.Replace(u, "r.pdf", "s.pdf", 1)); code != 403 {
		t.Fatalf("other key with same signature: %d", code)
	}
	if err := l.Delete(ctx, "reports/u/r.pdf"); err != nil {
		t.Fatal(err)
	}
	if err := l.Delete(ctx, "reports/u/r.pdf"); err != nil {
		t.Fatal("deleting a missing file must succeed:", err)
	}
	if code, _ := get(u); code != 404 {
		t.Fatalf("deleted file: %d", code)
	}
	now = now.Add(2 * time.Minute)
	if code, _ := get(u); code != 403 {
		t.Fatalf("expired link: %d", code)
	}
}
