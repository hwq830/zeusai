package workflows

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/converter"
	"go.temporal.io/sdk/mocks"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/testsuite"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

func TestBriefingCatchupWindow(t *testing.T) {
	s := schedule.Schedule{Freq: "daily", Time: "18:50", TZ: "Etc/GMT-8"}
	for _, tc := range []struct {
		now  string
		want bool
	}{
		{"2026-11-02T18:19:59+08:00", false},
		{"2026-11-02T18:20:00+08:00", true},
		{"2026-11-02T18:40:07+08:00", true}, // reported regression
		{"2026-11-02T18:50:00+08:00", true},
		{"2026-11-02T18:50:01+08:00", false},
	} {
		t.Run(tc.now, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.now)
			require.NoError(t, err)
			require.Equal(t, tc.want, needsBriefingCatchup(s, now))
		})
	}
	now := time.Date(2026, 11, 2, 10, 40, 0, 0, time.UTC) // Monday, 18:40 UTC+8
	s.Freq, s.Weekdays = "weekly", []int{2}
	require.False(t, needsBriefingCatchup(s, now))
	s.Weekdays = []int{1}
	require.True(t, needsBriefingCatchup(s, now))
	s.Freq, s.MonthDay = "monthly", 3
	require.False(t, needsBriefingCatchup(s, now))
	s.MonthDay = 2
	require.True(t, needsBriefingCatchup(s, now))
	s.Freq, s.Time, s.Weekdays = "daily", "00:05", nil
	require.True(t, needsBriefingCatchup(s, time.Date(2026, 11, 2, 16, 1, 0, 0, time.UTC)))
}

func TestUpsertBriefingCatchup(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		exists, paused, late bool
		alreadyRan           bool
	}{
		{"create late", false, false, true, false},
		{"update late", true, false, true, false},
		{"resume late", true, false, true, false},
		{"create early", false, false, false, false},
		{"update early", true, false, false, false},
		{"create paused", false, true, true, false},
		{"pause late", true, true, true, false},
		{"already ran today", true, false, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			c := mocks.NewClient(t)
			sc := mocks.NewScheduleClient(t)
			h := mocks.NewScheduleHandle(t)
			user, sub := uuid.New(), uuid.New()
			s := schedule.Schedule{Freq: "daily", Time: "18:50", TZ: "Asia/Shanghai"}
			now := time.Date(2026, 11, 2, 10, 40, 0, 0, time.UTC)
			if !tc.late {
				now = now.Add(-time.Hour)
			}
			c.On("ScheduleClient").Return(sc)
			sc.On("GetHandle", ctx, orch.ScheduleID(sub)).Return(h)
			catchup := tc.late && !tc.paused && !tc.alreadyRan
			if !tc.exists {
				h.On("Describe", ctx).Return(nil, serviceerror.NewNotFound("missing"))
				sc.On("Create", ctx, mock.MatchedBy(func(o client.ScheduleOptions) bool {
					a := o.Action.(*client.ScheduleWorkflowAction)
					return o.TriggerImmediately == catchup && o.Paused == tc.paused && a.Workflow == ScheduledBriefingWorkflowName && len(a.Args) == 2
				})).Return(h, nil).Once()
			} else {
				desc := &client.ScheduleDescription{}
				if tc.alreadyRan {
					desc.Info.RecentActions = []client.ScheduleActionResult{{ActualTime: now.Add(-time.Minute)}}
				}
				h.On("Describe", ctx).Return(desc, nil)
				h.On("Update", ctx, mock.Anything).Run(func(args mock.Arguments) {
					u, err := args.Get(1).(client.ScheduleUpdateOptions).DoUpdate(client.ScheduleUpdateInput{Description: client.ScheduleDescription{}})
					require.NoError(t, err)
					require.Equal(t, tc.paused, u.Schedule.State.Paused)
					require.Equal(t, ScheduledBriefingWorkflowName, u.Schedule.Action.(*client.ScheduleWorkflowAction).Workflow)
				}).Return(nil).Once()
				if catchup {
					h.On("Trigger", ctx, client.ScheduleTriggerOptions{Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP}).Return(nil).Once()
				}
			}
			oc := Client{C: c, TaskQueue: "test", Now: func() time.Time { return now }}
			require.NoError(t, oc.UpsertBriefingSchedule(ctx, user, sub, s, tc.paused))
		})
	}
}

func TestScheduledBriefingDailyIdentity(t *testing.T) {
	for _, childErr := range []error{nil, &temporal.ChildWorkflowExecutionAlreadyStartedError{}, errors.New("generation failed")} {
		var ts testsuite.WorkflowTestSuite
		env := ts.NewTestWorkflowEnvironment()
		env.SetStartTime(time.Date(2026, 11, 2, 10, 40, 7, 0, time.UTC))
		run := orch.BriefingRun{UserID: uuid.New(), SubscriptionID: uuid.New(), Scheduled: true}
		env.RegisterWorkflowWithOptions(BriefingWorkflow, workflow.RegisterOptions{Name: BriefingWorkflowName})
		env.OnWorkflow(BriefingWorkflowName, mock.Anything, run).Return(childErr).Once()
		env.SetOnChildWorkflowStartedListener(func(info *workflow.Info, _ workflow.Context, _ converter.EncodedValues) {
			require.Equal(t, "scheduled-briefing-"+run.SubscriptionID.String()+"-2026-11-02", info.WorkflowExecution.ID)
		})
		env.ExecuteWorkflow(ScheduledBriefingWorkflow, run, schedule.Schedule{Freq: "daily", Time: "18:50", TZ: "Etc/GMT-8"})
		if childErr != nil && !temporal.IsWorkflowExecutionAlreadyStartedError(childErr) {
			require.Error(t, env.GetWorkflowError())
		} else {
			require.NoError(t, env.GetWorkflowError())
		}
		env.AssertExpectations(t)
	}
}

func TestScheduledBriefingRepeatedTrigger(t *testing.T) {
	var ts testsuite.WorkflowTestSuite
	env := ts.NewTestWorkflowEnvironment()
	env.SetStartTime(time.Date(2026, 11, 2, 10, 40, 0, 0, time.UTC))
	run := orch.BriefingRun{UserID: uuid.New(), SubscriptionID: uuid.New(), Scheduled: true}
	s := schedule.Schedule{Freq: "daily", Time: "18:50", TZ: "Asia/Shanghai"}
	env.RegisterWorkflowWithOptions(BriefingWorkflow, workflow.RegisterOptions{Name: BriefingWorkflowName})
	env.OnWorkflow(BriefingWorkflowName, mock.Anything, run).Return(nil).Once()
	env.ExecuteWorkflow(func(ctx workflow.Context) error {
		if err := ScheduledBriefingWorkflow(ctx, run, s); err != nil {
			return err
		}
		return ScheduledBriefingWorkflow(ctx, run, s)
	})
	require.NoError(t, env.GetWorkflowError())
	env.AssertExpectations(t)
}
