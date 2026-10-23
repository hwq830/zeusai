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
	w.RegisterActivityWithOptions(a, activity.RegisterOptions{})
}
