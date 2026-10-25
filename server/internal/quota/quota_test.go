package quota_test

import (
	"context"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/quota"
)

func TestTokenQuota(t *testing.T) {
	ctx := context.Background()
	q := db.New(testdb.New(t))
	phone := "+8613800000011"
	u, err := q.CreateUser(ctx, db.CreateUserParams{Nickname: "u", Phone: &phone, Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	s := &quota.Service{Q: q}
	use := func(task *db.Task, n int64) {
		t.Helper()
		p := db.RecordModelUsageParams{UserID: &u.ID, Provider: "p", Model: "m", Tier: "strong", BilledTokens: n}
		if task != nil {
			p.TaskID = &task.ID
		}
		if err := q.RecordModelUsage(ctx, p); err != nil {
			t.Fatal(err)
		}
	}
	task := func(status string) db.Task {
		t.Helper()
		tk, err := q.CreateTask(ctx, db.CreateTaskParams{UserID: u.ID, Kind: "report", Title: "r", Input: []byte(`{}`)})
		if err != nil {
			t.Fatal(err)
		}
		if err := q.UpdateTaskProgress(ctx, db.UpdateTaskProgressParams{ID: tk.ID, UserID: u.ID, Status: status}); err != nil {
			t.Fatal(err)
		}
		return tk
	}

	done, failed := task("done"), task("failed")
	use(&done, 5_000_000)
	use(&failed, 2_000_000) // failed tasks are not charged
	tok, err := s.TokenUsage(ctx, u)
	if err != nil || tok.Used != 5_000_000 || tok.Limit != 7_500_000 || tok.ResetsAt.Day() != 1 {
		t.Fatalf("usage = %+v, %v", tok, err)
	}
	if th, _, _ := s.NewThreshold(ctx, u); th != 0 {
		t.Fatalf("threshold at 67%% = %d", th)
	}

	use(&done, 1_200_000) // 6.2M: 82%, 1.3M left
	if err := s.CheckReport(ctx, u); err != nil {
		t.Fatalf("report refused with 1.3M left: %v", err)
	}
	if th, _, _ := s.NewThreshold(ctx, u); th != 80 {
		t.Fatalf("threshold = %d, want 80", th)
	}
	if th, _, _ := s.NewThreshold(ctx, u); th != 0 {
		t.Fatalf("80%% reminder sent twice: %d", th)
	}

	use(nil, 1_000_000) // 7.2M: 300k left, below a report's 500k
	if err := s.CheckReport(ctx, u); err == nil {
		t.Fatal("report allowed with 300k left")
	}
	if err := s.CheckFollowup(ctx, u); err != nil {
		t.Fatalf("follow-up refused with 300k left: %v", err)
	}
	use(nil, 400_000) // over the limit
	if th, tok, _ := s.NewThreshold(ctx, u); th != 100 || tok.Remaining() != 0 {
		t.Fatalf("threshold = %d, remaining %d", th, tok.Remaining())
	}
	if err := s.CheckFollowup(ctx, u); err == nil {
		t.Fatal("follow-up allowed with nothing left")
	}

	// Next month starts from zero.
	s.Now = func() time.Time { return time.Now().AddDate(0, 1, 0) }
	if tok, _ := s.TokenUsage(ctx, u); tok.Used != 0 {
		t.Fatalf("next month used = %d", tok.Used)
	}
}
