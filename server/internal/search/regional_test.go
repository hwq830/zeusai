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

func TestRegionalRoutesAndCachedSnapshot(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]any{"title": r.URL.Path, "url": "https://example.com"}}})
	}))
	defer s.Close()
	p := func(name string) Provider {
		return Provider{Name: name, Protocol: ProtocolTavily, BaseURL: s.URL + "/" + name}
	}
	loads := 0
	r := &Router{Keys: fakeKeys{"global": "k", "cn": "k", "other": "k"}, LoadRouting: func(context.Context) (Routing, error) {
		loads++
		return Routing{Default: []Provider{p("global")}, Regions: map[string][]Provider{"CN": {p("cn")}, "other": {p("other")}, "HK": {}}}, nil
	}}
	for _, tc := range []struct {
		q    Query
		want string
	}{
		{Query{Country: "cn"}, "/cn/search"},
		{Query{Region: "CN", Country: "us"}, "/cn/search"},
		{Query{Region: "global", Country: "cn"}, "/global/search"},
		{Query{Region: "other"}, "/other/search"},
		{Query{Country: "tw"}, "/global/search"},
	} {
		out, err := r.Search(context.Background(), tc.q)
		if err != nil || len(out) != 1 || out[0].Title != tc.want {
			t.Fatalf("%+v: %+v %v", tc.q, out, err)
		}
	}
	if loads != 1 {
		t.Fatalf("snapshot loaded %d times", loads)
	}
	if _, err := r.Search(context.Background(), Query{Country: "hk"}); !errors.Is(err, ErrNoProvider) {
		t.Fatalf("empty explicit route must not inherit: %v", err)
	}
	r.Invalidate()
	r.LoadRouting = func(context.Context) (Routing, error) { return Routing{}, errors.New("database down") }
	out, err := r.Search(context.Background(), Query{Country: "cn"})
	if err != nil || out[0].Title != "/cn/search" {
		t.Fatalf("lost last good routing: %+v %v", out, err)
	}
}

func TestEmptySearchOnlySupplementsOnceAndPreservesQuery(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body["country"] != "taiwan" || body["time_range"] != "day" || body["include_domains"].([]any)[0] != "example.com" {
			t.Errorf("lost filters: %+v", body)
		}
		_, _ = w.Write([]byte(`{"results":[]}`))
	}))
	defer s.Close()
	r := &Router{Keys: fakeKeys{"a": "k", "b": "k", "c": "k"}}
	for _, name := range []string{"a", "b", "c"} {
		r.Fallback = append(r.Fallback, Provider{Name: name, Protocol: ProtocolTavily, BaseURL: s.URL})
	}
	out, err := r.Search(context.Background(), Query{Q: "test", Country: "tw", Site: "example.com", FreshDays: 1})
	if err != nil || len(out) != 0 || hits.Load() != 2 {
		t.Fatalf("%+v %v hits=%d", out, err, hits.Load())
	}
}

func TestAttemptTimeoutFallsBackAndCancellationStops(t *testing.T) {
	var hits atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		hits.Add(1)
		if r.URL.Path == "/slow/search" {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(`{"results":[{"title":"ok","url":"https://example.com"}]}`))
	}))
	defer s.Close()
	r := &Router{Keys: fakeKeys{"slow": "k", "good": "k"}, AttemptTimeout: 20 * time.Millisecond, TotalTimeout: time.Second,
		Fallback: []Provider{{Name: "slow", Protocol: ProtocolTavily, BaseURL: s.URL + "/slow"}, {Name: "good", Protocol: ProtocolTavily, BaseURL: s.URL + "/good"}}}
	out, err := r.Search(context.Background(), Query{Q: "x"})
	if err != nil || len(out) != 1 {
		t.Fatalf("timeout fallback: %+v %v", out, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := hits.Load()
	if _, err := r.Search(ctx, Query{}); !errors.Is(err, context.Canceled) || hits.Load() != before {
		t.Fatalf("cancelled request called provider: %v", err)
	}
	r = &Router{Keys: fakeKeys{"slow": "k"}, TotalTimeout: 10 * time.Millisecond, AttemptTimeout: time.Second, Fallback: []Provider{{Name: "slow", Protocol: ProtocolTavily, BaseURL: s.URL + "/slow"}}}
	if _, err := r.Search(context.Background(), Query{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("total timeout: %v", err)
	}
}
