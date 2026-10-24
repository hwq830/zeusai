package workflows

import (
	"go.temporal.io/sdk/activity"
	"go.temporal.io/sdk/worker"
	"go.temporal.io/sdk/workflow"
)

// Register adds every workflow and activity to a worker.
func Register(w worker.Registry, a *Activities) {
	w.RegisterWorkflowWithOptions(BriefingWorkflow, workflow.RegisterOptions{Name: BriefingWorkflowName})
	w.RegisterWorkflowWithOptions(ReportWorkflow, workflow.RegisterOptions{Name: ReportWorkflowName})
	w.RegisterWorkflowWithOptions(FollowupWorkflow, workflow.RegisterOptions{Name: FollowupWorkflowName})
	w.RegisterWorkflowWithOptions(ReportPDFWorkflow, workflow.RegisterOptions{Name: ReportPDFWorkflowName})
	w.RegisterWorkflowWithOptions(CampaignWorkflow, workflow.RegisterOptions{Name: CampaignWorkflowName})
	w.RegisterActivityWithOptions(a, activity.RegisterOptions{})
}
