package search

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/calllog"
)

type searchCallStore struct {
	starts []calllog.Begin
	ends   map[uuid.UUID]calllog.End
}

func (s *searchCallStore) Start(_ context.Context, b calllog.Begin) error {
	s.starts = append(s.starts, b)
	return nil
}
func (s *searchCallStore) Finish(_ context.Context, id uuid.UUID, e calllog.End) error {
	if s.ends == nil {
		s.ends = map[uuid.UUID]calllog.End{}
	}
	s.ends[id] = e
	return nil
}
func TestSearchAttemptsAreIndependentAndLinkedToTask(t *testing.T) {
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "do not persist raw secrets", 503) }))
	defer bad.Close()
	good := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "search-123")
		_ = json.NewEncoder(w).Encode(map[string]any{"results": []any{map[string]string{"title": "t", "url": "https://example.com", "content": "c"}}})
	}))
	defer good.Close()
	store := &searchCallStore{}
	r := &Router{Calls: &calllog.Recorder{Store: store}, Keys: fakeKeys{"bad": "k", "good": "k"}, Fallback: []Provider{
		{Name: "missing", Protocol: ProtocolTavily}, {Name: "bad", Protocol: ProtocolTavily, BaseURL: bad.URL}, {Name: "good", Protocol: ProtocolTavily, BaseURL: good.URL},
	}}
	task := uuid.New()
	ctx := calllog.WithScope(context.Background(), calllog.Scope{TaskID: &task, Purpose: "briefing.section"})
	if _, err := r.Search(ctx, Query{Q: "news", Region: "TW"}); err != nil {
		t.Fatal(err)
	}
	if len(store.starts) != 3 {
		t.Fatalf("want skip+error+success, got %d", len(store.starts))
	}
	for _, b := range store.starts {
		if b.GroupID != store.starts[0].GroupID || *b.TaskID != task {
			t.Fatal("lost task/group linkage")
		}
	}
	if store.starts[0].Dispatched || store.ends[store.starts[0].ID].Status != "skipped" {
		t.Fatal("missing-key skip counted as a request")
	}
	if e := store.ends[store.starts[1].ID]; e.Status != "failed" || e.ErrorCode != "http_503" {
		t.Fatalf("missing upstream failure: %+v", e)
	}
	last := store.starts[2]
	e := store.ends[last.ID]
	if last.Query != "news" || last.Region != "TW" || e.Status != "success" || e.ResultCount == nil || *e.ResultCount != 1 || e.RequestID != "search-123" {
		t.Fatalf("missing search metadata: %+v %+v", last, e)
	}
}
