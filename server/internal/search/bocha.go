package search

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// Bocha calls the 博查 Web Search API. Field names follow its public docs
// (https://open.bochaai.com); re-check them when the account is opened.
type Bocha struct {
	APIKey string
	// KeyFunc, when set, supplies the key per call instead of APIKey.
	KeyFunc func(context.Context) string
	BaseURL string // default https://api.bochaai.com
	HTTP    *http.Client
}

type bochaRequest struct {
	Query     string `json:"query"`
	Freshness string `json:"freshness,omitempty"`
	Summary   bool   `json:"summary"`
	Count     int    `json:"count,omitempty"`
	Include   string `json:"include,omitempty"`
}

type bochaResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		WebPages struct {
			Value []struct {
				Name            string `json:"name"`
				URL             string `json:"url"`
				Snippet         string `json:"snippet"`
				Summary         string `json:"summary"`
				SiteName        string `json:"siteName"`
				DatePublished   string `json:"datePublished"`
				DateLastCrawled string `json:"dateLastCrawled"`
			} `json:"value"`
		} `json:"webPages"`
	} `json:"data"`
}

func freshness(days int) string {
	switch {
	case days <= 0:
		return "noLimit"
	case days <= 1:
		return "oneDay"
	case days <= 7:
		return "oneWeek"
	case days <= 31:
		return "oneMonth"
	default:
		return "oneYear"
	}
}

func (b Bocha) Search(ctx context.Context, q Query) ([]Result, error) {
	n := q.Limit
	if n <= 0 || n > 20 {
		n = 8
	}
	body, _ := json.Marshal(bochaRequest{Query: q.Q, Freshness: freshness(q.FreshDays), Summary: true, Count: n, Include: q.Site})
	base := b.BaseURL
	if base == "" {
		base = "https://api.bochaai.com"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/v1/web-search", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	key := b.APIKey
	if b.KeyFunc != nil {
		key = b.KeyFunc(ctx)
	}
	if key == "" {
		return nil, errors.New("bocha: API key is not configured")
	}
	req.Header.Set("Authorization", "Bearer "+key)
	hc := b.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 20 * time.Second}
	}
	resp, err := hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("bocha: HTTP %d: %s", resp.StatusCode, bytes.TrimSpace(raw[:min(len(raw), 300)]))
	}
	var br bochaResponse
	if err := json.Unmarshal(raw, &br); err != nil {
		return nil, fmt.Errorf("bocha: %w", err)
	}
	if br.Code != 0 && br.Code != 200 {
		return nil, fmt.Errorf("bocha: code %d: %s", br.Code, br.Msg)
	}
	out := make([]Result, 0, len(br.Data.WebPages.Value))
	for _, v := range br.Data.WebPages.Value {
		r := Result{Title: v.Name, URL: v.URL, Snippet: v.Summary, SiteName: v.SiteName}
		if r.Snippet == "" {
			r.Snippet = v.Snippet
		}
		for _, d := range []string{v.DatePublished, v.DateLastCrawled} {
			if t, err := time.Parse(time.RFC3339, d); err == nil {
				t = t.UTC()
				r.PublishedAt = &t
				break
			}
		}
		out = append(out, r)
	}
	return out, nil
}
