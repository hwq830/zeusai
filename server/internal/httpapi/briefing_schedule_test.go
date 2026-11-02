package httpapi_test

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"go.temporal.io/sdk/client"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/schedule"
	"github.com/hwq830/zeusai/server/internal/workflows"
)

// Exercise real schedule creation/update, worker dispatch, and child-ID deduplication.
func TestBriefingSavedNearDelivery(t *testing.T) {
	for _, mode := range []string{"create", "update", "resume"} {
		t.Run(mode, func(t *testing.T) {
			h := setup(t)
			user := h.login(randomPhone())
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			now := time.Now().UTC()
			tz := "UTC"
			// Keep this test's delivery on the same local date at any wall-clock hour.
			if now.Hour() == 23 {
				tz = "Asia/Shanghai"
			}
			loc, err := time.LoadLocation(tz)
			require.NoError(t, err)
			s := schedule.Schedule{Freq: "daily", Time: now.In(loc).Add(20 * time.Minute).Format("15:04"), TZ: tz}
			if mode != "create" {
				h.orch.Now = func() time.Time { return now.Add(-time.Hour) }
			}
			var sub struct{ ID string }
			h.do("POST", "/v1/briefing-subscriptions", map[string]any{
				"name": "临近送达时间", "topics": []string{"AI"}, "schedule": s, "pushEnabled": false,
			}, 201, &sub)
			t.Cleanup(func() { h.do("DELETE", "/v1/briefing-subscriptions/"+sub.ID, nil, 204, nil) })
			if mode == "resume" {
				h.do("PATCH", "/v1/briefing-subscriptions/"+sub.ID, map[string]any{"paused": true}, 200, nil)
			}
			h.orch.Now = nil
			if mode != "create" {
				h.do("PATCH", "/v1/briefing-subscriptions/"+sub.ID, map[string]any{"paused": false, "schedule": s}, 200, nil)
			}
			uid, sid := uuid.MustParse(user.Me.User.ID), uuid.MustParse(sub.ID)
			tasks := func() []db.Task {
				rows, err := h.q.ListTasks(ctx, db.ListTasksParams{UserID: uid, Limit: 100})
				require.NoError(t, err)
				return rows
			}
			require.Eventually(t, func() bool {
				rows := tasks()
				return len(rows) == 1 && rows[0].Status == "done"
			}, 15*time.Second, 50*time.Millisecond, "saving a subscription must start today's issue")
			// Repeat saves after the child has finished, then run the calendar entry point
			// directly. Neither case may generate a second task, even with push disabled.
			h.do("PATCH", "/v1/briefing-subscriptions/"+sub.ID, map[string]any{"schedule": s}, 200, nil)
			for i := 0; i < 2; i++ {
				r, err := h.orch.C.ExecuteWorkflow(ctx, client.StartWorkflowOptions{ID: uuid.NewString(), TaskQueue: h.orch.TaskQueue},
					workflows.ScheduledBriefingWorkflowName, orch.BriefingRun{UserID: uid, SubscriptionID: sid, Scheduled: true}, s)
				require.NoError(t, err)
				require.NoError(t, r.Get(ctx, nil))
			}
			require.Len(t, tasks(), 1)
			require.Empty(t, h.pushes.Notifications())
		})
	}
}
