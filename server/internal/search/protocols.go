package search

import (
	"fmt"
	"net/http"
	"regexp"
	"strings"
)

// The search APIs Zeus can call. A provider added in the admin console picks one of these;
// several providers may share a protocol (e.g. two Tavily accounts).
const (
	ProtocolBocha  = "bocha"
	ProtocolTavily = "tavily"
	ProtocolBrave  = "brave"
	ProtocolSerper = "serper"
)

// ProtocolInfo describes a supported search API for the console.
type ProtocolInfo struct {
	ID, Name, DefaultBaseURL, Region, DocsURL string
}

// Protocols lists the supported APIs in display order.
var Protocols = []ProtocolInfo{
	{ID: ProtocolBocha, Name: "博查", DefaultBaseURL: "https://api.bochaai.com", Region: "中文内容为主", DocsURL: "https://open.bochaai.com"},
	{ID: ProtocolTavily, Name: "Tavily", DefaultBaseURL: "https://api.tavily.com", Region: "全球，面向 AI 检索", DocsURL: "https://docs.tavily.com"},
	{ID: ProtocolBrave, Name: "Brave Search", DefaultBaseURL: "https://api.search.brave.com", Region: "全球，独立索引", DocsURL: "https://api-dashboard.search.brave.com/app/documentation"},
	{ID: ProtocolSerper, Name: "Serper（Google 结果）", DefaultBaseURL: "https://google.serper.dev", Region: "全球，Google 结果", DocsURL: "https://serper.dev"},
}

// KnownProtocol reports whether id names a supported API.
func KnownProtocol(id string) bool {
	for _, p := range Protocols {
		if p.ID == id {
			return true
		}
	}
	return false
}

func orDefault(base, protocol string) string {
	if base = strings.TrimRight(strings.TrimSpace(base), "/"); base != "" {
		return base
	}
	for _, p := range Protocols {
		if p.ID == protocol {
			return p.DefaultBaseURL
		}
	}
	return ""
}

// New returns a searcher for protocol; baseURL "" means the API's default address.
func New(protocol, key, baseURL string, hc *http.Client) (Searcher, error) {
	switch protocol {
	case ProtocolBocha:
		return Bocha{APIKey: key, BaseURL: baseURL, HTTP: hc}, nil
	case ProtocolTavily:
		return Tavily{APIKey: key, BaseURL: baseURL, HTTP: hc}, nil
	case ProtocolBrave:
		return Brave{APIKey: key, BaseURL: baseURL, HTTP: hc}, nil
	case ProtocolSerper:
		return Serper{APIKey: key, BaseURL: baseURL, HTTP: hc}, nil
	}
	return nil, fmt.Errorf("unknown search protocol %q", protocol)
}

// googleLang maps a search language to Google's hl value (Serper).
func googleLang(l string) string {
	switch l {
	case "zh-hant":
		return "zh-tw"
	case "zh-hans":
		return "zh-cn"
	}
	return l
}

// withSite narrows a query to one site with the common "site:" operator.
func withSite(q Query) string {
	if q.Site == "" {
		return q.Q
	}
	return q.Q + " site:" + q.Site
}

var tags = regexp.MustCompile(`<[^>]+>`)

func stripTags(s string) string {
	s = tags.ReplaceAllString(s, "")
	return strings.NewReplacer("&quot;", `"`, "&#39;", "'", "&amp;", "&", "&lt;", "<", "&gt;", ">").Replace(s)
}

func hostOf(u string) string {
	u = strings.TrimPrefix(strings.TrimPrefix(u, "https://"), "http://")
	if i := strings.IndexAny(u, "/?#"); i >= 0 {
		u = u[:i]
	}
	return strings.TrimPrefix(u, "www.")
}
