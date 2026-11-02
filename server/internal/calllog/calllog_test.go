package calllog

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
)

type memoryStore struct {
	mu              sync.Mutex
	starts          []Begin
	ends            map[uuid.UUID]End
	failStarts      int
	failEnds        int
	cancelledWrites bool
}

func (m *memoryStore) Start(ctx context.Context, b Begin) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		m.cancelledWrites = true
	}
	m.starts = append(m.starts, b)
	if m.failStarts > 0 {
		m.failStarts--
		return errors.New("temporary database failure")
	}
	return nil
}
func (m *memoryStore) Finish(ctx context.Context, id uuid.UUID, e End) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ctx.Err() != nil {
		m.cancelledWrites = true
	}
	if m.failEnds > 0 {
		m.failEnds--
		return errors.New("temporary database failure")
	}
	if m.ends == nil {
		m.ends = map[uuid.UUID]End{}
	}
	m.ends[id] = e
	return nil
}

func TestPersistenceSurvivesCancellationAndUsesStableIDs(t *testing.T) {
	store := &memoryStore{failStarts: 1, failEnds: 1}
	r := &Recorder{Store: store}
	ctx, cancel := context.WithCancel(context.Background())
	call, err := r.Start(ctx, Begin{Kind: "model", Provider: "qwen", Dispatched: true}, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	call.Finish(ctx, End{Status: "timeout", ErrorCode: "deadline_exceeded"})
	if store.cancelledWrites {
		t.Fatal("ledger writes used cancelled request context")
	}
	if len(store.starts) != 2 || store.starts[0].ID != store.starts[1].ID {
		t.Fatal("retry could duplicate the attempt")
	}
	if len(store.ends) != 1 || store.ends[call.begin.ID].Status != "timeout" {
		t.Fatalf("missing result: %+v", store.ends)
	}
}

func TestSkippedAttemptIsNotDispatched(t *testing.T) {
	store := &memoryStore{}
	r := &Recorder{Store: store}
	if err := r.Skip(context.Background(), Begin{Kind: "model", Provider: "qwen"}, "missing_key"); err != nil {
		t.Fatal(err)
	}
	b := store.starts[0]
	if b.Dispatched || store.ends[b.ID].Status != "skipped" {
		t.Fatal("skipped provider counted as a dispatched request")
	}
}
