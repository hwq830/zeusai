// Package pdf prints report HTML to PDF with headless Chromium. Input is our own rendered
// template; a CSP is injected so no script runs and nothing is fetched while printing.
package pdf

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
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

// lockDown puts the CSP first in <head>. (--blink-settings=scriptEnabled=false would be
// simpler but stops --print-to-pdf from writing anything.)
func lockDown(html []byte) []byte {
	if i := bytes.Index(bytes.ToLower(html), []byte("<head>")); i >= 0 {
		i += len("<head>")
		return append(append(append([]byte{}, html[:i]...), csp...), html[i:]...)
	}
	return append([]byte(csp), html...)
}
