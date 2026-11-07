// Package pagetemplate stores the report page layout as publishable data: templates,
// immutable versions, a per-kind binding and an audit trail. Rendering stays in
// web/report-template; this package supplies the active version and the publish gate.
package pagetemplate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/hwq830/zeusai/server/internal/apperr"
	"github.com/hwq830/zeusai/server/internal/db"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// KindReport is the only kind wired in P1.
const KindReport = "report"

// DefaultTTL bounds how long the active template is cached in a process.
const DefaultTTL = time.Minute

const (
	maxFileBytes  = 1 << 20
	maxTotalBytes = 3 << 20
)

// AllowedFiles is the upload whitelist; anything else is rejected by name.
var AllowedFiles = map[string]bool{
	reporttemplate.TemplateFile:    true,
	reporttemplate.ChartScriptFile: true,
	reporttemplate.UsageFile:       true,
	reporttemplate.NoticesFile:     true,
}

// Store reads the active version and owns the publish workflow.
type Store struct {
	Q    *db.Queries
	Pool interface {
		Begin(ctx context.Context) (pgx.Tx, error)
	}
	// Verifier runs the layout and print checks; nil means they cannot run.
	Verifier Verifier
	// VerifyRequired makes a missing/unreachable verifier fail those checks (fail closed).
	VerifyRequired bool
	TTL            time.Duration

	mu    sync.Mutex
	cache map[string]cachedTemplate
}

type cachedTemplate struct {
	version uuid.UUID
	tpl     *reporttemplate.Template
	at      time.Time
}

func New(q *db.Queries, pool interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}, verifier Verifier, verifyRequired bool) *Store {
	return &Store{Q: q, Pool: pool, Verifier: verifier, VerifyRequired: verifyRequired, TTL: DefaultTTL, cache: map[string]cachedTemplate{}}
}

// ReportTemplate implements reporttemplate.Provider. A missing binding returns (nil, nil) so
// the renderer falls back to the embedded template without logging an error.
func (s *Store) ReportTemplate(ctx context.Context) (*reporttemplate.Template, error) {
	return s.active(ctx, KindReport)
}

