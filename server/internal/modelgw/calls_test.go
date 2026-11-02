package modelgw

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/calllog"
	"github.com/hwq830/zeusai/server/internal/llm"
)

type callStore struct {
	mu         sync.Mutex
	starts     []calllog.Begin
	ends       map[uuid.UUID]calllog.End
	startError error
}

func (s *callStore) Start(_ context.Context, b calllog.Begin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.starts = append(s.starts, b)
	return s.startError
}
func (s *callStore) Finish(_ context.Context, id uuid.UUID, e calllog.End) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ends == nil {
		s.ends = map[uuid.UUID]calllog.End{}
	}
	s.ends[id] = e
	return nil
}

func TestEveryModelAttemptIsRecordedIncludingTimeoutAndFallback(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var v any
		_ = json.NewDecoder(r.Body).Decode(&v)
		<-r.Context().Done()
	}))
	defer slow.Close()
	var hits atomic.Int32
	good := upstreamServer(t, 200, "ok", &hits)
	cfg := Config{Routes: map[llm.Tier][]string{llm.TierStrong: {"qwen", "deepseek"}, llm.TierFast: {"qwen", "deepseek"}}, BreakerFailures: 1,
		Providers: []ProviderConfig{
			{Name: "qwen", BaseURL: slow.URL, Timeout: 50 * time.Millisecond, Models: map[llm.Tier]string{llm.TierStrong: "qwen3.8-max"}},
			{Name: "deepseek", BaseURL: good.URL, Timeout: time.Second, Models: map[llm.Tier]string{llm.TierStrong: "deepseek-chat"}},
		}}
	store := &callStore{}
	g, err := New(cfg, nil, nil, WithCalls(&calllog.Recorder{Store: store}), WithKeys(mapKeys{"qwen": "k", "deepseek": "k"}))
	if err != nil {
		t.Fatal(err)
	}
	user, task := uuid.New(), uuid.New()
	req := llm.Request{Tier: llm.TierStrong, Purpose: "report.write", UserID: &user, TaskID: &task}
	if _, err := g.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if len(store.starts) != 2 {
		t.Fatalf("attempts %+v", store.starts)
	}
	first, second := store.starts[0], store.starts[1]
	if first.GroupID != second.GroupID || first.Attempt != 1 || second.Attempt != 2 || *first.TaskID != task {
		t.Fatalf("lost linkage: %+v %+v", first, second)
	}
	if e := store.ends[first.ID]; e.Status != "timeout" || e.UsageKnown {
		t.Fatalf("timeout was not unknown usage: %+v", e)
	}
	if e := store.ends[second.ID]; e.Status != "success" || !e.UsageKnown || e.InputTokens != 1000 || e.OutputTokens != 500 {
		t.Fatalf("successful fallback: %+v", e)
	}
	if _, err := g.Complete(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	skipped := store.starts[2]
	if skipped.Dispatched || store.ends[skipped.ID].ErrorCode != "circuit_open" {
		t.Fatalf("missing cooldown skip: %+v", skipped)
	}
	if skipped.GroupID == first.GroupID {
		t.Fatal("different logical requests share a group")
	}
}

func TestModelLedgerUnknownUsageAndPreflightPersistenceFailure(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"content":"ok"}}]}`))
	}))
	defer server.Close()
	store := &callStore{}
	client := llm.OpenAICompat{Name: "qwen", BaseURL: server.URL, Calls: &calllog.Recorder{Store: store}}
	if _, err := client.Complete(context.Background(), llm.Request{Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if e := store.ends[store.starts[0].ID]; e.Status != "success" || e.UsageKnown {
		t.Fatalf("missing usage was treated as zero: %+v", e)
	}
	store.startError = errors.New("database unavailable")
	if _, err := client.Complete(context.Background(), llm.Request{Model: "m"}); err == nil {
		t.Fatal("unrecorded external request was allowed")
	}
	if hits.Load() != 1 {
		t.Fatal("provider was called despite ledger write failure")
	}
}
