package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// Serper calls serper.dev, which returns Google results (https://serper.dev).
type Serper struct {
	APIKey  string
	BaseURL string // default https://google.serper.dev
	HTTP    *http.Client
}

type serperRequest struct {
	Q   string `json:"q"`
	Num int    `json:"num"`
	TBS string `json:"tbs,omitempty"`
	GL  string `json:"gl,omitempty"`
	HL  string `json:"hl,omitempty"`
}

type serperResponse struct {
	Organic []struct {
		Title   string `json:"title"`
		Link    string `json:"link"`
		Snippet string `json:"snippet"`
	} `json:"organic"`
}

func (s Serper) Search(ctx context.Context, q Query) ([]Result, error) {
	if s.APIKey == "" {
		return nil, errors.New("serper: API key is not configured")
	}
	r := serperRequest{Q: withSite(q), Num: limit(q.Limit, 8, 20), GL: q.Country, HL: googleLang(q.Lang)}
	if tr := timeRange(q.FreshDays); tr != "" {
		r.TBS = "qdr:" + tr[:1] // qdr:d / w / m / y
	}
	body, _ := json.Marshal(r)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, orDefault(s.BaseURL, ProtocolSerper)+"/search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-API-KEY", s.APIKey)
	raw, err := do(s.HTTP, req, "serper")
	if err != nil {
		return nil, err
	}
	var sr serperResponse
	if err := json.Unmarshal(raw, &sr); err != nil {
		return nil, fmt.Errorf("serper: %w", err)
	}
	out := make([]Result, 0, len(sr.Organic))
	for _, o := range sr.Organic {
		// Serper's dates are relative ("3 days ago"), so PublishedAt stays empty.
		out = append(out, Result{Title: o.Title, URL: o.Link, Snippet: o.Snippet, SiteName: hostOf(o.Link)})
	}
	return out, nil
}
