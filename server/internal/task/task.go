// Package task exposes task state to the app and forwards cancel / message / confirm to the
// running workflow.
package task

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/orch"
)

type Task struct {
	ID             string          `json:"id"`
	Kind           string          `json:"kind"`
	Status         string          `json:"status"`
	Title          string          `json:"title"`
	Subtitle       string          `json:"subtitle,omitempty"`
	ReportID       *string         `json:"reportId,omitempty"`
	BriefingID     *string         `json:"briefingId,omitempty"`
	SubscriptionID *string         `json:"subscriptionId,omitempty"`
	Plan           []events.Step   `json:"plan,omitempty"`
	CurrentStep    *events.Step    `json:"currentStep,omitempty"`
	PendingConfirm *events.Confirm `json:"pendingConfirm,omitempty"`
	LastEventID    int64           `json:"lastEventId"`
	Error          *events.Error   `json:"error,omitempty"`
	CreatedAt      time.Time       `json:"createdAt"`
	StartedAt      *time.Time      `json:"startedAt,omitempty"`
	FinishedAt     *time.Time      `json:"finishedAt,omitempty"`
}

func idStr(u *uuid.UUID) *string {
	if u == nil {
		return nil
	}
	s := u.String()
	return &s
}

func decode[T any](raw json.RawMessage) *T {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var v T
	if json.Unmarshal(raw, &v) != nil {
		return nil
	}
	return &v
}

func FromRow(r db.Task) Task {
	t := Task{
		ID: r.ID.String(), Kind: r.Kind, Status: r.Status, Title: r.Title, Subtitle: r.Subtitle,
		ReportID: idStr(r.ReportID), BriefingID: idStr(r.BriefingID), SubscriptionID: idStr(r.SubscriptionID),
		CurrentStep: decode[events.Step](r.CurrentStep), PendingConfirm: decode[events.Confirm](r.PendingConfirm),
		LastEventID: r.LastEventID, Error: decode[events.Error](r.Error),
		CreatedAt: r.CreatedAt, StartedAt: r.StartedAt, FinishedAt: r.FinishedAt,
	}
	if p := decode[[]events.Step](r.Plan); p != nil {
		t.Plan = *p
	}
	return t
}

// Terminal reports whether the task will produce no more events.
func Terminal(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled"
}

type Service struct {
	Q    *db.Queries
	Orch orch.Client
}

func (s *Service) Get(ctx context.Context, userID uuid.UUID, id string) (db.Task, error) {
	tid, err := uuid.Parse(id)
	if err != nil {
		return db.Task{}, apperr.NotFound("任务不存在")
	}
	t, err := s.Q.GetTask(ctx, db.GetTaskParams{ID: tid, UserID: userID})
	if errors.Is(err, pgx.ErrNoRows) {
		return t, apperr.NotFound("任务不存在")
	}
	if err != nil {
		return t, apperr.Internal(err)
	}
	return t, nil
}

func (s *Service) List(ctx context.Context, userID uuid.UUID, activeOnly bool, limit, offset int) ([]db.Task, error) {
	rows, err := s.Q.ListTasks(ctx, db.ListTasksParams{UserID: userID, ActiveOnly: activeOnly, Limit: int32(limit), Offset: int32(offset)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return rows, nil
}

func (s *Service) running(ctx context.Context, userID uuid.UUID, id string) (db.Task, error) {
	t, err := s.Get(ctx, userID, id)
	if err != nil {
		return t, err
	}
	if Terminal(t.Status) {
		return t, apperr.Conflict("任务已结束")
	}
	return t, nil
}

func (s *Service) Cancel(ctx context.Context, userID uuid.UUID, id string) (db.Task, error) {
	t, err := s.running(ctx, userID, id)
	if err != nil {
		return t, err
	}
	if err := s.Orch.CancelTask(ctx, t.ID); err != nil {
		return t, apperr.Unavailable("中止失败，请稍后再试").WithCause(err)
	}
	return t, nil
}

func (s *Service) SendMessage(ctx context.Context, userID uuid.UUID, id, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return apperr.Invalid("内容不能为空")
	}
	t, err := s.running(ctx, userID, id)
	if err != nil {
		return err
	}
	if err := s.Orch.SendTaskMessage(ctx, t.ID, text); err != nil {
		return apperr.Unavailable("发送失败，请稍后再试").WithCause(err)
	}
	return nil
}

func (s *Service) ResolveConfirm(ctx context.Context, userID uuid.UUID, id, confirmID, decision string) error {
	if decision != "approve" && decision != "reject" {
		return apperr.Invalid("decision 应为 approve / reject")
	}
	t, err := s.running(ctx, userID, id)
	if err != nil {
		return err
	}
	pc := decode[events.Confirm](t.PendingConfirm)
	if pc == nil || pc.ConfirmID != confirmID {
		return apperr.Conflict("这个确认已失效")
	}
	if err := s.Orch.ResolveConfirm(ctx, t.ID, confirmID, decision); err != nil {
		return apperr.Unavailable("提交失败，请稍后再试").WithCause(err)
	}
	return nil
}
