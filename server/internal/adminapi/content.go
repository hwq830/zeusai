package adminapi

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/template"
)

// recode converts between structs that share a JSON shape (domain ↔ generated types).
func recode[T any](v any) T {
	var out T
	b, err := json.Marshal(v)
	if err == nil {
		err = json.Unmarshal(b, &out)
	}
	if err != nil {
		panic("adminapi: recode: " + err.Error()) // shapes are fixed by the contract; a mismatch is a bug
	}
	return out
}

// ---------------------------------------------------------------- templates

func toAdminTemplate(r db.Template, subs int) (gen.AdminTemplate, error) {
	t, err := template.FromRow(r)
	if err != nil {
		return gen.AdminTemplate{}, apperr.Internal(err)
	}
	out := recode[gen.AdminTemplate](t.Input)
	out.Id, out.Kind, out.Version = t.ID, gen.AdminTemplateKind(t.Kind), t.Version
	out.OwnerId = idStr(r.OwnerID)
	out.Published, out.SortOrder, out.Slug = r.Published, int(r.SortOrder), r.Slug
	out.AdminManaged, out.SubscriptionCount = r.AdminManaged, subs
	out.CreatedAt, out.UpdatedAt = r.CreatedAt, r.UpdatedAt
	return out, nil
}

type templateColumns struct {
	in                               template.Input
	fields, sections, sources, sched json.RawMessage
}

func templateInput(body gen.AdminTemplateInput) (templateColumns, error) {
	in := recode[template.Input](body)
	if err := in.Validate(); err != nil {
		return templateColumns{}, err
	}
	m := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	c := templateColumns{in: in, fields: m(orEmpty(in.Fields)), sections: m(orEmpty(in.Sections)), sources: m(orEmpty(in.Sources))}
	if in.DefaultSchedule != nil {
		c.sched = m(in.DefaultSchedule)
	}
	return c, nil
}

func orEmpty[T any](s []T) []T {
	if s == nil {
		return []T{}
	}
	return s
}

func (s *Server) ListTemplates(ctx context.Context, req gen.ListTemplatesRequestObject) (gen.ListTemplatesResponseObject, error) {
	p := req.Params
	scope := "official"
	if p.Scope != nil {
		scope = string(*p.Scope)
	}
	lim, off := page(p.Cursor, p.Limit)
	rows, err := s.Q.AdminListTemplates(ctx, db.AdminListTemplatesParams{Scope: scope, Q: strPtr(p.Q), Limit: int32(lim), Offset: int32(off)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminTemplate, 0, len(rows))
	for _, r := range rows {
		t, err := toAdminTemplate(db.Template{
			ID: r.ID, OwnerID: r.OwnerID, Slug: r.Slug, Name: r.Name, Description: r.Description, Category: r.Category,
			Icon: r.Icon, Fields: r.Fields, Sections: r.Sections, Sources: r.Sources, DefaultSchedule: r.DefaultSchedule,
			DefaultLength: r.DefaultLength, RequiredConnectors: r.RequiredConnectors, Version: r.Version,
			Published: r.Published, SortOrder: r.SortOrder, CreatedAt: r.CreatedAt, UpdatedAt: r.UpdatedAt,
			AdminManaged: r.AdminManaged,
		}, int(r.SubscriptionCount))
		if err != nil {
			return nil, err
		}
		items = append(items, t)
	}
	return gen.ListTemplates200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) loadTemplate(ctx context.Context, id string) (db.Template, error) {
	tid, err := parseID(id, "模板")
	if err != nil {
		return db.Template{}, err
	}
	r, err := s.Q.AdminGetTemplate(ctx, tid)
	if err != nil {
		return r, notFound(err, "模板")
	}
	return r, nil
}

func (s *Server) GetTemplate(ctx context.Context, req gen.GetTemplateRequestObject) (gen.GetTemplateResponseObject, error) {
	r, err := s.loadTemplate(ctx, req.TemplateId)
	if err != nil {
		return nil, err
	}
	t, err := toAdminTemplate(r, 0)
	if err != nil {
		return nil, err
	}
	return gen.GetTemplate200JSONResponse(t), nil
}

func (s *Server) CreateTemplate(ctx context.Context, req gen.CreateTemplateRequestObject) (gen.CreateTemplateResponseObject, error) {
	if _, err := require(ctx, PermTemplates); err != nil {
		return nil, err
	}
	c, err := templateInput(*req.Body)
	if err != nil {
		return nil, err
	}
	r, err := s.Q.AdminCreateOfficialTemplate(ctx, db.AdminCreateOfficialTemplateParams{
		Slug: strPtr(req.Body.Slug), Name: c.in.Name, Description: c.in.Description, Category: c.in.Category, Icon: c.in.Icon,
		Fields: c.fields, Sections: c.sections, Sources: c.sources, DefaultSchedule: c.sched, DefaultLength: c.in.DefaultLength,
		RequiredConnectors: orEmpty(c.in.RequiredConnectors), Published: req.Body.Published, SortOrder: int32(req.Body.SortOrder),
	})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, apperr.Conflict("slug 已被使用")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "template.create", "template", r.ID.String(), map[string]any{"name": r.Name})
	t, err := toAdminTemplate(r, 0)
	if err != nil {
		return nil, err
	}
	return gen.CreateTemplate201JSONResponse(t), nil
}