func (s *Store) active(ctx context.Context, kind string) (*reporttemplate.Template, error) {
	v, err := s.Q.ActivePageTemplateVersion(ctx, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	ttl := s.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	s.mu.Lock()
	if entry, ok := s.cache[kind]; ok && entry.version == v.ID && time.Since(entry.at) < ttl {
		s.mu.Unlock()
		return entry.tpl, nil
	}
	s.mu.Unlock()
	tpl, err := reporttemplate.NewTemplate(v.ID.String(), decodeFiles(v.Files), decodeHashes(v.CspHashes))
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache[kind] = cachedTemplate{version: v.ID, tpl: tpl, at: time.Now()}
	s.mu.Unlock()
	return tpl, nil
}

// Invalidate drops the cached template for a kind after publish or rollback.
func (s *Store) Invalidate(kind string) {
	s.mu.Lock()
	delete(s.cache, kind)
	s.mu.Unlock()
}

// EnsureBuiltin imports the embedded report template as version 1 and binds it, so a fresh
// database renders exactly like the embedded build. Idempotent; safe from every process.
func EnsureBuiltin(ctx context.Context, q *db.Queries) error {
	if _, err := q.GetPageTemplateByKind(ctx, KindReport); err == nil {
		return nil
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	tpl, err := q.CreatePageTemplate(ctx, db.CreatePageTemplateParams{Kind: KindReport, Name: "内置报告模板", Description: "由二进制内置模板导入的初始版本"})
	if err != nil {
		if concurrent, getErr := q.GetPageTemplateByKind(ctx, KindReport); getErr == nil {
			tpl = concurrent
		} else {
			return err
		}
	}
	files := reporttemplate.BuiltinFiles()
	hashes := reporttemplate.ComputeCSPHashes(files[reporttemplate.TemplateFile], files[reporttemplate.ChartScriptFile])
	validation, _ := json.Marshal(Validation{Passed: true, Checks: []Check{{
		ID: "builtin", Name: "内置模板导入", Status: CheckSkipped, Detail: "由 embed 导入，未跑发布闸门",
	}}})
	version, err := q.CreatePageTemplateVersion(ctx, db.CreatePageTemplateVersionParams{
		TemplateID: tpl.ID, Version: 1, Files: encodeFiles(files), ContractVersion: reporttemplate.ContractVersion,
		CspHashes: encodeHashes(hashes),
	})
	if err != nil {
		if existing, getErr := q.GetPageTemplateByKind(ctx, KindReport); getErr == nil && existing.ID == tpl.ID {
			return nil
		}
		return err
	}
	if err := q.SetPageTemplateVersionValidation(ctx, db.SetPageTemplateVersionValidationParams{
		ID: version.ID, Status: "validated", Validation: validation, CspHashes: encodeHashes(hashes),
	}); err != nil {
		return err
	}
	if err := q.PublishPageTemplateVersion(ctx, version.ID); err != nil {
		return err
	}
	return q.UpsertPageTemplateBinding(ctx, db.UpsertPageTemplateBindingParams{Kind: KindReport, VersionID: version.ID})
}

// TemplateSummary is one console list row.
type TemplateSummary struct {
	ID                uuid.UUID
	Kind              string
	Name              string
	Description       string
	CreatedAt         time.Time
	ArchivedAt        *time.Time
	ActiveVersionID   *uuid.UUID
	ActiveVersion     *int32
	ActivePublishedAt *time.Time
}

func (s *Store) List(ctx context.Context, kind *string) ([]TemplateSummary, error) {
	rows, err := s.Q.ListPageTemplates(ctx, kind)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	out := make([]TemplateSummary, 0, len(rows))
	for _, r := range rows {
		out = append(out, TemplateSummary{
			ID: r.ID, Kind: r.Kind, Name: r.Name, Description: r.Description, CreatedAt: r.CreatedAt, ArchivedAt: r.ArchivedAt,
			ActiveVersionID: r.ActiveVersionID, ActiveVersion: r.ActiveVersion, ActivePublishedAt: r.ActivePublishedAt,
		})
	}
	return out, nil
}

func (s *Store) Template(ctx context.Context, id uuid.UUID) (db.PageTemplate, error) {
	t, err := s.Q.GetPageTemplate(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return t, apperr.NotFound("页面模板不存在")
	}
	return t, err
}

func (s *Store) Versions(ctx context.Context, templateID uuid.UUID) ([]db.PageTemplateVersion, error) {
	if _, err := s.Template(ctx, templateID); err != nil {
		return nil, err
	}
	rows, err := s.Q.ListPageTemplateVersions(ctx, templateID)
	if err != nil {
		return nil, apperr.Internal(err)
	}
	return rows, nil
}

func (s *Store) Version(ctx context.Context, id uuid.UUID) (db.PageTemplateVersion, error) {
	v, err := s.Q.GetPageTemplateVersion(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return v, apperr.NotFound("模板版本不存在")
	}
	return v, err
}

// Upload stores a new draft version. Files are whitelisted, text-only and size-capped.
func (s *Store) Upload(ctx context.Context, templateID, actor uuid.UUID, files map[string]string, contractVersion string) (db.PageTemplateVersion, error) {
	tpl, err := s.Template(ctx, templateID)
	if err != nil {
		return db.PageTemplateVersion{}, err
	}
	if contractVersion == "" {
		contractVersion = reporttemplate.ContractVersion
	}
	if contractVersion != reporttemplate.ContractVersion {
		return db.PageTemplateVersion{}, apperr.Invalid("契约版本不匹配：当前只支持 " + reporttemplate.ContractVersion)
	}
	if err := checkFiles(tpl.Kind, files); err != nil {
		return db.PageTemplateVersion{}, err
	}
	var version db.PageTemplateVersion
	for attempt := 0; attempt < 2; attempt++ {
		next, err := s.Q.NextPageTemplateVersion(ctx, templateID)
		if err != nil {
			return version, apperr.Internal(err)
		}
		version, err = s.Q.CreatePageTemplateVersion(ctx, db.CreatePageTemplateVersionParams{
			TemplateID: templateID, Version: int32(next), Files: encodeFiles(files), ContractVersion: contractVersion,
			CspHashes: encodeHashes(reporttemplate.ComputeCSPHashes(files[reporttemplate.TemplateFile], files[reporttemplate.ChartScriptFile])),
			CreatedBy: &actor,
		})
		if err == nil {
			break
		}
		if !isUniqueViolation(err) || attempt == 1 {
			return db.PageTemplateVersion{}, apperr.Internal(err)
		}
	}
	s.audit(ctx, actor, "upload", tpl.ID, version.ID, map[string]any{"version": version.Version, "files": fileNames(files)})
	return version, nil
}

// Validate runs the publish gate and records the result on the version.
func (s *Store) Validate(ctx context.Context, versionID uuid.UUID) (db.PageTemplateVersion, error) {
	v, err := s.Version(ctx, versionID)
	if err != nil {
		return v, err
	}
	tpl, err := s.Template(ctx, v.TemplateID)
	if err != nil {
		return v, err
	}
	result, cspHashes := RunGate(ctx, tpl.Kind, decodeFiles(v.Files), decodeHashes(v.CspHashes), s.Verifier, s.VerifyRequired)
	raw, _ := json.Marshal(result)
	status := "draft"
	if result.Passed {
		status = "validated"
	}
	if err := s.Q.SetPageTemplateVersionValidation(ctx, db.SetPageTemplateVersionValidationParams{
		ID: v.ID, Status: status, Validation: raw, CspHashes: encodeHashes(cspHashes),
	}); err != nil {
		return v, apperr.Internal(err)
	}
	v.Status, v.Validation, v.CspHashes = status, raw, encodeHashes(cspHashes)
	return v, nil
}

// Publish binds a validated version for its kind in one transaction.
func (s *Store) Publish(ctx context.Context, versionID, actor uuid.UUID) error {
	v, err := s.Version(ctx, versionID)
	if err != nil {
		return err
	}
	tpl, err := s.Template(ctx, v.TemplateID)
	if err != nil {
		return err
	}
	if v.ContractVersion != reporttemplate.ContractVersion {
		return apperr.Invalid("契约版本不匹配：" + v.ContractVersion)
	}
	if v.Status != "validated" {
		return apperr.Invalid("只有通过校验的版本才能发布")
	}
	if !storedValidationPassed(v.Validation) {
		return apperr.Invalid("校验未通过，不能发布")
	}
	if err := s.withTx(ctx, func(q *db.Queries) error {
		if err := q.RetirePublishedPageTemplateVersions(ctx, tpl.ID); err != nil {
			return err
		}
		if err := q.PublishPageTemplateVersion(ctx, v.ID); err != nil {
			return err
		}
		if err := q.UpsertPageTemplateBinding(ctx, db.UpsertPageTemplateBindingParams{Kind: tpl.Kind, VersionID: v.ID, UpdatedBy: &actor}); err != nil {
			return err
		}
		detail := map[string]any{"version": v.Version, "kind": tpl.Kind}
		if unverified := skippedChecks(v.Validation); len(unverified) > 0 {
			detail["unverified"] = unverified
		}
		return q.InsertPageTemplateAudit(ctx, db.InsertPageTemplateAuditParams{
			ActorID: &actor, Action: "publish", TemplateID: &tpl.ID, VersionID: &v.ID, Detail: mustJSON(detail),
		})
	}); err != nil {
		return err
	}
	s.Invalidate(tpl.Kind)
	return nil
}

// Rollback moves the kind's binding to the previously published version.
func (s *Store) Rollback(ctx context.Context, kind string, actor uuid.UUID) (db.PageTemplateVersion, error) {
	binding, err := s.Q.GetPageTemplateBinding(ctx, kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PageTemplateVersion{}, apperr.NotFound("该类型还没有发布过页面模板")
	}
	if err != nil {
		return db.PageTemplateVersion{}, apperr.Internal(err)
	}
	current, err := s.Version(ctx, binding.VersionID)
	if err != nil {
		return db.PageTemplateVersion{}, err
	}
	prev, err := s.Q.PreviousPublishedPageTemplateVersion(ctx, db.PreviousPublishedPageTemplateVersionParams{
		TemplateID: current.TemplateID, ExcludeID: current.ID,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return db.PageTemplateVersion{}, apperr.Conflict("没有可回退的历史版本")
	}
	if err != nil {
		return db.PageTemplateVersion{}, apperr.Internal(err)
	}
	if err := s.withTx(ctx, func(q *db.Queries) error {
		if err := q.RetirePublishedPageTemplateVersions(ctx, current.TemplateID); err != nil {
			return err
		}
		if err := q.PublishPageTemplateVersion(ctx, prev.ID); err != nil {
			return err
		}
		if err := q.UpsertPageTemplateBinding(ctx, db.UpsertPageTemplateBindingParams{Kind: kind, VersionID: prev.ID, UpdatedBy: &actor}); err != nil {
			return err
		}
		return q.InsertPageTemplateAudit(ctx, db.InsertPageTemplateAuditParams{
			ActorID: &actor, Action: "rollback", TemplateID: &current.TemplateID, VersionID: &prev.ID,
			Detail: mustJSON(map[string]any{"from": current.Version, "to": prev.Version, "kind": kind}),
		})
	}); err != nil {
		return db.PageTemplateVersion{}, err
	}
	s.Invalidate(kind)
	// Re-read: the row fetched before the transaction still carries its retired status.
	return s.Version(ctx, prev.ID)
}

// Preview renders one version with the caller's data.
func (s *Store) Preview(ctx context.Context, versionID uuid.UUID, data reporttemplate.Data) ([]byte, error) {
	v, err := s.Version(ctx, versionID)
	if err != nil {
		return nil, err
	}
	tpl, err := reporttemplate.NewTemplate(v.ID.String(), decodeFiles(v.Files), decodeHashes(v.CspHashes))
	if err != nil {
		return nil, apperr.Invalid("模板无法解析：" + err.Error())
	}
	out, err := tpl.Render(data)
	if err != nil {
		return nil, apperr.Invalid("模板渲染失败：" + err.Error())
	}
	return out, nil
}

func (s *Store) audit(ctx context.Context, actor uuid.UUID, action string, templateID, versionID uuid.UUID, detail map[string]any) {
	_ = s.Q.InsertPageTemplateAudit(ctx, db.InsertPageTemplateAuditParams{
		ActorID: &actor, Action: action, TemplateID: &templateID, VersionID: &versionID, Detail: mustJSON(detail),
	})
}

func (s *Store) withTx(ctx context.Context, fn func(*db.Queries) error) error {
	if s.Pool == nil {
		return fn(s.Q)
	}
	tx, err := s.Pool.Begin(ctx)
	if err != nil {
		return apperr.Internal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if err := fn(s.Q.WithTx(tx)); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func checkFiles(kind string, files map[string]string) error {
	if len(files) == 0 {
		return apperr.Invalid("请至少上传 " + reporttemplate.TemplateFile)
	}
	total := 0
	var bad []string
	for name, content := range files {
		if !AllowedFiles[name] {
			bad = append(bad, name)
			continue
		}
		if strings.ContainsRune(content, 0) {
			return apperr.Invalid("文件是二进制内容，不能作为模板：" + name)
		}
		if len(content) > maxFileBytes {
			return apperr.Invalid(fmt.Sprintf("%s 超过 1MB", name))
		}
		total += len(content)
	}
	if len(bad) > 0 {
		return apperr.Invalid("不允许的文件：" + strings.Join(bad, "、") + "（只允许 report.html.tmpl、chartscript.js、usage.md、notices.md）")
	}
	if total > maxTotalBytes {
		return apperr.Invalid("模板总大小超过 3MB")
	}
	if strings.TrimSpace(files[reporttemplate.TemplateFile]) == "" {
		return apperr.Invalid("缺少 " + reporttemplate.TemplateFile)
	}
	if kind == KindReport && strings.TrimSpace(files[reporttemplate.ChartScriptFile]) == "" {
		return apperr.Invalid("报告模板必须包含 " + reporttemplate.ChartScriptFile)
	}
	return nil
}

func fileNames(files map[string]string) []string {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	return names
}

func decodeFiles(raw json.RawMessage) map[string]string {
	out := map[string]string{}
	_ = json.Unmarshal(raw, &out)
	return out
}

func encodeFiles(files map[string]string) json.RawMessage {
	b, _ := json.Marshal(files)
	return b
}

func decodeHashes(raw json.RawMessage) []string {
	var out []string
	_ = json.Unmarshal(raw, &out)
	return out
}

func encodeHashes(hashes []string) json.RawMessage {
	if hashes == nil {
		hashes = []string{}
	}
	b, _ := json.Marshal(hashes)
	return b
}

func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

func isUniqueViolation(err error) bool {
	var pgErr interface{ SQLState() string }
	return errors.As(err, &pgErr) && pgErr.SQLState() == "23505"
}

// skippedChecks names gate checks that were skipped, so the audit trail can show which
// verification did not run when publishing was allowed without it.
func skippedChecks(raw json.RawMessage) []string {
	var v Validation
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return nil
	}
	var out []string
	for _, c := range v.Checks {
		if c.Status == CheckSkipped && (c.ID == "viewport" || c.ID == "pdf") {
			out = append(out, c.ID)
		}
	}
	return out
}

func storedValidationPassed(raw json.RawMessage) bool {
	var v Validation
	if len(raw) == 0 || json.Unmarshal(raw, &v) != nil {
		return false
	}
	return v.Passed
}
