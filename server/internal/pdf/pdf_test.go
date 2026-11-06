package pdf

import (
	"bytes"
	"context"
	"os"
	"strings"
	"testing"
)

func TestChromium(t *testing.T) {
	path := os.Getenv("ZEUS_TEST_CHROMIUM")
	if path == "" {
		t.Skip("ZEUS_TEST_CHROMIUM not set")
	}
	b, err := Chromium{Path: path, NoSandbox: true}.Render(context.Background(),
		[]byte(`<!doctype html><html><head><meta charset="utf-8"></head><body><h1>报告</h1><script>document.body.innerHTML=""</script></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(b, []byte("%PDF")) || len(b) < 1000 {
		t.Fatalf("not a PDF: %d bytes", len(b))
	}
}

func TestLockDown(t *testing.T) {
	got := string(lockDown([]byte("<!doctype html><HTML><HEAD><title>x</title>")))
	if got != "<!doctype html><HTML><HEAD>"+csp+"<title>x</title>" {
		t.Fatal(got)
	}
}

func TestLockDownKeepsPageCSPWithScriptHash(t *testing.T) {
	in := []byte(`<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; script-src 'sha256-abc='"></head><body></body></html>`)
	got := string(lockDown(in))
	if got != string(in) {
		t.Fatalf("page CSP with script hash must be kept:\n%s", got)
	}
}

func TestLockDownFailsClosedOnPageCSPWithoutHash(t *testing.T) {
	in := []byte(`<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src 'none'; script-src 'unsafe-inline'"></head><body></body></html>`)
	got := string(lockDown(in))
	if !strings.Contains(got, csp) {
		t.Fatalf("CSP without script hash must get the full deny policy: %s", got)
	}
}

func TestLockDownKeepsEscapedTemplateCSP(t *testing.T) {
	in := []byte(`<!doctype html><html><head><meta http-equiv="Content-Security-Policy" content="default-src &#39;none&#39;; script-src &#39;sha256-XZw06nvewRPGMxDh58qy6HuFTLU7AaQ&#43;ylwlzB2E0Cs=&#39;"></head><body></body></html>`)
	got := string(lockDown(in))
	if got != string(in) {
		t.Fatalf("escaped template CSP must be kept:\n%s", got)
	}
}
