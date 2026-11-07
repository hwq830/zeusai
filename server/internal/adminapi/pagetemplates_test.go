package adminapi_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/pagetemplate"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

type pageTemplateRow struct {
	Id              string  `json:"id"`
	Kind            string  `json:"kind"`
	Name            string  `json:"name"`
	ActiveVersion   *int    `json:"activeVersion"`
	ActiveVersionId *string `json:"activeVersionId"`
}

type pageTemplateList struct {
	Items []pageTemplateRow `json:"items"`
}

type pageTemplateCheck struct {
	Id     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type pageTemplateValidation struct {
	Passed bool                `json:"passed"`
	Checks []pageTemplateCheck `json:"checks"`
}

type pageTemplateVersion struct {
	Id         string                  `json:"id"`
	TemplateId string                  `json:"templateId"`
	Version    int                     `json:"version"`
	Status     string                  `json:"status"`
	Validation *pageTemplateValidation `json:"validation"`
}

type pageTemplateVersionList struct {
	Items []pageTemplateVersion `json:"items"`
}

func TestPageTemplateLifecycle(t *testing.T) {
	h := setup(t)
	ctx := context.Background()
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")

	var list pageTemplateList
	h.do("GET", "/admin/v1/page-templates?kind=report", nil, 200, &list)
	if len(list.Items) != 1 || list.Items[0].Kind != "report" || list.Items[0].ActiveVersion == nil || *list.Items[0].ActiveVersion != 1 {
		t.Fatalf("page templates: %+v", list.Items)
	}
	templateID := list.Items[0].Id

	var versions pageTemplateVersionList
	h.do("GET", "/admin/v1/page-templates/"+templateID+"/versions", nil, 200, &versions)
	if len(versions.Items) != 1 || versions.Items[0].Status != "published" {
		t.Fatalf("versions: %+v", versions.Items)
	}

	files := reporttemplate.BuiltinFiles()
	files[reporttemplate.TemplateFile] = strings.Replace(files[reporttemplate.TemplateFile], ":root{", ":root{--page-template-admin-v2:1;", 1)
	var upload pageTemplateVersion
	h.do("POST", "/admin/v1/page-templates/"+templateID+"/versions", map[string]any{"files": files}, 201, &upload)
	if upload.Version != 2 || upload.Status != "draft" {
		t.Fatalf("upload: %+v", upload)
	}

	var validated pageTemplateVersion
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/validate", nil, 200, &validated)
	if validated.Status != "validated" || validated.Validation == nil || !validated.Validation.Passed || len(validated.Validation.Checks) != 8 {
		t.Fatalf("validate: %+v", validated)
	}

	// Preview returns the rendered HTML for the fixed fixture.
	var preview struct{ Html string }
	h.do("GET", "/admin/v1/page-template-versions/"+upload.Id+"/preview", nil, 200, &preview)
	if !strings.Contains(preview.Html, "页面模板校验样例") || !strings.Contains(preview.Html, "--page-template-admin-v2:1") {
		t.Fatalf("preview: %.200s", preview.Html)
	}

	var published pageTemplateVersion
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/publish", nil, 200, &published)
	if published.Status != "published" {
		t.Fatalf("publish: %+v", published)
	}
	out, err := reporttemplate.Render(reporttemplate.Data{Title: "x", Markdown: "正文", Lang: "zh"})
	if err != nil || !strings.Contains(string(out), "--page-template-admin-v2:1") {
		t.Fatalf("published template not used: %v", err)
	}

	var rolled pageTemplateVersion
	h.do("POST", "/admin/v1/page-templates/report/rollback", nil, 200, &rolled)
	if rolled.Version != 1 || rolled.Status != "published" {
		t.Fatalf("rollback: %+v", rolled)
	}
	out, err = reporttemplate.Render(reporttemplate.Data{Title: "x", Markdown: "正文", Lang: "zh"})
	if err != nil || strings.Contains(string(out), "--page-template-admin-v2:1") {
		t.Fatalf("rollback did not restore the old template: %v", err)
	}

	var audits int
	if err := h.pool.QueryRow(ctx, "SELECT count(*) FROM page_template_audit WHERE action IN ('upload','publish','rollback')").Scan(&audits); err != nil {
		t.Fatal(err)
	}
	if audits < 3 {
		t.Fatalf("audits: %d", audits)
	}

	// A bad file name is rejected with the offending name.
	h.do("POST", "/admin/v1/page-templates/"+templateID+"/versions",
		map[string]any{"files": map[string]string{"report.html.tmpl": "x", "chartscript.js": "y", "evil.sh": "#!/bin/sh"}}, 400, nil)
}

func TestPageTemplateWriteNeedsPermission(t *testing.T) {
	h := setup(t)
	email := "viewer-" + uuid.NewString() + "@zeus.test"
	hash, err := adminapi.HashPassword("correct-horse-battery")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.q.CreateAdminUser(context.Background(), db.CreateAdminUserParams{
		Email: email, PasswordHash: hash, Role: "viewer",
	}); err != nil {
		t.Fatal(err)
	}
	h.login(email, "correct-horse-battery")
	// Reads are open to every role; writes need page_templates.write.
	h.do("GET", "/admin/v1/page-templates", nil, 200, nil)
	h.do("POST", "/admin/v1/page-templates/x/versions", map[string]any{"files": map[string]string{"report.html.tmpl": "x"}}, 403, nil)
}

// gateVerifier lets a test force a verification outcome.
type gateVerifier struct {
	result pagetemplate.VerifyResult
	err    error
}

func (g gateVerifier) Verify(context.Context, []byte) (pagetemplate.VerifyResult, error) {
	return g.result, g.err
}

func okGate() pagetemplate.VerifyResult {
	var r pagetemplate.VerifyResult
	r.Viewport.OK, r.Viewport.ScrollWidth, r.Viewport.InnerWidth = true, 390, 390
	r.PDF.OK, r.PDF.Bytes = true, 120000
	return r
}

func uploadTemplateVersion(t *testing.T, h *harness, templateID, marker string) pageTemplateVersion {
	t.Helper()
	files := reporttemplate.BuiltinFiles()
	files[reporttemplate.TemplateFile] = strings.Replace(files[reporttemplate.TemplateFile], ":root{", ":root{"+marker, 1)
	var upload pageTemplateVersion
	h.do("POST", "/admin/v1/page-templates/"+templateID+"/versions", map[string]any{"files": files}, 201, &upload)
	return upload
}

func firstTemplateID(t *testing.T, h *harness) string {
	t.Helper()
	var list pageTemplateList
	h.do("GET", "/admin/v1/page-templates", nil, 200, &list)
	if len(list.Items) != 1 {
		t.Fatalf("templates: %+v", list.Items)
	}
	return list.Items[0].Id
}

func TestPageTemplateGateFailsClosedWhenVerifierUnreachable(t *testing.T) {
	h := setupWith(t, func(s *adminapi.Server) {
		s.PageTemplates.Verifier = gateVerifier{err: errors.New("dial tcp 10.0.0.5:8090: connect: connection refused")}
		s.PageTemplates.VerifyRequired = true
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	templateID := firstTemplateID(t, h)
	upload := uploadTemplateVersion(t, h, templateID, "--gate-closed:1;")

	var validated pageTemplateVersion
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/validate", nil, 200, &validated)
	if validated.Status != "draft" || validated.Validation == nil || validated.Validation.Passed {
		t.Fatalf("validate: %+v", validated)
	}
	status := map[string]string{}
	for _, c := range validated.Validation.Checks {
		status[c.Id] = c.Status
	}
	if status["viewport"] != "failed" || status["pdf"] != "failed" {
		t.Fatalf("checks: %+v", validated.Validation.Checks)
	}
	var e struct{ Code, Message string }
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/publish", nil, 400, &e)
	if !strings.Contains(e.Message, "页面校验服务不可达") && !strings.Contains(e.Message, "校验") {
		t.Fatalf("publish error: %+v", e)
	}
}

func TestPageTemplateGateOptionalSkipsAndAudits(t *testing.T) {
	h := setupWith(t, func(s *adminapi.Server) {
		s.PageTemplates.Verifier = gateVerifier{err: errors.New("connection refused")}
		s.PageTemplates.VerifyRequired = false
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	templateID := firstTemplateID(t, h)
	upload := uploadTemplateVersion(t, h, templateID, "--gate-optional:1;")

	var validated pageTemplateVersion
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/validate", nil, 200, &validated)
	if validated.Status != "validated" || validated.Validation == nil || !validated.Validation.Passed {
		t.Fatalf("validate: %+v", validated)
	}
	status := map[string]string{}
	for _, c := range validated.Validation.Checks {
		status[c.Id] = c.Status
	}
	if status["viewport"] != "skipped" || status["pdf"] != "skipped" {
		t.Fatalf("checks: %+v", validated.Validation.Checks)
	}
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/publish", nil, 200, nil)

	// The audit row records that verification was skipped.
	var detail []byte
	if err := h.pool.QueryRow(context.Background(),
		"SELECT detail FROM page_template_audit WHERE action = 'publish' ORDER BY at DESC LIMIT 1").Scan(&detail); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(detail), "unverified") {
		t.Fatalf("audit detail: %s", detail)
	}
}

func TestPageTemplateGateRejectsOverflow(t *testing.T) {
	h := setupWith(t, func(s *adminapi.Server) {
		r := okGate()
		r.Viewport.OK, r.Viewport.ScrollWidth, r.Viewport.InnerWidth = false, 520, 390
		s.PageTemplates.Verifier = gateVerifier{result: r}
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	templateID := firstTemplateID(t, h)
	upload := uploadTemplateVersion(t, h, templateID, "--gate-overflow:1;")

	var validated pageTemplateVersion
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/validate", nil, 200, &validated)
	if validated.Validation == nil || validated.Validation.Passed {
		t.Fatalf("validate: %+v", validated)
	}
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/publish", nil, 400, nil)
}

func TestPageTemplateGateRejectsEmptyPDF(t *testing.T) {
	h := setupWith(t, func(s *adminapi.Server) {
		r := okGate()
		r.PDF.OK, r.PDF.Error = false, "打印结果不是 PDF"
		s.PageTemplates.Verifier = gateVerifier{result: r}
	})
	h.createOwner("owner@zeus.test", "correct-horse-battery")
	h.login("owner@zeus.test", "correct-horse-battery")
	templateID := firstTemplateID(t, h)
	upload := uploadTemplateVersion(t, h, templateID, "--gate-pdf:1;")

	var validated pageTemplateVersion
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/validate", nil, 200, &validated)
	if validated.Validation == nil || validated.Validation.Passed {
		t.Fatalf("validate: %+v", validated)
	}
	h.do("POST", "/admin/v1/page-template-versions/"+upload.Id+"/publish", nil, 400, nil)
}
