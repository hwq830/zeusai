package search

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

type fakeKeys map[string]string

func (f fakeKeys) Key(_ context.Context, p, _ string) string { return f[p] }
func (f fakeKeys) BaseURL(context.Context, string) string    { return "" }

func TestRouterFallsBackAndReloads(t *testing.T) {
	var badHits atomic.Int32
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		badHits.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	}))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://www.x.com/a","content":"c"}]}`))
	}))
	defer good.Close()
	now := time.Unix(1000, 0)
	chain := []Provider{{Name: "b1", Protocol: ProtocolBocha, BaseURL: bad.URL}, {Name: "nokey", Protocol: ProtocolBrave},
		{Name: "t1", Protocol: ProtocolTavily, BaseURL: good.URL}}
	loads := 0
	r := &Router{Keys: fakeKeys{"b1": "k", "t1": "k"}, Now: func() time.Time { return now },
		Load: func(context.Context) ([]Provider, error) { loads++; return chain, nil }}
	res, err := r.Search(context.Background(), Query{Q: "x"})
	if err != nil || len(res) != 1 || res[0].SiteName != "x.com" {
		t.Fatalf("res %+v err %v", res, err)
	}
	// The failed primary is tried last while cooling down.
	if _, err := r.Search(context.Background(), Query{Q: "x"}); err != nil || badHits.Load() != 1 {
		t.Fatalf("cooldown not applied: hits %d err %v", badHits.Load(), err)
	}
	now = now.Add(2 * time.Minute)
	chain = []Provider{{Name: "b1", Protocol: ProtocolBocha, BaseURL: bad.URL}}
	if _, err := r.Search(context.Background(), Query{Q: "x"}); err == nil || loads != 2 {
		t.Fatalf("want reload and failure, loads %d err %v", loads, err)
	}
}

func TestRouterEmptyAndLoadError(t *testing.T) {
	r := &Router{Load: func(context.Context) ([]Provider, error) { return nil, errors.New("db down") }}
	if _, err := r.Search(context.Background(), Query{Q: "x"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("err %v", err)
	}
}

func TestTavilyBraveSerper(t *testing.T) {
	var got map[string]any
	var gotURL string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotURL = r.URL.String()
		got = nil
		_ = json.NewDecoder(r.Body).Decode(&got)
		switch r.URL.Path {
		case "/search":
			if r.Header.Get("X-API-KEY") == "k" {
				_, _ = w.Write([]byte(`{"organic":[{"title":"S","link":"https://s.com/1","snippet":"sn","date":"2 days ago"}]}`))
				return
			}
			if r.Header.Get("Authorization") != "Bearer k" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"results":[{"title":"T","url":"https://t.com/1","content":"tc","published_date":"2026-10-30"}]}`))
		case "/res/v1/web/search":
			if r.Header.Get("X-Subscription-Token") != "k" {
				http.Error(w, "no", http.StatusUnauthorized)
				return
			}
			_, _ = w.Write([]byte(`{"web":{"results":[{"title":"<strong>B</strong>","url":"https://b.com/1","description":"a &amp; <strong>b</strong>","page_age":"2026-10-29T10:00:00","profile":{"name":"BSite"}}]}}`))
		}
	}))
	defer s.Close()
	ctx := context.Background()
	q := Query{Q: "ev", Site: "a.com", FreshDays: 7, Limit: 3}

	res, err := Tavily{APIKey: "k", BaseURL: s.URL}.Search(ctx, q)
	if err != nil || len(res) != 1 || res[0].PublishedAt == nil || got["time_range"] != "week" || got["max_results"] != float64(3) {
		t.Fatalf("tavily %+v %v %v", res, err, got)
	}
	if _, err := (Tavily{APIKey: "bad", BaseURL: s.URL}).Search(ctx, q); err == nil {
		t.Fatal("401 must fail")
	} else if se := (*StatusError)(nil); !errors.As(err, &se) || se.Status != 401 {
		t.Fatalf("want StatusError, got %v", err)
	}

	res, err = Brave{APIKey: "k", BaseURL: s.URL}.Search(ctx, q)
	if err != nil || len(res) != 1 || res[0].Title != "B" || res[0].Snippet != "a & b" || res[0].SiteName != "BSite" || res[0].PublishedAt == nil {
		t.Fatalf("brave %+v %v", res, err)
	}
	if want := "/res/v1/web/search?count=3&freshness=pw&q=ev+site%3Aa.com"; gotURL != want {
		t.Fatalf("brave url %s", gotURL)
	}

	res, err = Serper{APIKey: "k", BaseURL: s.URL}.Search(ctx, q)
	if err != nil || len(res) != 1 || res[0].URL != "https://s.com/1" || got["tbs"] != "qdr:w" || got["q"] != "ev site:a.com" {
		t.Fatalf("serper %+v %v %v", res, err, got)
	}
}

func TestLocaleParams(t *testing.T) {
	var body map[string]any
	var rawQuery string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rawQuery = r.URL.RawQuery
		body = nil
		_ = json.NewDecoder(r.Body).Decode(&body)
		_, _ = w.Write([]byte(`{}`))
	}))
	defer s.Close()
	ctx := context.Background()
	q := Query{Q: "x", Country: "tw", Lang: "zh-hant"}
	if _, err := (Serper{APIKey: "k", BaseURL: s.URL}).Search(ctx, q); err != nil || body["gl"] != "tw" || body["hl"] != "zh-tw" {
		t.Fatalf("serper %v %v", body, err)
	}
	if _, err := (Tavily{APIKey: "k", BaseURL: s.URL}).Search(ctx, q); err != nil || body["country"] != "taiwan" {
		t.Fatalf("tavily %v %v", body, err)
	}
	if _, err := (Brave{APIKey: "k", BaseURL: s.URL}).Search(ctx, q); err != nil || rawQuery != "count=8&country=TW&q=x&search_lang=zh-hant" {
		t.Fatalf("brave %s %v", rawQuery, err)
	}
	// Brave rejects countries outside its list, so Singapore is left out.
	if _, err := (Brave{APIKey: "k", BaseURL: s.URL}).Search(ctx, Query{Q: "x", Country: "sg"}); err != nil || rawQuery != "count=8&q=x" {
		t.Fatalf("brave sg %s %v", rawQuery, err)
	}
}
