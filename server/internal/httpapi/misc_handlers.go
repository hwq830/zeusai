package httpapi

import (
	"bytes"
	"context"
	"fmt"
	"html"
	"net"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi/gen"
	"github.com/hwq830/zeusai/server/internal/i18n"
	"github.com/hwq830/zeusai/server/internal/report"
	"github.com/hwq830/zeusai/server/internal/share"
	"github.com/hwq830/zeusai/server/internal/task"
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

// ---- reports and shares

func (s *Server) CreateTask(ctx context.Context, req gen.CreateTaskRequestObject) (gen.CreateTaskResponseObject, error) {
	u, err := s.currentUser(ctx)
	if err != nil {
		return nil, err
	}
	t, err := s.Reports.Create(ctx, u, recode[report.Input](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.CreateTask202JSONResponse(recode[gen.Task](task.FromRow(t))), nil
}

func (s *Server) ListReports(ctx context.Context, req gen.ListReportsRequestObject) (gen.ListReportsResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	status := ""
	if p.Status != nil {
		status = string(*p.Status)
	}
	items, err := s.Reports.List(ctx, userID(ctx), report.ListParams{Status: status, Q: deref(p.Q), Limit: lim, Offset: off})
	if err != nil {
		return nil, err
	}
	return gen.ListReports200JSONResponse{Items: recode[[]gen.Report](items), NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) GetReport(ctx context.Context, req gen.GetReportRequestObject) (gen.GetReportResponseObject, error) {
	r, err := s.Reports.Get(ctx, userID(ctx), req.ReportId)
	if err != nil {
		return nil, err
	}
	return gen.GetReport200JSONResponse(recode[gen.Report](r)), nil
}

func (s *Server) DeleteReport(ctx context.Context, req gen.DeleteReportRequestObject) (gen.DeleteReportResponseObject, error) {
	if err := s.Reports.Delete(ctx, userID(ctx), req.ReportId); err != nil {
		return nil, err
	}
	return gen.DeleteReport204Response{}, nil
}

func (s *Server) GetReportHtml(ctx context.Context, req gen.GetReportHtmlRequestObject) (gen.GetReportHtmlResponseObject, error) {
	b, err := s.Reports.HTML(ctx, userID(ctx), req.ReportId)
	if err != nil {
		return nil, err
	}
	return gen.GetReportHtml200TexthtmlResponse{Body: bytes.NewReader(b), ContentLength: int64(len(b))}, nil
}

func (s *Server) GetReportPdf(ctx context.Context, req gen.GetReportPdfRequestObject) (gen.GetReportPdfResponseObject, error) {
	link, err := s.Reports.PDF(ctx, userID(ctx), req.ReportId)
	if err != nil {
		return nil, err
	}
	return gen.GetReportPdf200JSONResponse(recode[gen.FileLink](link)), nil
}

func (s *Server) CreateReportFollowup(ctx context.Context, req gen.CreateReportFollowupRequestObject) (gen.CreateReportFollowupResponseObject, error) {
	t, err := s.Reports.Followup(ctx, userID(ctx), req.ReportId, req.Body.Question)
	if err != nil {
		return nil, err
	}
	return gen.CreateReportFollowup202JSONResponse(recode[gen.Task](task.FromRow(t))), nil
}

func (s *Server) CreateShare(ctx context.Context, req gen.CreateShareRequestObject) (gen.CreateShareResponseObject, error) {
	sh, err := s.Shares.CreateForReport(ctx, userID(ctx), req.ReportId, recode[share.Request](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.CreateShare201JSONResponse(recode[gen.Share](sh)), nil
}

func (s *Server) CreateBriefingShare(ctx context.Context, req gen.CreateBriefingShareRequestObject) (gen.CreateBriefingShareResponseObject, error) {
	sh, err := s.Shares.CreateForBriefing(ctx, userID(ctx), req.BriefingId, recode[share.Request](req.Body))
	if err != nil {
		return nil, err
	}
	return gen.CreateBriefingShare201JSONResponse(recode[gen.Share](sh)), nil
}

func (s *Server) ListShares(ctx context.Context, req gen.ListSharesRequestObject) (gen.ListSharesResponseObject, error) {
	lim, off := page(req.Params.Cursor, req.Params.Limit)
	items, err := s.Shares.List(ctx, userID(ctx), lim, off)
	if err != nil {
		return nil, err
	}
	return gen.ListShares200JSONResponse{Items: recode[[]gen.Share](items), NextCursor: nextCursor(len(items), lim, off)}, nil
}

func (s *Server) RevokeShare(ctx context.Context, req gen.RevokeShareRequestObject) (gen.RevokeShareResponseObject, error) {
	if err := s.Shares.Revoke(ctx, userID(ctx), req.ShareId); err != nil {
		return nil, err
	}
	return gen.RevokeShare204Response{}, nil
}

func (s *Server) RevokeAllShares(ctx context.Context, _ gen.RevokeAllSharesRequestObject) (gen.RevokeAllSharesResponseObject, error) {
	if err := s.Shares.RevokeAll(ctx, userID(ctx)); err != nil {
		return nil, err
	}
	return gen.RevokeAllShares204Response{}, nil
}

// sharePage serves the public H5 page /s/{token}. It is outside the contract on purpose.
func (s *Server) sharePage(w http.ResponseWriter, r *http.Request) {
	b, err := s.Shares.Page(r.Context(), chi.URLParam(r, "token"), share.Visitor{IP: clientIP(r), UserAgent: r.UserAgent()})
	if err != nil {
		e := apperr.As(err)
		msg := i18n.Message(i18n.FromContext(r.Context()), e.Code, e.Message)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(e.Status)
		fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><meta name="viewport" content="width=device-width"><meta name="robots" content="noindex"><p style="font-family:sans-serif;text-align:center;margin-top:40vh">%s</p>`, html.EscapeString(msg))
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// Short, private caching: a revoked or expired link must stop working quickly.
	w.Header().Set("Cache-Control", "private, max-age=60")
	_, _ = w.Write(b)
}

// shareCTA counts a tap on the share page's button and redirects to the landing page.
func (s *Server) shareCTA(w http.ResponseWriter, r *http.Request) {
	to, err := s.Shares.CTA(r.Context(), chi.URLParam(r, "token"))
	if err != nil {
		s.writeError(w, r, err)
		return
	}
	http.Redirect(w, r, to, http.StatusFound)
}

// clientIP is the address middleware.RealIP left in RemoteAddr, without the port.
func clientIP(r *http.Request) string {
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}
