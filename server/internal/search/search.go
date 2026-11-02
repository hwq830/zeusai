// Package search is the web search used by briefings and reports. Candidates for production
// (博查, 阿里云 IQS) are picked with the eval set; Mock returns stable fake results.
package search

import (
	"context"
	"fmt"
	"hash/fnv"
	"strings"
	"time"
)

type Query struct {
	// Region preserves other/global independently of API country support.
	Region    string
	Q         string
	Site      string
	FreshDays int
	Limit     int
	// Country (ISO 3166, lower case, e.g. "tw") and Lang ("zh-hant", "zh-hans", "en") ask for
	// local results where the API supports it; empty means no preference.
	Country string
	Lang    string
}

type Result struct {
	Title       string     `json:"title"`
	URL         string     `json:"url"`
	Snippet     string     `json:"snippet"`
	SiteName    string     `json:"siteName,omitempty"`
	PublishedAt *time.Time `json:"publishedAt,omitempty"`
}

type Searcher interface {
	Search(ctx context.Context, q Query) ([]Result, error)
}

type Mock struct{ Now func() time.Time }

func (m Mock) Search(_ context.Context, q Query) ([]Result, error) {
	now := time.Now()
	if m.Now != nil {
		now = m.Now()
	}
	h := fnv.New32a()
	h.Write([]byte(q.Q + q.Site))
	seed := h.Sum32()
	n := q.Limit
	if n <= 0 || n > 8 {
		n = 5
	}
	site := q.Site
	if site == "" {
		site = "news.example.com"
	}
	topic := strings.TrimSpace(q.Q)
	out := make([]Result, 0, n)
	for i := 0; i < n; i++ {
		id := seed + uint32(i)
		pub := now.Add(-time.Duration(i+1) * 3 * time.Hour).UTC()
		out = append(out, Result{
			Title:       fmt.Sprintf("%s：第 %d 条进展", topic, i+1),
			URL:         fmt.Sprintf("https://%s/a/%08x", site, id),
			Snippet:     fmt.Sprintf("关于「%s」的报道摘要 #%d，包含关键数字 %d%%。", topic, i+1, id%90+10),
			SiteName:    site,
			PublishedAt: &pub,
		})
	}
	return out, nil
}
