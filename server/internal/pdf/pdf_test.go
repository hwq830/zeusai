package pdf

import (
	"bytes"
	"context"
	"os"
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
