package workflows

import (
	"context"
	"errors"
	"time"

	"github.com/google/uuid"
	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/server/internal/orch"
)

const (
	CampaignWorkflowName = "CampaignWorkflow"
	// Pause between batches so a large campaign does not burst APNs or the database.
	campaignBatchGap = time.Second
	// Continue-as-new after this many batches to keep the history small.
	campaignBatchesPerRun = 200
)

type CampaignBatch struct {
	Next uuid.UUID
	Done bool
}

// CampaignWorkflow waits for the send time, then sends to the audience in keyset-paged batches
// until it is exhausted or the campaign is cancelled (workflow cancel or status change).
func CampaignWorkflow(ctx workflow.Context, run orch.CampaignRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 5 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 5},
	})
	if wait := run.SendAt.Sub(workflow.Now(ctx)); wait > 0 {
		if err := workflow.Sleep(ctx, wait); err != nil {
			return err
		}
	}
	var started bool
	if err := workflow.ExecuteActivity(ctx, a.StartCampaign, run.CampaignID).Get(ctx, &started); err != nil || !started {
		return err
	}
	after := run.After
	for i := 0; i < campaignBatchesPerRun; i++ {
		var b CampaignBatch
		if err := workflow.ExecuteActivity(ctx, a.SendCampaignBatch, run.CampaignID, after).Get(ctx, &b); err != nil {
			return err
		}
		if b.Done {
			return workflow.ExecuteActivity(ctx, a.FinishCampaign, run.CampaignID).Get(ctx, nil)
		}
		after = b.Next
		if err := workflow.Sleep(ctx, campaignBatchGap); err != nil {
			return err
		}
	}
	return workflow.NewContinueAsNewError(ctx, CampaignWorkflowName, orch.CampaignRun{CampaignID: run.CampaignID, After: after})
}

var errNoNotify = errors.New("workflows: Activities.Notify is not configured")

func (a *Activities) StartCampaign(ctx context.Context, id uuid.UUID) (bool, error) {
	if a.Notify == nil {
		return false, errNoNotify
	}
	return a.Notify.StartCampaign(ctx, id)
}

func (a *Activities) SendCampaignBatch(ctx context.Context, id, after uuid.UUID) (CampaignBatch, error) {
	if a.Notify == nil {
		return CampaignBatch{}, errNoNotify
	}
	next, done, err := a.Notify.SendCampaignBatch(ctx, id, after)
	return CampaignBatch{Next: next, Done: done}, err
}

func (a *Activities) FinishCampaign(ctx context.Context, id uuid.UUID) error {
	if a.Notify == nil {
		return errNoNotify
	}
	return a.Notify.FinishCampaign(ctx, id)
}
