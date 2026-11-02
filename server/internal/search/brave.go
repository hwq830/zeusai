package search

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
)

// Brave calls the Brave Search web API (https://api-dashboard.search.brave.com).
type Brave struct {
	APIKey  string
	BaseURL string // default https://api.search.brave.com
	HTTP    *http.Client
}

type braveResponse struct {
	Web struct {
		Results []struct {
			Title       string `json:"title"`
			URL         string `json:"url"`
			Description string `json:"description"`
			PageAge     string `json:"page_age"`
			Profile     struct {
				Name string `json:"name"`
			} `json:"profile"`
		} `json:"results"`
	} `json:"web"`
}

func braveFreshness(days int) string {
	switch {
	case days <= 0:
		return ""
	case days <= 1:
		return "pd"
	case days <= 7:
		return "pw"
	case days <= 31:
		return "pm"
	default:
		return "py"
	}
}

func (b Brave) Search(ctx context.Context, q Query) ([]Result, error) {
	if b.APIKey == "" {
		return nil, errors.New("brave: API key is not configured")
	}
	v := url.Values{"q": {withSite(q)}, "count": {strconv.Itoa(limit(q.Limit, 8, 20))}}
	if f := braveFreshness(q.FreshDays); f != "" {
		v.Set("freshness", f)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, orDefault(b.BaseURL, ProtocolBrave)+"/res/v1/web/search?"+v.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("X-Subscription-Token", b.APIKey)
	raw, err := do(b.HTTP, req, "brave")
	if err != nil {
		return nil, err
	}
	var br braveResponse
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, fmt.Errorf("brave: %w", err)
	}
	out := make([]Result, 0, len(br.Web.Results))
	for _, r := range br.Web.Results {
		site := r.Profile.Name
		if site == "" {
			site = hostOf(r.URL)
		}
		out = append(out, Result{Title: stripTags(r.Title), URL: r.URL, Snippet: stripTags(r.Description), SiteName: site, PublishedAt: parseTime(r.PageAge)})
	}
	return out, nil
}
