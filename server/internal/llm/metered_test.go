package llm_test

import (
	"context"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
)

type fixed struct{ usage llm.Usage }

func (f fixed) Complete(context.Context, llm.Request) (llm.Response, error) {
	return llm.Response{Content: "ok", Provider: "p", Model: "m", Usage: f.usage}, nil
}

func TestMeteredRecordsBilledTokens(t *testing.T) {
	ctx := context.Background()
	q := db.New(testdb.New(t))
	phone := "+8613800000009"
	u, err := q.CreateUser(ctx, db.CreateUserParams{Nickname: "u", Phone: &phone, Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now().Add(-time.Minute)
	m := llm.Metered{Provider: fixed{llm.Usage{InputTokens: 1000, OutputTokens: 200, CachedTokens: 300}}, Q: q}
	for range 2 {
		if _, err := m.Complete(ctx, llm.Request{UserID: &u.ID}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := q.SumBilledTokensSince(ctx, db.SumBilledTokensSinceParams{UserID: &u.ID, CreatedAt: start})
	if err != nil {
		t.Fatal(err)
	}
	if got != 2400 { // neutral weights: (700 uncached + 300 cached + 200 output) × 2
		t.Fatalf("billed tokens = %d, want 2400", got)
	}
}
