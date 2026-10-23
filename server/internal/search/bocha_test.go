package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBocha(t *testing.T) {
	var got bochaRequest
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/web-search" || r.Header.Get("Authorization") != "Bearer k" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"code":200,"data":{"webPages":{"value":[
			{"name":"标题","url":"https://a.cn/1","snippet":"短","summary":"长摘要","siteName":"A","datePublished":"2026-10-22T08:00:00+08:00"},
			{"name":"二","url":"https://b.cn/2","snippet":"只有短摘要","siteName":"B","datePublished":""}]}}}`))
	}))
	defer s.Close()
	res, err := Bocha{APIKey: "k", BaseURL: s.URL}.Search(context.Background(), Query{Q: "新能源", FreshDays: 1, Limit: 5, Site: "a.cn"})
	if err != nil {
		t.Fatal(err)
	}
	if got.Query != "新能源" || got.Freshness != "oneDay" || got.Count != 5 || got.Include != "a.cn" || !got.Summary {
		t.Fatalf("request %+v", got)
	}
	if len(res) != 2 || res[0].Snippet != "长摘要" || res[1].Snippet != "只有短摘要" || res[0].PublishedAt == nil || res[1].PublishedAt != nil {
		t.Fatalf("results %+v", res)
	}
	if res[0].PublishedAt.Hour() != 0 {
		t.Fatalf("time not UTC: %v", res[0].PublishedAt)
	}
	if _, err := (Bocha{APIKey: "bad", BaseURL: s.URL}).Search(context.Background(), Query{Q: "x"}); err == nil {
		t.Fatal("401 must fail")
	}
}
