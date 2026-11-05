// Package browse reads web pages as plain text for research. HTTP fetches pages directly with
// an SSRF guard; JavaScript-heavy pages need the headless browser in the per-task sandbox,
// which plugs in behind the same Reader interface.
package browse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/html"
	"golang.org/x/net/html/charset"
)

type Page struct {
	URL   string
	Title string
	Text  string
}

type Reader interface {
	Read(ctx context.Context, url string) (Page, error)
}

var ErrBlocked = errors.New("browse: address not allowed")

// blocked covers loopback, private, link-local, CGNAT (Aliyun metadata is 100.100.100.200)
// and other non-public ranges.
var blocked = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("224.0.0.0/3"),
	netip.MustParsePrefix("::/127"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
}

func allowed(addr netip.Addr) bool {
	addr = addr.Unmap()
	for _, p := range blocked {
		if p.Contains(addr) {
			return false
		}
	}
	return true
}

// HTTP fetches pages over the public internet only.
type HTTP struct {
	MaxBytes int64 // default 2 MiB
	Timeout  time.Duration
	// Proxy, when set, is the egress proxy every fetch goes through. The dial-time guard then
	// only sees the proxy's address, so the proxy must refuse non-public destinations itself;
	// literal IPs are still checked here.
	Proxy *url.URL
	// AllowPrivate disables the address guard; tests only.
	AllowPrivate bool
	client       *http.Client
}

func (h *HTTP) httpClient() *http.Client {
	if h.client != nil {
		return h.client
	}
	d := &net.Dialer{Timeout: 10 * time.Second}
	if !h.AllowPrivate && h.Proxy == nil {
		// Checked at connect time, after DNS, so rebinding and redirects are covered too.
		d.Control = func(_, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || !allowed(ap.Addr()) {
				return ErrBlocked
			}
			return nil
		}
	}
	timeout := h.Timeout
	if timeout == 0 {
		timeout = 20 * time.Second
	}
	h.client = &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{DialContext: d.DialContext, Proxy: func(*http.Request) (*url.URL, error) { return h.Proxy, nil }, TLSHandshakeTimeout: 10 * time.Second},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 3 {
				return errors.New("browse: too many redirects")
			}
			return checkURL(req.URL)
		},
	}
	return h.client
}

func checkURL(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("browse: scheme %q not allowed", u.Scheme)
	}
	if p := u.Port(); p != "" && p != "80" && p != "443" {
		return ErrBlocked
	}
	if a, err := netip.ParseAddr(strings.Trim(u.Hostname(), "[]")); err == nil && !allowed(a) {
		return ErrBlocked
	}
	if h := strings.ToLower(u.Hostname()); h == "localhost" || strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".internal") {
		return ErrBlocked
	}
	return nil
}

func (h *HTTP) Read(ctx context.Context, raw string) (Page, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return Page{}, err
	}
	if !h.AllowPrivate {
		if err := checkURL(u); err != nil {
			return Page{}, err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return Page{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ZeusBot/0.1)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9")
	resp, err := h.httpClient().Do(req)
	if err != nil {
		return Page{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Page{}, fmt.Errorf("browse: HTTP %d", resp.StatusCode)
	}
	ct := resp.Header.Get("Content-Type")
	if ct != "" && !strings.Contains(ct, "html") && !strings.HasPrefix(ct, "text/") {
		return Page{}, fmt.Errorf("browse: unsupported content type %q", ct)
	}
	max := h.MaxBytes
	if max <= 0 {
		max = 2 << 20
	}
	body, err := charset.NewReader(io.LimitReader(resp.Body, max), ct)
	if err != nil {
		return Page{}, err
	}
	title, text, err := Extract(body)
	if err != nil {
		return Page{}, err
	}
	return Page{URL: resp.Request.URL.String(), Title: title, Text: text}, nil
}

var skip = map[string]bool{"script": true, "style": true, "noscript": true, "svg": true, "nav": true,
	"header": true, "footer": true, "aside": true, "form": true, "iframe": true, "template": true}

var block = map[string]bool{"p": true, "div": true, "br": true, "li": true, "h1": true, "h2": true,
	"h3": true, "h4": true, "h5": true, "h6": true, "tr": true, "pre": true, "blockquote": true,
	"section": true, "article": true, "table": true, "ul": true, "ol": true}

// Extract returns the page title and its readable text, one block per line.
func Extract(r io.Reader) (title, text string, err error) {
	doc, err := html.Parse(r)
	if err != nil {
		return "", "", err
	}
	var b strings.Builder
	var walk func(n *html.Node)
	walk = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "title" && title == "" && n.FirstChild != nil {
				title = strings.TrimSpace(n.FirstChild.Data)
				return
			}
			if skip[n.Data] {
				return
			}
		}
		if n.Type == html.TextNode {
			if s := strings.Join(strings.Fields(n.Data), " "); s != "" {
				b.WriteString(s)
				b.WriteByte(' ')
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
		if n.Type == html.ElementNode && block[n.Data] {
			b.WriteByte('\n')
		}
	}
	walk(doc)
	var lines []string
	for _, l := range strings.Split(b.String(), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return title, strings.Join(lines, "\n"), nil
}
