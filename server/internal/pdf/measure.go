package pdf

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"html"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var metricsRe = regexp.MustCompile(`data-zeus-metrics="([^"]*)"`)

// Viewport is the measured horizontal layout at a fixed CSS width.
type Viewport struct {
	ScrollWidth int
	InnerWidth  int
}

// MeasureViewport measures the page at width CSS pixels. Chrome clamps its headless window to
// 500px, so the page is rendered inside a fixed-width iframe: an iframe establishes its own
// viewport, which makes media queries evaluate at exactly the requested width.
func (c Chromium) MeasureViewport(ctx context.Context, page []byte, width int) (Viewport, error) {
	dir, err := os.MkdirTemp("", "zeus-measure-")
	if err != nil {
		return Viewport{}, err
	}
	defer os.RemoveAll(dir)
	escaped := strings.NewReplacer("&", "&amp;", `"`, "&quot;", "<", "&lt;", ">", "&gt;").Replace(string(page))
	document := `<!doctype html><html><head><meta charset="utf-8"></head><body>` +
		`<iframe id="zeus-viewport" style="width:` + strconv.Itoa(width) + `px;height:1200px;border:0" srcdoc="` + escaped + `"></iframe>` +
		`<script>var f=document.getElementById("zeus-viewport");` +
		`function z(){try{var d=f.contentDocument;if(!d||!d.documentElement)return false;` +
		`document.documentElement.setAttribute("data-zeus-metrics",JSON.stringify({sw:d.documentElement.scrollWidth,iw:f.contentWindow.innerWidth}));return true}catch(e){return false}}` +
		`f.addEventListener("load",z);setTimeout(z,1500);</script></body></html>`
	in := filepath.Join(dir, "page.html")
	if err := os.WriteFile(in, []byte(document), 0o600); err != nil {
		return Viewport{}, err
	}
	timeout := c.Timeout
	if timeout == 0 {
		timeout = time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	args := []string{
		"--headless", "--disable-gpu", "--no-first-run", "--no-default-browser-check",
		"--disable-extensions", "--disable-background-networking", "--disable-dev-shm-usage",
		"--user-data-dir=" + filepath.Join(dir, "profile"),
		"--window-size=900,1300",
		"--virtual-time-budget=3000",
		"--dump-dom",
	}
	if c.NoSandbox {
		args = append(args, "--no-sandbox")
	}
	args = append(args, "file://"+in)
	cmd := exec.CommandContext(ctx, c.Path, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Viewport{}, fmt.Errorf("chromium: %w: %s", err, tail(stderr.Bytes(), 500))
	}
	match := metricsRe.FindSubmatch(stdout.Bytes())
	if len(match) != 2 {
		return Viewport{}, fmt.Errorf("chromium reported no layout metrics: %s", tail(stderr.Bytes(), 300))
	}
	var metrics struct {
		SW int `json:"sw"`
		IW int `json:"iw"`
	}
	if err := json.Unmarshal([]byte(html.UnescapeString(string(match[1]))), &metrics); err != nil {
		return Viewport{}, fmt.Errorf("chromium layout metrics: %w", err)
	}
	if metrics.IW <= 0 {
		return Viewport{}, fmt.Errorf("chromium reported innerWidth=%d", metrics.IW)
	}
	return Viewport{ScrollWidth: metrics.SW, InnerWidth: metrics.IW}, nil
}
