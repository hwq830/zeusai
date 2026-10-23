package workflows_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/stretchr/testify/mock"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/platform/testenv"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/search"
	"github.com/hwq830/zeusai/server/internal/workflows"
)

type fixture struct {
	q      *db.Queries
	acts   *workflows.Activities
	pushes *push.Log
	user   db.User
	now    time.Time
}

func newFixture(t *testing.T, now time.Time) *fixture {
	q := db.New(testdb.New(t))
	f := &fixture{q: q, pushes: &push.Log{}, now: now}
	f.acts = &workflows.Activities{Q: q, Bus: &events.Bus{R: testenv.Redis(t)}, LLM: llm.Mock{}, Search: search.Mock{}, Push: f.pushes, Now: func() time.Time { return f.now }}
	u, err := q.CreateUser(context.Background(), db.CreateUserParams{Nickname: "t", Timezone: "Asia/Shanghai"})
	if err != nil {
		t.Fatal(err)
	}
	f.user = u
	return f
}

func (f *fixture) subscription(t *testing.T) db.BriefingSubscription {
	ctx := context.Background()
	s, err := f.q.CreateSubscription(ctx, db.CreateSubscriptionParams{
		UserID: f.user.ID, Name: "AI 早报", FieldValues: json.RawMessage(`{}`), Topics: []string{"大模型"},
		Schedule: json.RawMessage(`{"freq":"daily","time":"08:00","tz":"Asia/Shanghai"}`), Length: "short", PushEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func (f *fixture) run(t *testing.T, start time.Time, run orch.BriefingRun) *testsuite.TestWorkflowEnvironment {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(start)
	env.RegisterWorkflow(workflows.BriefingWorkflow)
	env.RegisterActivity(f.acts)
	env.ExecuteWorkflow(workflows.BriefingWorkflow, run)
	if !env.IsWorkflowCompleted() {
		t.Fatal("workflow did not complete")
	}
	if err := env.GetWorkflowError(); err != nil {
		t.Fatal(err)
	}
	return env
}

func TestScheduledBriefingPushesAtDeliveryTime(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	start := time.Date(2026, 10, 23, 7, 38, 0, 0, sh) // the schedule fires ~22 minutes early
	f := newFixture(t, start)
	sub := f.subscription(t)

	env := f.run(t, start, orch.BriefingRun{UserID: f.user.ID, SubscriptionID: sub.ID, Scheduled: true})
	if got := env.Now().In(sh); got.Before(time.Date(2026, 10, 23, 8, 0, 0, 0, sh)) {
		t.Fatalf("pushed at %v, before the 08:00 delivery time", got)
	}
	if len(f.pushes.Sent) != 1 || f.pushes.Sent[0].Title != "AI 早报" {
		t.Fatalf("pushes: %+v", f.pushes.Sent)
	}

	ctx := context.Background()
	b, err := f.q.ListBriefingsOnDate(ctx, db.ListBriefingsOnDateParams{UserID: f.user.ID, Date: time.Date(2026, 10, 23, 0, 0, 0, 0, time.UTC)})
	if err != nil || len(b) != 1 || b[0].Status != "ready" || b[0].ItemCount == 0 {
		t.Fatalf("briefing: %+v %v", b, err)
	}
	if f.pushes.Sent[0].DeepLink != "zeus://briefing/"+b[0].ID.String() {
		t.Fatalf("deep link: %s", f.pushes.Sent[0].DeepLink)
	}
	tasks, _ := f.q.ListTasks(ctx, db.ListTasksParams{UserID: f.user.ID, Limit: 10})
	if len(tasks) != 1 || tasks[0].Status != "done" || tasks[0].Subtitle != "定时简报" {
		t.Fatalf("tasks: %+v", tasks)
	}
}

func TestScheduledBriefingPausesWhenUnread(t *testing.T) {
	sh, _ := time.LoadLocation("Asia/Shanghai")
	first := time.Date(2026, 11, 10, 7, 40, 0, 0, sh)
	f := newFixture(t, first)
	sub := f.subscription(t)
	f.run(t, first, orch.BriefingRun{UserID: f.user.ID, SubscriptionID: sub.ID, Scheduled: true})

	// Ten days later nobody has opened an issue: the run is skipped and the user told once.
	f.now = first.AddDate(0, 0, 10)
	f.run(t, f.now, orch.BriefingRun{UserID: f.user.ID, SubscriptionID: sub.ID, Scheduled: true})
	got, err := f.q.GetSubscription(context.Background(), db.GetSubscriptionParams{ID: sub.ID, UserID: f.user.ID})
	if err != nil || got.Status != "paused" || got.PausedReason == nil || *got.PausedReason != "inactive" {
		t.Fatalf("subscription: %+v %v", got, err)
	}
	if len(f.pushes.Sent) != 2 || f.pushes.Sent[1].Title != "AI 早报 已暂停" {
		t.Fatalf("pushes: %+v", f.pushes.Sent)
	}

	// Further scheduled firings do nothing.
	f.now = f.now.AddDate(0, 0, 1)
	f.run(t, f.now, orch.BriefingRun{UserID: f.user.ID, SubscriptionID: sub.ID, Scheduled: true})
	if len(f.pushes.Sent) != 2 {
		t.Fatalf("paused subscription pushed again: %+v", f.pushes.Sent)
	}
}

func TestReportFailureIsRecordedAndNotCharged(t *testing.T) {
	f := newFixture(t, time.Now())
	ctx := context.Background()
	task, err := f.q.CreateTask(ctx, db.CreateTaskParams{UserID: f.user.ID, Kind: "report", Title: "t", Input: json.RawMessage(`{}`)})
	if err != nil {
		t.Fatal(err)
	}
	rep, err := f.q.CreateReport(ctx, db.CreateReportParams{UserID: f.user.ID, TaskID: &task.ID, Title: "t", Topic: "储能电池", Depth: "quick", Focuses: []string{}})
	if err != nil {
		t.Fatal(err)
	}

	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.RegisterWorkflow(workflows.ReportWorkflow)
	env.RegisterActivity(f.acts)
	env.OnActivity(f.acts.ResearchQuestion, mock.Anything, mock.Anything, mock.Anything, mock.Anything).
		Return(workflows.QuestionNotes{}, temporal.NewNonRetryableApplicationError("search down", "test", nil))
	env.ExecuteWorkflow(workflows.ReportWorkflow, orch.ReportRun{UserID: f.user.ID, TaskID: task.ID, ReportID: rep.ID})
	if env.GetWorkflowError() == nil {
		t.Fatal("expected workflow error")
	}

	rep, _ = f.q.GetReport(ctx, db.GetReportParams{ID: rep.ID, UserID: f.user.ID})
	task, _ = f.q.GetTask(ctx, db.GetTaskParams{ID: task.ID, UserID: f.user.ID})
	if rep.Status != "failed" || task.Status != "failed" || task.Error == nil {
		t.Fatalf("report %s task %s err %s", rep.Status, task.Status, task.Error)
	}
	if n, _ := f.q.CountReportsSince(ctx, db.CountReportsSinceParams{UserID: f.user.ID, CreatedAt: time.Now().Add(-time.Hour)}); n != 0 {
		t.Fatalf("failed report counted against quota: %d", n)
	}
}
