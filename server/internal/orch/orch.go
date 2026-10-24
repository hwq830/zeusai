// Package orch is the contract between the api process and the orchestrator (Temporal).
// The api only starts, signals and schedules workflows through Client.
package orch

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/schedule"
)

// BriefingRun starts one briefing issue. Scheduled runs come from a Temporal Schedule and
// carry no TaskID (the workflow creates it); manual runs ("立即生成") are pre-created by the api.
type BriefingRun struct {
	UserID         uuid.UUID
	SubscriptionID uuid.UUID
	TaskID         uuid.UUID
	Scheduled      bool
}

type ReportRun struct {
	UserID   uuid.UUID
	TaskID   uuid.UUID
	ReportID uuid.UUID
}

// FollowupRun answers one question about a finished report.
type FollowupRun struct {
	UserID   uuid.UUID
	TaskID   uuid.UUID
	ReportID uuid.UUID
	Question string
}

// ReportPDFRun prints a finished report to PDF and stores it under Key.
type ReportPDFRun struct {
	UserID   uuid.UUID
	ReportID uuid.UUID
	Key      string
}

// CampaignRun sends an approved operator campaign at SendAt (zero: now). After is the last user
// already handled, carried across continue-as-new.
type CampaignRun struct {
	CampaignID uuid.UUID
	SendAt     time.Time
	After      uuid.UUID
}

type Client interface {
	UpsertBriefingSchedule(ctx context.Context, userID, subscriptionID uuid.UUID, s schedule.Schedule, paused bool) error
	DeleteBriefingSchedule(ctx context.Context, subscriptionID uuid.UUID) error
	StartBriefing(ctx context.Context, run BriefingRun) error
	StartReport(ctx context.Context, run ReportRun) error
	StartFollowup(ctx context.Context, run FollowupRun) error
	// StartReportPDF is idempotent while a print for the same report is running.
	StartReportPDF(ctx context.Context, run ReportPDFRun) error
	CancelTask(ctx context.Context, taskID uuid.UUID) error
	SendTaskMessage(ctx context.Context, taskID uuid.UUID, text string) error
	ResolveConfirm(ctx context.Context, taskID uuid.UUID, confirmID, decision string) error
	// StartCampaign is idempotent while the campaign's workflow is running.
	StartCampaign(ctx context.Context, run CampaignRun) error
	// CancelCampaign stops a scheduled or sending campaign; a finished one is a no-op.
	CancelCampaign(ctx context.Context, campaignID uuid.UUID) error
}

// WorkflowID is the Temporal workflow id of a task.
func WorkflowID(taskID uuid.UUID) string { return "task-" + taskID.String() }

// CampaignWorkflowID is the Temporal workflow id of an operator campaign.
func CampaignWorkflowID(campaignID uuid.UUID) string { return "campaign-" + campaignID.String() }

// ScheduleID is the Temporal schedule id of a briefing subscription.
func ScheduleID(subscriptionID uuid.UUID) string { return "briefing-" + subscriptionID.String() }

// Signal names.
const (
	SignalCancel  = "cancel"
	SignalMessage = "message"
	SignalConfirm = "confirm"
)

type ConfirmSignal struct {
	ConfirmID string
	Decision  string
}
