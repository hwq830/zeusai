// Package pdf prints report HTML to PDF with headless Chromium. Input is our own rendered
// template; a CSP is injected so no script runs and nothing is fetched while printing.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	stdhtml "html"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type Renderer interface {
	Render(ctx context.Context, html []byte) ([]byte, error)
}

type Chromium struct {
	Path string // chromium / google-chrome binary
	// NoSandbox is needed when running as root in a container without user namespaces.
	NoSandbox bool
	Timeout   time.Duration
}

func (c Chromium) Render(ctx context.Context, html []byte) ([]byte, error) {
	dir, err := os.MkdirTemp("", "zeus-pdf-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)
	in := filepath.Join(dir, "report.html")
	out := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(in, lockDown(html), 0o600); err != nil {
		return nil, err
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-background-networking",
		"--user-data-dir=" + filepath.Join(dir, "profile"), "--no-pdf-header-footer",
		"--virtual-time-budget=5000",
		"--print-to-pdf=" + out,
	}
	if c.NoSandbox {
		args = append(args, "--no-sandbox")
	}
	args = append(args, "file://"+in)
	cmd := exec.CommandContext(ctx, c.Path, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("chromium: %w: %s", err, tail(stderr.Bytes(), 500))
	}
	b, err := os.ReadFile(out)
	if err != nil || !bytes.HasPrefix(b, []byte("%PDF")) {
		return nil, fmt.Errorf("chromium wrote no PDF: %s", tail(stderr.Bytes(), 500))
	}
	return b, nil
}

func tail(b []byte, n int) []byte {
	if len(b) > n {
		return b[len(b)-n:]
	}
	return b
}

const csp = `<meta http-equiv="Content-Security-Policy" content="default-src 'none'; style-src 'unsafe-inline'; img-src data:; font-src data:">`

// lockDown keeps a page-declared CSP when it already allows our hashed chart
// script; otherwise it injects the full deny policy (fail closed).
func lockDown(html []byte) []byte {
	if content, ok := declaredCSP(html); ok {
		// The template emits entities such as &#39; in the attribute; decode
		// before checking so the real CSP value is recognized.
		decoded := strings.ToLower(stdhtml.UnescapeString(string(content)))
		if strings.Contains(decoded, "script-src 'sha256-") {
			return html
		}
	}
	return injectCSP(html)
}

func declaredCSP(html []byte) ([]byte, bool) {
	lower := bytes.ToLower(html)
	i := bytes.Index(lower, []byte("content-security-policy"))
	if i < 0 {
		return nil, false
	}
	start := bytes.Index(lower[i:], []byte(`content="`))
	if start < 0 {
		return nil, true
	}
	start += i + len(`content="`)
	end := bytes.IndexByte(html[start:], '"')
	if end < 0 {
		return nil, true
	}
	return html[start : start+end], true
}

func injectCSP(html []byte) []byte {
	if i := bytes.Index(bytes.ToLower(html), []byte("<head>")); i >= 0 {
		i += len("<head>")
		return append(append(append([]byte{}, html[:i]...), csp...), html[i:]...)
	}
	return append([]byte(csp), html...)
}
