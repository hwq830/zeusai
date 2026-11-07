package adminapi

import (
	"context"
	"encoding/json"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi/gen"
	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/pagetemplate"
	"github.com/hwq830/zeusai/server/internal/report"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// ListPageTemplates is the console's page-template index (read-only).
func (s *Server) ListPageTemplates(ctx context.Context, req gen.ListPageTemplatesRequestObject) (gen.ListPageTemplatesResponseObject, error) {
	items, err := s.PageTemplates.List(ctx, req.Params.Kind)
	if err != nil {
		return nil, err
	}
	out := make([]gen.PageTemplate, 0, len(items))
	for _, t := range items {
		out = append(out, gen.PageTemplate{
			Id: t.ID.String(), Kind: t.Kind, Name: t.Name, Description: t.Description, CreatedAt: t.CreatedAt,
			ArchivedAt: t.ArchivedAt, ActiveVersionId: idStr(t.ActiveVersionID), ActiveVersion: intPtr(t.ActiveVersion),
			ActivePublishedAt: t.ActivePublishedAt,
		})
	}
	return gen.ListPageTemplates200JSONResponse{Items: out}, nil
}

// ListPageTemplateVersions lists a template's versions, newest first.
func (s *Server) ListPageTemplateVersions(ctx context.Context, req gen.ListPageTemplateVersionsRequestObject) (gen.ListPageTemplateVersionsResponseObject, error) {
	id, err := parseID(req.PageTemplateId, "页面模板")
	if err != nil {
		return nil, err
	}
	rows, err := s.PageTemplates.Versions(ctx, id)
	if err != nil {
		return nil, err
	}
	out := make([]gen.PageTemplateVersion, 0, len(rows))
	for _, row := range rows {
		out = append(out, toPageTemplateVersion(row))
	}
	return gen.ListPageTemplateVersions200JSONResponse{Items: out}, nil
}

// UploadPageTemplateVersion stores a new draft version.
func (s *Server) UploadPageTemplateVersion(ctx context.Context, req gen.UploadPageTemplateVersionRequestObject) (gen.UploadPageTemplateVersionResponseObject, error) {
	a, err := require(ctx, PermPageTemplates)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.PageTemplateId, "页面模板")
	if err != nil {
		return nil, err
	}
	if req.Body == nil || len(req.Body.Files) == 0 {
		return nil, apperr.Invalid("请上传模板文件")
	}
	contract := ""
	if req.Body.ContractVersion != nil {
		contract = *req.Body.ContractVersion
	}
	v, err := s.PageTemplates.Upload(ctx, id, a.ID, req.Body.Files, contract)
	if err != nil {
		return nil, err
	}
	return gen.UploadPageTemplateVersion201JSONResponse(toPageTemplateVersion(v)), nil
}

// ValidatePageTemplateVersion runs the publish gate and stores its report.
func (s *Server) ValidatePageTemplateVersion(ctx context.Context, req gen.ValidatePageTemplateVersionRequestObject) (gen.ValidatePageTemplateVersionResponseObject, error) {
	if _, err := require(ctx, PermPageTemplates); err != nil {
		return nil, err
	}
	id, err := parseID(req.VersionId, "模板版本")
	if err != nil {
		return nil, err
	}
	v, err := s.PageTemplates.Validate(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.ValidatePageTemplateVersion200JSONResponse(toPageTemplateVersion(v)), nil
}

// PublishPageTemplateVersion moves the kind's binding to a validated version.
func (s *Server) PublishPageTemplateVersion(ctx context.Context, req gen.PublishPageTemplateVersionRequestObject) (gen.PublishPageTemplateVersionResponseObject, error) {
	a, err := require(ctx, PermPageTemplates)
	if err != nil {
		return nil, err
	}
	id, err := parseID(req.VersionId, "模板版本")
	if err != nil {
		return nil, err
	}
	if err := s.PageTemplates.Publish(ctx, id, a.ID); err != nil {
		return nil, err
	}
	v, err := s.PageTemplates.Version(ctx, id)
	if err != nil {
		return nil, err
	}
	return gen.PublishPageTemplateVersion200JSONResponse(toPageTemplateVersion(v)), nil
}

// RollbackPageTemplate points a kind back at its previously published version.
func (s *Server) RollbackPageTemplate(ctx context.Context, req gen.RollbackPageTemplateRequestObject) (gen.RollbackPageTemplateResponseObject, error) {
	a, err := require(ctx, PermPageTemplates)
	if err != nil {
		return nil, err
	}
	v, err := s.PageTemplates.Rollback(ctx, req.Kind, a.ID)
	if err != nil {
		return nil, err
	}
	return gen.RollbackPageTemplate200JSONResponse(toPageTemplateVersion(v)), nil
}

// PreviewPageTemplateVersion renders one version with the fixture or a real report.
func (s *Server) PreviewPageTemplateVersion(ctx context.Context, req gen.PreviewPageTemplateVersionRequestObject) (gen.PreviewPageTemplateVersionResponseObject, error) {
	id, err := parseID(req.VersionId, "模板版本")
	if err != nil {
		return nil, err
	}
	data := pagetemplate.FixtureData()
	if req.Params.ReportId != nil && *req.Params.ReportId != "" {
		reportID, err := uuid.Parse(*req.Params.ReportId)
		if err != nil {
			return nil, apperr.Invalid("reportId 格式不正确")
		}
		row, err := s.Q.AdminGetReport(ctx, reportID)
		if err != nil {
			return nil, notFound(err, "报告")
		}
		data = report.TemplateData(row, true)
	}
	html, err := s.PageTemplates.Preview(ctx, id, data)
	if err != nil {
		return nil, err
	}
	return gen.PreviewPageTemplateVersion200JSONResponse{Html: string(html)}, nil
}

func toPageTemplateVersion(v db.PageTemplateVersion) gen.PageTemplateVersion {
	out := gen.PageTemplateVersion{
		Id: v.ID.String(), TemplateId: v.TemplateID.String(), Version: int(v.Version),
		ContractVersion: v.ContractVersion, CreatedAt: v.CreatedAt, PublishedAt: v.PublishedAt, CreatedBy: idStr(v.CreatedBy),
		Status: gen.PageTemplateVersionStatus(v.Status),
	}
	if len(v.Validation) > 0 {
		var validation gen.PageTemplateValidation
		if json.Unmarshal(v.Validation, &validation) == nil {
			out.Validation = &validation
		}
	}
	var files map[string]string
	if json.Unmarshal(v.Files, &files) == nil {
		list := make([]gen.PageTemplateFile, 0, len(files))
		for name, content := range files {
			list = append(list, gen.PageTemplateFile{Name: name, Bytes: len(content)})
		}
		out.Files = &list
	}
	return out
}

var _ = reporttemplate.ContractVersion