func (s *Server) UpdateTemplate(ctx context.Context, req gen.UpdateTemplateRequestObject) (gen.UpdateTemplateResponseObject, error) {
	if _, err := require(ctx, PermTemplates); err != nil {
		return nil, err
	}
	cur, err := s.loadTemplate(ctx, req.TemplateId)
	if err != nil {
		return nil, err
	}
	c, err := templateInput(*req.Body)
	if err != nil {
		return nil, err
	}
	r, err := s.Q.AdminUpdateTemplate(ctx, db.AdminUpdateTemplateParams{
		ID: cur.ID, Name: c.in.Name, Description: c.in.Description, Category: c.in.Category, Icon: c.in.Icon,
		Fields: c.fields, Sections: c.sections, Sources: c.sources, DefaultSchedule: c.sched, DefaultLength: c.in.DefaultLength,
		RequiredConnectors: orEmpty(c.in.RequiredConnectors), Published: req.Body.Published, SortOrder: int32(req.Body.SortOrder),
	})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "template.update", "template", r.ID.String(), map[string]any{"name": r.Name, "version": r.Version, "published": r.Published})
	t, err := toAdminTemplate(r, 0)
	if err != nil {
		return nil, err
	}
	return gen.UpdateTemplate200JSONResponse(t), nil
}

func (s *Server) DeleteTemplate(ctx context.Context, req gen.DeleteTemplateRequestObject) (gen.DeleteTemplateResponseObject, error) {
	r, err := s.loadTemplate(ctx, req.TemplateId)
	if err != nil {
		return nil, err
	}
	// Official templates are content operations; removing a user's template is moderation.
	perm := PermTemplates
	if r.OwnerID != nil {
		perm = PermModeration
	}
	if _, err := require(ctx, perm); err != nil {
		return nil, err
	}
	if _, err := s.Q.AdminDeleteTemplate(ctx, r.ID); err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "template.delete", "template", r.ID.String(), map[string]any{"name": r.Name, "ownerId": idStr(r.OwnerID)})
	return gen.DeleteTemplate204Response{}, nil
}

// ---------------------------------------------------------------- connectors

func toAdminConnector(c db.Connector, enabled, authorized int) gen.AdminConnector {
	tools := []gen.ConnectorTool{}
	_ = json.Unmarshal(c.Tools, &tools)
	icon, ver := c.IconUrl, c.McpVersion
	return gen.AdminConnector{
		Id: c.ID, Name: c.Name, Description: c.Description, IconUrl: &icon, Kind: c.Kind, Category: c.Category,
		Transport: c.Transport, McpVersion: &ver, RequiresAuth: c.RequiresAuth, DefaultOn: c.DefaultOn,
		Available: c.Available, SortOrder: int(c.SortOrder), Tools: tools, AdminManaged: c.AdminManaged,
		EnabledUsers: enabled, AuthorizedUsers: authorized,
	}
}

