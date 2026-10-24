package httpapi

import (
	"context"
	"encoding/json"

	"github.com/hwq830/zeusai/server/internal/feedback"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/report"
)

// ---- feedback

func (s *Server) UploadFeedbackAttachment(ctx context.Context, req gen.UploadFeedbackAttachmentRequestObject) (gen.UploadFeedbackAttachmentResponseObject, error) {
	id, err := s.Feedback.Upload(ctx, userID(ctx), req.Body)
	if err != nil {
		return nil, err
	}
	return gen.UploadFeedbackAttachment201JSONResponse{Id: id}, nil
}

func (s *Server) CreateFeedback(ctx context.Context, req gen.CreateFeedbackRequestObject) (gen.CreateFeedbackResponseObject, error) {
	b := req.Body
	in := feedback.Input{
		Type: string(b.Type), Content: b.Content, AttachmentIDs: deref(b.AttachmentIds), Contact: deref(b.Contact),
		ReportID: deref(b.ReportId), TaskID: deref(b.TaskId),
	}
	if b.Client != nil {
		in.Client, _ = json.Marshal(b.Client)
	}
	t, err := s.Feedback.Create(ctx, userID(ctx), in)
	if err != nil {
		return nil, err
	}
	return gen.CreateFeedback201JSONResponse(recode[gen.FeedbackTicket](t)), nil
}

func (s *Server) ListFeedback(ctx context.Context, req gen.ListFeedbackRequestObject) (gen.ListFeedbackResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	items, err := s.Feedback.List(ctx, userID(ctx), lim, off)
	if err != nil {
		return nil, err
	}
	return gen.ListFeedback200JSONResponse{Items: recode[[]gen.FeedbackTicket](items), NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) GetFeedback(ctx context.Context, req gen.GetFeedbackRequestObject) (gen.GetFeedbackResponseObject, error) {
	t, err := s.Feedback.Get(ctx, userID(ctx), req.FeedbackId)
	if err != nil {
		return nil, err
	}
	return gen.GetFeedback200JSONResponse(recode[gen.FeedbackTicket](t)), nil
}

func (s *Server) PutReportFeedback(ctx context.Context, req gen.PutReportFeedbackRequestObject) (gen.PutReportFeedbackResponseObject, error) {
	if err := s.Reports.Rate(ctx, userID(ctx), req.ReportId, recode[report.Feedback](req.Body)); err != nil {
		return nil, err
	}
	return gen.PutReportFeedback204Response{}, nil
}
