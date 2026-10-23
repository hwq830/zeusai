package httpapi

import (
	"context"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
)

// ---- connectors

func (s *Server) ListConnectors(ctx context.Context, _ gen.ListConnectorsRequestObject) (gen.ListConnectorsResponseObject, error) {
	cs, err := s.Connectors.List(ctx, userID(ctx))
	if err != nil {
		return nil, err
	}
	return gen.ListConnectors200JSONResponse{Items: recode[[]gen.Connector](cs)}, nil
}

func (s *Server) UpdateConnector(ctx context.Context, req gen.UpdateConnectorRequestObject) (gen.UpdateConnectorResponseObject, error) {
	c, err := s.Connectors.SetEnabled(ctx, userID(ctx), req.ConnectorId, req.Body.Enabled)
	if err != nil {
		return nil, err
	}
	return gen.UpdateConnector200JSONResponse(recode[gen.Connector](c)), nil
}

// AuthorizeConnector needs each provider's OAuth app (client id / secret), which are not set up yet.
func (s *Server) AuthorizeConnector(ctx context.Context, req gen.AuthorizeConnectorRequestObject) (gen.AuthorizeConnectorResponseObject, error) {
	if _, err := s.Connectors.Get(ctx, userID(ctx), req.ConnectorId); err != nil {
		return nil, err
	}
	return nil, apperr.Unavailable("该连接器的授权即将开放")
}

func (s *Server) RevokeConnector(ctx context.Context, req gen.RevokeConnectorRequestObject) (gen.RevokeConnectorResponseObject, error) {
	if err := s.Connectors.Revoke(ctx, userID(ctx), req.ConnectorId); err != nil {
		return nil, err
	}
	return gen.RevokeConnector204Response{}, nil
}

// ---- memories

func (s *Server) ListMemories(ctx context.Context, req gen.ListMemoriesRequestObject) (gen.ListMemoriesResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	var kind *string
	if req.Params.Kind != nil {
		k := string(*req.Params.Kind)
		kind = &k
	}
	rows, err := s.Q.ListMemories(ctx, db.ListMemoriesParams{UserID: userID(ctx), Kind: kind, Limit: int32(lim), Offset: int32(off)})
	if err != nil {
		return nil, err
	}
	items := make([]gen.Memory, 0, len(rows))
	for _, r := range rows {
		m := gen.Memory{Id: r.ID.String(), Kind: gen.MemoryKind(r.Kind), Content: r.Content, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt}
		if r.SourceTaskID != nil {
			id := r.SourceTaskID.String()
			m.SourceTaskId = &id
		}
		items = append(items, m)
	}
	return gen.ListMemories200JSONResponse{Items: items, NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) DeleteMemory(ctx context.Context, req gen.DeleteMemoryRequestObject) (gen.DeleteMemoryResponseObject, error) {
	id, err := uuid.Parse(req.MemoryId)
	if err != nil {
		return nil, apperr.NotFound("记忆不存在")
	}
	n, err := s.Q.DeleteMemory(ctx, db.DeleteMemoryParams{ID: id, UserID: userID(ctx)})
	if err != nil {
		return nil, err
	}
	if n == 0 {
		return nil, apperr.NotFound("记忆不存在")
	}
	return gen.DeleteMemory204Response{}, nil
}

func (s *Server) DeleteAllMemories(ctx context.Context, _ gen.DeleteAllMemoriesRequestObject) (gen.DeleteAllMemoriesResponseObject, error) {
	if err := s.Q.DeleteAllMemories(ctx, userID(ctx)); err != nil {
		return nil, err
	}
	return gen.DeleteAllMemories204Response{}, nil
}

// ---- reports and shares (stage 3)

func (s *Server) ListReports(context.Context, gen.ListReportsRequestObject) (gen.ListReportsResponseObject, error) {
	return gen.ListReports200JSONResponse{Items: []gen.Report{}}, nil
}
func (s *Server) GetReport(context.Context, gen.GetReportRequestObject) (gen.GetReportResponseObject, error) {
	return nil, apperr.NotFound("报告不存在")
}
func (s *Server) DeleteReport(context.Context, gen.DeleteReportRequestObject) (gen.DeleteReportResponseObject, error) {
	return nil, apperr.NotFound("报告不存在")
}
func (s *Server) GetReportHtml(context.Context, gen.GetReportHtmlRequestObject) (gen.GetReportHtmlResponseObject, error) {
	return nil, apperr.NotFound("报告不存在")
}
func (s *Server) GetReportPdf(context.Context, gen.GetReportPdfRequestObject) (gen.GetReportPdfResponseObject, error) {
	return nil, apperr.NotFound("报告不存在")
}
func (s *Server) CreateReportFollowup(context.Context, gen.CreateReportFollowupRequestObject) (gen.CreateReportFollowupResponseObject, error) {
	return nil, errNotImplemented
}
func (s *Server) CreateShare(context.Context, gen.CreateShareRequestObject) (gen.CreateShareResponseObject, error) {
	return nil, errNotImplemented
}
func (s *Server) RevokeShare(context.Context, gen.RevokeShareRequestObject) (gen.RevokeShareResponseObject, error) {
	return nil, apperr.NotFound("分享不存在")
}
func (s *Server) CreateTask(context.Context, gen.CreateTaskRequestObject) (gen.CreateTaskResponseObject, error) {
	return nil, errNotImplemented
}
