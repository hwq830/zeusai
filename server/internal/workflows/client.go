// Package workflows holds the Temporal workflows and activities run by the orchestrator, and
// the Temporal-backed orch.Client used by the api process.
package workflows

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/api/enums/v1"
	"go.temporal.io/api/serviceerror"
	"go.temporal.io/sdk/client"

	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

// Generation starts LeadTime before delivery, spread by up to Jitter so 8:00 peaks flatten.
const (
	LeadTime = 30 * time.Minute
	Jitter   = 15 * time.Minute
)

type Client struct {
	C         client.Client
	TaskQueue string
	Now       func() time.Time
}

var _ orch.Client = (*Client)(nil)

// calendar converts a delivery schedule to a Temporal calendar spec for the generation start.
func calendar(s schedule.Schedule) client.ScheduleCalendarSpec {
	h, m := s.Clock()
	start := h*60 + m - int(LeadTime/time.Minute)
	if start < 0 {
		start = 0 // deliveries just after midnight get less lead time instead of moving a day
	}
	cal := client.ScheduleCalendarSpec{
		Hour:   []client.ScheduleRange{{Start: start / 60}},
		Minute: []client.ScheduleRange{{Start: start % 60}},
	}
	switch s.Freq {
	case "daily", "weekly":
		if len(s.Weekdays) > 0 {
			for _, d := range s.Weekdays {
				cal.DayOfWeek = append(cal.DayOfWeek, client.ScheduleRange{Start: d % 7}) // Temporal: 0 = Sunday
			}
		}
	case "monthly":
		cal.DayOfMonth = []client.ScheduleRange{{Start: s.MonthDay}}
	}
	return cal
}

func (c *Client) UpsertBriefingSchedule(ctx context.Context, userID, subID uuid.UUID, s schedule.Schedule, paused bool) error {
	now := time.Now()
	if c.Now != nil {
		now = c.Now()
	}
	catchup := !paused && needsBriefingCatchup(s, now)
	id := orch.ScheduleID(subID)
	spec := client.ScheduleSpec{Calendars: []client.ScheduleCalendarSpec{calendar(s)}, TimeZoneName: s.TZ, Jitter: Jitter}
	action := &client.ScheduleWorkflowAction{
		ID:        "briefing-run-" + subID.String(),
		Workflow:  ScheduledBriefingWorkflowName,
		Args:      []any{orch.BriefingRun{UserID: userID, SubscriptionID: subID, Scheduled: true}, s},
		TaskQueue: c.TaskQueue,
	}
	h := c.C.ScheduleClient().GetHandle(ctx, id)
	desc, err := h.Describe(ctx)
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		_, err = c.C.ScheduleClient().Create(ctx, client.ScheduleOptions{
			ID: id, Spec: spec, Action: action, Paused: paused, Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP,
			TriggerImmediately: catchup,
		})
		return err
	}
	if err != nil {
		return err
	}
	// Avoid redundant catches, including legacy runs that predate the daily child
	// ID. The child ID still arbitrates triggers racing with this snapshot.
	for _, action := range desc.Info.RecentActions {
		if action.ActualTime.In(s.Location()).Format("2006-01-02") == now.In(s.Location()).Format("2006-01-02") {
			catchup = false
			break
		}
	}
	err = h.Update(ctx, client.ScheduleUpdateOptions{DoUpdate: func(in client.ScheduleUpdateInput) (*client.ScheduleUpdate, error) {
		sch := in.Description.Schedule
		sch.Spec = &spec
		sch.Action = action
		if sch.State == nil {
			sch.State = &client.ScheduleState{}
		}
		sch.State.Paused = paused
		return &client.ScheduleUpdate{Schedule: &sch}, nil
	}})
	if err != nil || !catchup {
		return err
	}
	return h.Trigger(ctx, client.ScheduleTriggerOptions{Overlap: enums.SCHEDULE_OVERLAP_POLICY_SKIP})
}

func (c *Client) DeleteBriefingSchedule(ctx context.Context, subID uuid.UUID) error {
	err := c.C.ScheduleClient().GetHandle(ctx, orch.ScheduleID(subID)).Delete(ctx)
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

func (c *Client) start(ctx context.Context, taskID uuid.UUID, wf string, arg any) error {
	_, err := c.C.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: orch.WorkflowID(taskID), TaskQueue: c.TaskQueue, WorkflowExecutionTimeout: 2 * time.Hour,
	}, wf, arg)
	return err
}

func (c *Client) StartBriefing(ctx context.Context, run orch.BriefingRun) error {
	return c.start(ctx, run.TaskID, BriefingWorkflowName, run)
}

func (c *Client) StartReport(ctx context.Context, run orch.ReportRun) error {
	return c.start(ctx, run.TaskID, ReportWorkflowName, run)
}

func (c *Client) StartFollowup(ctx context.Context, run orch.FollowupRun) error {
	return c.start(ctx, run.TaskID, FollowupWorkflowName, run)
}

func (c *Client) StartReportPDF(ctx context.Context, run orch.ReportPDFRun) error {
	_, err := c.C.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "pdf-" + run.ReportID.String(), TaskQueue: c.TaskQueue, WorkflowExecutionTimeout: 10 * time.Minute,
	}, ReportPDFWorkflowName, run)
	return err
}

func (c *Client) StartBriefingPDF(ctx context.Context, run orch.BriefingPDFRun) error {
	_, err := c.C.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: "pdf-briefing-" + run.BriefingID.String(), TaskQueue: c.TaskQueue, WorkflowExecutionTimeout: 10 * time.Minute,
	}, BriefingPDFWorkflowName, run)
	return err
}

func (c *Client) StartCampaign(ctx context.Context, run orch.CampaignRun) error {
	_, err := c.C.ExecuteWorkflow(ctx, client.StartWorkflowOptions{
		ID: orch.CampaignWorkflowID(run.CampaignID), TaskQueue: c.TaskQueue,
	}, CampaignWorkflowName, run)
	return err
}

func (c *Client) CancelCampaign(ctx context.Context, campaignID uuid.UUID) error {
	err := c.C.CancelWorkflow(ctx, orch.CampaignWorkflowID(campaignID), "")
	var nf *serviceerror.NotFound
	if errors.As(err, &nf) {
		return nil
	}
	return err
}

func (c *Client) CancelTask(ctx context.Context, taskID uuid.UUID) error {
	return c.C.CancelWorkflow(ctx, orch.WorkflowID(taskID), "")
}

func (c *Client) SendTaskMessage(ctx context.Context, taskID uuid.UUID, text string) error {
	return c.C.SignalWorkflow(ctx, orch.WorkflowID(taskID), "", orch.SignalMessage, text)
}

func (c *Client) ResolveConfirm(ctx context.Context, taskID uuid.UUID, confirmID, decision string) error {
	return c.C.SignalWorkflow(ctx, orch.WorkflowID(taskID), "", orch.SignalConfirm, orch.ConfirmSignal{ConfirmID: confirmID, Decision: decision})
}
