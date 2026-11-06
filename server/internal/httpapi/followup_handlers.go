package httpapi

import (
	"context"

	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
)

// ListReportFollowups returns the report's follow-up history, newest first.
func (s *Server) ListReportFollowups(ctx context.Context, req gen.ListReportFollowupsRequestObject) (gen.ListReportFollowupsResponseObject, error) {
	items, err := s.Reports.ListFollowups(ctx, userID(ctx), req.ReportId)
	if err != nil {
		return nil, err
	}
	out := make([]gen.ReportFollowup, 0, len(items))
	for _, f := range items {
		out = append(out, gen.ReportFollowup{Id: f.ID, Question: f.Question, Answer: f.Answer, AttachedAt: f.AttachedAt, CreatedAt: f.CreatedAt})
	}
	return gen.ListReportFollowups200JSONResponse{Items: out}, nil
}

// AttachReportFollowup adds one follow-up to the report body as an appendix (idempotent).
func (s *Server) AttachReportFollowup(ctx context.Context, req gen.AttachReportFollowupRequestObject) (gen.AttachReportFollowupResponseObject, error) {
	f, err := s.Reports.AttachFollowup(ctx, userID(ctx), req.ReportId, req.FollowupId)
	if err != nil {
		return nil, err
	}
	return gen.AttachReportFollowup200JSONResponse{
		Id: f.ID, Question: f.Question, Answer: f.Answer, AttachedAt: f.AttachedAt, CreatedAt: f.CreatedAt,
	}, nil
}
