package workflows

import (
	"context"
	"errors"
	"time"

	"go.temporal.io/sdk/temporal"
	"go.temporal.io/sdk/workflow"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/report"
)

const ReportPDFWorkflowName = "ReportPDFWorkflow"

func ReportPDFWorkflow(ctx workflow.Context, run orch.ReportPDFRun) error {
	var a *Activities
	ctx = workflow.WithActivityOptions(ctx, workflow.ActivityOptions{
		StartToCloseTimeout: 2 * time.Minute,
		RetryPolicy:         &temporal.RetryPolicy{MaximumAttempts: 3},
	})
	err := workflow.ExecuteActivity(ctx, a.PrintReportPDF, run).Get(ctx, nil)
	if err != nil {
		_ = workflow.ExecuteActivity(ctx, a.SetReportPDFStatus, run, "failed").Get(ctx, nil)
	}
	return err
}

func (a *Activities) PrintReportPDF(ctx context.Context, run orch.ReportPDFRun) error {
	if a.PDF == nil || a.Blob == nil {
		return temporal.NewNonRetryableApplicationError("PDF export is not configured", "config", nil)
	}
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: run.ReportID, UserID: run.UserID})
	if err != nil {
		return err
	}
	if r.Status != "done" {
		return temporal.NewNonRetryableApplicationError("report not finished", "state", nil)
	}
	if report.PDFKey(r) != run.Key {
		// A follow-up changed the report since the print was requested; the next request
		// starts a fresh print with the new key.
		return temporal.NewNonRetryableApplicationError("report changed", "stale", errors.New(run.Key))
	}
	html, err := report.PrintHTML(r)
	if err != nil {
		return err
	}
	b, err := a.PDF.Render(ctx, html)
	if err != nil {
		return err
	}
	if err := a.Blob.Put(ctx, run.Key, b, "application/pdf"); err != nil {
		return err
	}
	if err := a.Q.SetReportPDF(ctx, db.SetReportPDFParams{ID: run.ReportID, UserID: run.UserID, PdfStatus: "ready", PdfKey: &run.Key}); err != nil {
		return err
	}
	// The file printed before the last follow-up is no longer reachable.
	if r.PdfKey != nil && *r.PdfKey != run.Key {
		_ = a.Blob.Delete(ctx, *r.PdfKey)
	}
	return nil
}

func (a *Activities) SetReportPDFStatus(ctx context.Context, run orch.ReportPDFRun, status string) error {
	r, err := a.Q.GetReport(ctx, db.GetReportParams{ID: run.ReportID, UserID: run.UserID})
	if err != nil {
		return nil // report deleted meanwhile
	}
	// Keep the key of the last good file so it can still be cleaned up later.
	return a.Q.SetReportPDF(ctx, db.SetReportPDFParams{ID: run.ReportID, UserID: run.UserID, PdfStatus: status, PdfKey: r.PdfKey})
}
