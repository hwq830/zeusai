package workflows

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
)

// emit mirrors a task event onto the tasks row and then publishes it. State is saved first so a
// client that receives "completed" and re-reads the task sees it finished.
func (a *Activities) emit(ctx context.Context, userID, taskID uuid.UUID, ev events.Event) error {
	ev.TaskID = taskID.String()
	if err := a.saveProgress(ctx, userID, taskID, ev); err != nil {
		return err
	}
	ev, err := a.Bus.Publish(ctx, ev)
	if err != nil {
		return err
	}
	return a.Q.BumpTaskLastEvent(ctx, db.BumpTaskLastEventParams{ID: taskID, UserID: userID, LastEventID: ev.Seq})
}

func (a *Activities) saveProgress(ctx context.Context, userID, taskID uuid.UUID, ev events.Event) error {
	p := db.UpdateTaskProgressParams{ID: taskID, UserID: userID, Status: "running"}
	switch ev.Type {
	case events.TypeStatus:
		p.Status = ev.Status
	case events.TypePlan:
		p.Plan, _ = json.Marshal(ev.Steps)
	case events.TypeStepStarted, events.TypeStepDone:
		p.CurrentStep, _ = json.Marshal(ev.Step)
	case events.TypeConfirmRequired:
		p.Status = "waiting_confirm"
		p.PendingConfirm, _ = json.Marshal(ev.Confirm)
	case events.TypeCompleted:
		p.Status = "done"
	case events.TypeFailed:
		p.Status = "failed"
		p.Error, _ = json.Marshal(ev.Error)
	case events.TypeCancelled:
		p.Status = "cancelled"
	}
	return a.Q.UpdateTaskProgress(ctx, p)
}
