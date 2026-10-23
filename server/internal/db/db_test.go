package db_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/pgvector/pgvector-go"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
)

func ptr[T any](v T) *T { return &v }

func TestQueriesAgainstSchema(t *testing.T) {
	ctx := context.Background()
	q := db.New(testdb.New(t))

	alice, err := q.CreateUser(ctx, db.CreateUserParams{Nickname: "alice", Phone: ptr("+8613800000001"), Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	bob, err := q.CreateUser(ctx, db.CreateUserParams{Nickname: "bob", Phone: ptr("+8613800000002"), Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}

	sub, err := q.CreateSubscription(ctx, db.CreateSubscriptionParams{
		UserID: alice.ID, Name: "早报", FieldValues: json.RawMessage(`{}`), Topics: []string{"AI"},
		Schedule: json.RawMessage(`{"freq":"daily","time":"08:00","tz":"Asia/Shanghai"}`), Length: "standard", PushEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetSubscription(ctx, db.GetSubscriptionParams{ID: sub.ID, UserID: bob.ID}); err == nil {
		t.Fatal("bob must not read alice's subscription")
	}

	task, err := q.CreateTask(ctx, db.CreateTaskParams{UserID: alice.ID, Kind: "briefing", Title: "早报", Input: json.RawMessage(`{}`), SubscriptionID: &sub.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, st := range []struct {
		status string
		seq    int64
	}{{"running", 3}, {"running", 2}, {"done", 5}} {
		if err := q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: task.ID, UserID: alice.ID, Status: st.status, LastEventID: st.seq}); err != nil {
			t.Fatal(err)
		}
	}
	task, _ = q.GetTask(ctx, db.GetTaskParams{ID: task.ID, UserID: alice.ID})
	if task.LastEventID != 5 || task.StartedAt == nil || task.FinishedAt == nil {
		t.Fatalf("progress not applied: %+v", task)
	}

	today := time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)
	b1, err := q.StartBriefing(ctx, db.StartBriefingParams{UserID: alice.ID, SubscriptionID: sub.ID, TaskID: &task.ID, Title: "早报", Date: today})
	if err != nil {
		t.Fatal(err)
	}
	b2, err := q.StartBriefing(ctx, db.StartBriefingParams{UserID: alice.ID, SubscriptionID: sub.ID, TaskID: &task.ID, Title: "早报 v2", Date: today})
	if err != nil {
		t.Fatal(err)
	}
	if b1.ID != b2.ID {
		t.Fatal("same subscription and day must reuse the briefing row")
	}
	if err := q.UpsertBriefingFeedback(ctx, db.UpsertBriefingFeedbackParams{ID: b1.ID, UserID: bob.ID, ItemID: "", Rating: "useful"}); err != nil {
		t.Fatal(err)
	}
	if _, err := q.GetBriefingFeedback(ctx, db.GetBriefingFeedbackParams{BriefingID: b1.ID, UserID: alice.ID}); err == nil {
		t.Fatal("feedback from another user must be ignored")
	}

	if err := q.UpsertConnector(ctx, db.UpsertConnectorParams{ID: "web_search", Name: "网页搜索", Kind: "builtin", Tools: json.RawMessage(`[]`), Transport: "builtin", Available: true, DefaultOn: true}); err != nil {
		t.Fatal(err)
	}
	cs, err := q.ListConnectorsForUser(ctx, alice.ID)
	if err != nil || len(cs) != 1 || !cs[0].Enabled || cs[0].Authorized {
		t.Fatalf("connectors: %+v %v", cs, err)
	}

	vec := make([]float32, 1024)
	vec[0] = 1
	v := pgvector.NewVector(vec)
	if _, err := q.CreateMemory(ctx, db.CreateMemoryParams{UserID: alice.ID, Kind: "interest", Content: "关注新能源车", Embedding: &v}); err != nil {
		t.Fatal(err)
	}
	hits, err := q.SearchMemories(ctx, db.SearchMemoriesParams{UserID: alice.ID, Embedding: &v, Limit: 3})
	if err != nil || len(hits) != 1 {
		t.Fatalf("search memories: %v %v", hits, err)
	}

	if err := q.DeleteUser(ctx, alice.ID); err != nil {
		t.Fatal(err)
	}
	if n, _ := q.CountActiveSubscriptions(ctx, alice.ID); n != 0 {
		t.Fatal("account deletion must cascade")
	}
}
