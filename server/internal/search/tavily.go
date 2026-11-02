package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Tavily calls the Tavily Search API (https://docs.tavily.com).
type Tavily struct {
	APIKey  string
	BaseURL string // default https://api.tavily.com
	HTTP    *http.Client
}

type tavilyRequest struct {
	Query          string   `json:"query"`
	MaxResults     int      `json:"max_results"`
	TimeRange      string   `json:"time_range,omitempty"`
	IncludeDomains []string `json:"include_domains,omitempty"`
	SearchDepth    string   `json:"search_depth"`
	Topic          string   `json:"topic"`
	Country        string   `json:"country,omitempty"`
}

// tavilyCountries maps ISO codes to the country names Tavily takes (general topic only).
var tavilyCountries = map[string]string{"cn": "china", "tw": "taiwan", "hk": "hong kong", "sg": "singapore", "my": "malaysia", "us": "united states"}

type tavilyResponse struct {
	Results []struct {
		Title         string `json:"title"`
		URL           string `json:"url"`
		Content       string `json:"content"`
		PublishedDate string `json:"published_date"`
	} `json:"results"`
}

// timeRange maps a freshness window to day/week/month/year (shared by Tavily and Serper).
func timeRange(days int) string {
	switch {
	case days <= 0:
		return ""
	case days <= 1:
		return "day"
	case days <= 7:
		return "week"
	case days <= 31:
		return "month"
	default:
		return "year"
	}
}

func (t Tavily) Search(ctx context.Context, q Query) ([]Result, error) {
	if t.APIKey == "" {
		return nil, errors.New("tavily: API key is not configured")
	}
	r := tavilyRequest{Query: q.Q, MaxResults: limit(q.Limit, 8, 20), TimeRange: timeRange(q.FreshDays), SearchDepth: "basic", Topic: "general",
		Country: tavilyCountries[q.Country]}
	if q.Site != "" {
		r.IncludeDomains = []string{q.Site}
	}
	body, _ := json.Marshal(r)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, orDefault(t.BaseURL, ProtocolTavily)+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.APIKey)
	raw, err := do(t.HTTP, req, "tavily")
	if err != nil {
		return nil, err
	}
	var tr tavilyResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		return nil, fmt.Errorf("tavily: %w", err)
	}
	out := make([]Result, 0, len(tr.Results))
	for _, v := range tr.Results {
		out = append(out, Result{Title: v.Title, URL: v.URL, Snippet: v.Content, SiteName: hostOf(v.URL), PublishedAt: parseTime(v.PublishedDate)})
	}
	return out, nil
}