func (s *Server) ListConnectors(ctx context.Context, _ gen.ListConnectorsRequestObject) (gen.ListConnectorsResponseObject, error) {
	rows, err := s.Q.AdminListConnectors(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make(gen.ListConnectors200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, toAdminConnector(db.Connector{
			ID: r.ID, Name: r.Name, Description: r.Description, IconUrl: r.IconUrl, Kind: r.Kind, Category: r.Category,
			RequiresAuth: r.RequiresAuth, DefaultOn: r.DefaultOn, Tools: r.Tools, Transport: r.Transport,
			McpVersion: r.McpVersion, Available: r.Available, SortOrder: r.SortOrder, AdminManaged: r.AdminManaged,
		}, int(r.EnabledUsers), int(r.AuthorizedUsers)))
	}
	return out, nil
}

func (s *Server) UpdateConnector(ctx context.Context, req gen.UpdateConnectorRequestObject) (gen.UpdateConnectorResponseObject, error) {
	if _, err := require(ctx, PermConnectors); err != nil {
		return nil, err
	}
	b := req.Body
	if b.Name != nil && strings.TrimSpace(*b.Name) == "" {
		return nil, apperr.Invalid("名称不能为空")
	}
	c, err := s.Q.AdminUpdateConnector(ctx, db.AdminUpdateConnectorParams{
		ID: req.ConnectorId, Name: b.Name, Description: b.Description, Category: b.Category,
		Available: b.Available, DefaultOn: b.DefaultOn, SortOrder: int32Ptr(b.SortOrder),
	})
	if err != nil {
		return nil, notFound(err, "连接器")
	}
	s.audit(ctx, "connector.update", "connector", c.ID, b)
	// Usage counts are only shown in the list; the console refetches it after an edit.
	return gen.UpdateConnector200JSONResponse(toAdminConnector(c, 0, 0)), nil
}

// ---------------------------------------------------------------- moderation

func (s *Server) ListModerationTerms(ctx context.Context, _ gen.ListModerationTermsRequestObject) (gen.ListModerationTermsResponseObject, error) {
	rows, err := s.Q.AdminListModerationTerms(ctx)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make(gen.ListModerationTerms200JSONResponse, 0, len(rows))
	for _, r := range rows {
		out = append(out, gen.ModerationTerm{Id: r.ID, Term: r.Term, Note: r.Note, CreatedAt: r.CreatedAt})
	}
	return out, nil
}

func (s *Server) CreateModerationTerm(ctx context.Context, req gen.CreateModerationTermRequestObject) (gen.CreateModerationTermResponseObject, error) {
	a, err := require(ctx, PermModeration)
	if err != nil {
		return nil, err
	}
	term := strings.TrimSpace(req.Body.Term)
	if term == "" || len([]rune(term)) > 50 {
		return nil, apperr.Invalid("屏蔽词不能为空且不超过 50 字")
	}
	r, err := s.Q.CreateModerationTerm(ctx, db.CreateModerationTermParams{Term: term, Note: strings.TrimSpace(deref(req.Body.Note)), CreatedBy: &a.ID})
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return nil, apperr.Conflict("屏蔽词已存在")
	}
	if err != nil {
		return nil, apperr.Internal(err)
	}
	s.audit(ctx, "moderation.add_term", "moderation_term", term, nil)
	return gen.CreateModerationTerm201JSONResponse{Id: r.ID, Term: r.Term, Note: r.Note, CreatedAt: r.CreatedAt}, nil
}

