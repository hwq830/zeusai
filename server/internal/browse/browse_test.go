package browse

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
)

func TestExtract(t *testing.T) {
	title, text, err := Extract(strings.NewReader(`<html><head><title> 标题 </title><style>x{}</style></head>
<body><nav>菜单</nav><article><h1>大标题</h1><p>第一段
  文字。</p><script>alert(1)</script><ul><li>要点一</li><li>要点二</li></ul></article><footer>版权</footer></body></html>`))
	if err != nil {
		t.Fatal(err)
	}
	if title != "标题" || text != "大标题\n第一段 文字。\n要点一\n要点二" {
		t.Fatalf("title %q text %q", title, text)
	}
}

func TestReadDecodesGBK(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=gbk")
		_, _ = w.Write([]byte("<title>\xd6\xd0\xce\xc4</title><p>ok</p>")) // 中文 in GBK
	}))
	defer s.Close()
	p, err := (&HTTP{AllowPrivate: true}).Read(context.Background(), s.URL)
	if err != nil {
		t.Fatal(err)
	}
	if p.Title != "中文" || p.Text != "ok" {
		t.Fatalf("%+v", p)
	}
}

func TestGuard(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer s.Close()
	h := &HTTP{}
	// httptest listens on 127.0.0.1 with a random port: both the port and the address are refused.
	if _, err := h.Read(context.Background(), s.URL); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback: %v", err)
	}
	if _, err := h.Read(context.Background(), "http://127.0.0.1/"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("loopback :80: %v", err)
	}
	if _, err := h.Read(context.Background(), "http://localhost/"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("localhost: %v", err)
	}
	// With an egress proxy the literal-IP check still applies before anything is sent.
	if _, err := (&HTTP{Proxy: mustURL("http://proxy.invalid:3128")}).Read(context.Background(), "http://100.100.100.200/latest/meta-data"); !errors.Is(err, ErrBlocked) {
		t.Fatalf("metadata via proxy: %v", err)
	}
	if _, err := h.Read(context.Background(), "file:///etc/passwd"); err == nil {
		t.Fatal("file scheme allowed")
	}
	for addr, ok := range map[string]bool{"100.100.100.200": false, "10.1.2.3": false, "::ffff:192.168.1.1": false,
		"fe80::1": false, "8.8.8.8": true, "2400:3200::1": true} {
		if allowed(netip.MustParseAddr(addr)) != ok {
			t.Errorf("%s: want allowed=%v", addr, ok)
		}
	}
}

func mustURL(s string) *url.URL {
	u, err := url.Parse(s)
	if err != nil {
		panic(err)
	}
	return u
}
