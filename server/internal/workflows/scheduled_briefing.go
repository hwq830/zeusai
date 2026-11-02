package workflows

import (
	"time"

	"go.temporal.io/api/enums/v1"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/schedule"
)

const ScheduledBriefingWorkflowName = "ScheduledBriefingWorkflow"

func briefingDelivery(s schedule.Schedule, now time.Time) time.Time {
	local := now.In(s.Location())
	h, m := s.Clock()
	return time.Date(local.Year(), local.Month(), local.Day(), h, m, 0, 0, local.Location())
}

// A newly saved schedule cannot catch up calendar ticks from before its creation.
// Trigger within the entire generation window, including jitter that may not yet
// have fired. The daily child workflow below arbitrates concurrent triggers.
func needsBriefingCatchup(s schedule.Schedule, now time.Time) bool {
	delivery := briefingDelivery(s, now)
	return s.Matches(delivery) && !now.Before(delivery.Add(-LeadTime)) && !now.After(delivery)
}

// ScheduledBriefingWorkflow funnels both calendar and immediate triggers through
// a stable daily child ID. Completed and running issues are both deduplicated;
// manual regeneration continues to use independent task workflow IDs.
// This separate entry point leaves existing BriefingWorkflow histories replayable.
func ScheduledBriefingWorkflow(ctx workflow.Context, run orch.BriefingRun, s schedule.Schedule) error {
	delivery := briefingDelivery(s, workflow.Now(ctx))
	ctx = workflow.WithChildOptions(ctx, workflow.ChildWorkflowOptions{
		WorkflowID:            "scheduled-briefing-" + run.SubscriptionID.String() + "-" + delivery.Format("2006-01-02"),
		WorkflowIDReusePolicy: enums.WORKFLOW_ID_REUSE_POLICY_REJECT_DUPLICATE,
	})
	run.Scheduled = true
	err := workflow.ExecuteChildWorkflow(ctx, BriefingWorkflowName, run).Get(ctx, nil)
	if temporal.IsWorkflowExecutionAlreadyStartedError(err) {
		return nil
	}
	return err
}