func (s *Server) DeleteModerationTerm(ctx context.Context, req gen.DeleteModerationTermRequestObject) (gen.DeleteModerationTermResponseObject, error) {
	if _, err := require(ctx, PermModeration); err != nil {
		return nil, err
	}
	n, err := s.Q.DeleteModerationTerm(ctx, req.TermId)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if n == 0 {
		return nil, apperr.NotFound("屏蔽词不存在")
	}
	s.audit(ctx, "moderation.delete_term", "moderation_term", "", map[string]any{"id": req.TermId})
	return gen.DeleteModerationTerm204Response{}, nil
}

func (s *Server) ListModerationEvents(ctx context.Context, req gen.ListModerationEventsRequestObject) (gen.ListModerationEventsResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	params := db.ListModerationEventsParams{Limit: int32(lim), Offset: int32(off)}
	if p.Kind != nil {
		k := string(*p.Kind)
		params.Kind = &k
	}
	if p.UserId != nil {
		id, err := parseID(*p.UserId, "用户")
		if err != nil {
			return nil, err
		}
		params.UserID = &id
	}
	rows, err := s.Q.ListModerationEvents(ctx, params)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.ModerationEvent, 0, len(rows))
	for _, r := range rows {
		nick, phone := deref(r.Nickname), maskPhone(r.Phone)
		items = append(items, gen.ModerationEvent{
			Id: r.ID, UserId: idStr(r.UserID), Nickname: &nick, PhoneMasked: &phone, Kind: gen.ModerationEventKind(r.Kind),
			Reason: r.Reason, Excerpt: r.Excerpt, CreatedAt: r.CreatedAt,
		})
	}
	return gen.ListModerationEvents200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) ListShares(ctx context.Context, req gen.ListSharesRequestObject) (gen.ListSharesResponseObject, error) {
	p := req.Params
	lim, off := page(p.Cursor, p.Limit)
	active := p.Active == nil || *p.Active
	rows, err := s.Q.AdminListShares(ctx, db.AdminListSharesParams{ActiveOnly: active, Limit: int32(lim), Offset: int32(off)})
	if err != nil {
		return nil, apperr.Internal(err)
	}
	items := make([]gen.AdminShare, 0, len(rows))
	for _, r := range rows {
		url := strings.TrimRight(s.PublicBaseURL, "/") + "/s/" + r.Token
		items = append(items, gen.AdminShare{
			Id: r.ID.String(), ReportId: r.ReportID.String(), ReportTitle: r.ReportTitle, UserId: r.UserID.String(),
			Nickname: r.Nickname, PhoneMasked: maskPhone(r.Phone), Scope: r.Scope, ViewCount: int(r.ViewCount),
			Url: &url, CreatedAt: r.CreatedAt, RevokedAt: r.RevokedAt,
		})
	}
	return gen.ListShares200JSONResponse{Items: items, NextCursor: nextCursor(len(rows), lim, off)}, nil
}

func (s *Server) RevokeShare(ctx context.Context, req gen.RevokeShareRequestObject) (gen.RevokeShareResponseObject, error) {
	if _, err := require(ctx, PermModeration); err != nil {
		return nil, err
	}
	id, err := parseID(req.ShareId, "分享")
	if err != nil {
		return nil, err
	}
	n, err := s.Q.AdminRevokeShare(ctx, id)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	if n == 0 {
		return nil, apperr.NotFound("分享不存在或已撤销")
	}
	s.audit(ctx, "share.revoke", "share", id.String(), nil)
	return gen.RevokeShare204Response{}, nil
}

func (s *Server) GetReport(ctx context.Context, req gen.GetReportRequestObject) (gen.GetReportResponseObject, error) {
	id, err := parseID(req.ReportId, "报告")
	if err != nil {
		return nil, err
	}
	r, err := s.Q.AdminGetReport(ctx, id)
	if err != nil {
		return nil, notFound(err, "报告")
	}
	return gen.GetReport200JSONResponse{
		Id: r.ID.String(), UserId: r.UserID.String(), Title: r.Title, Topic: r.Topic, Status: r.Status,
		Summary: r.Summary, Markdown: r.Markdown, SourceCount: int(r.SourceCount), CreatedAt: r.CreatedAt,
	}, nil
}
