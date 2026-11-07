package pagetemplate

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func testAdmin(t *testing.T, q *db.Queries) db.AdminUser {
	t.Helper()
	a, err := q.CreateAdminUser(context.Background(), db.CreateAdminUserParams{
		Email: "pt-" + uuid.NewString() + "@zeus.test", PasswordHash: "x", Role: "owner",
	})
	require.NoError(t, err)
	return a
}

func captureLogs(buf *bytes.Buffer) func() {
	old := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})))
	return func() { slog.SetDefault(old) }
}

func TestEnsureBuiltinRendersByteIdenticalThroughDatabase(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	require.NoError(t, EnsureBuiltin(ctx, q))
	// Idempotent.
	require.NoError(t, EnsureBuiltin(ctx, q))

	binding, err := q.GetPageTemplateBinding(ctx, KindReport)
	require.NoError(t, err)
	require.NotNil(t, binding.VersionID)

	store := New(q, pool, nil)
	tpl, err := store.ReportTemplate(ctx)
	require.NoError(t, err)
	require.NotNil(t, tpl)

	want, err := reporttemplate.RenderBuiltin(FixtureData())
	require.NoError(t, err)
	got, err := tpl.Render(FixtureData())
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, got), "database-seeded template must render byte-for-byte like embed")
}

func TestPublishAndRollbackThroughStore(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	require.NoError(t, EnsureBuiltin(ctx, q))

	store := New(q, pool, nil)
	tplRow, err := q.GetPageTemplateByKind(ctx, KindReport)
	require.NoError(t, err)
	admin := testAdmin(t, q)

	files := reporttemplate.BuiltinFiles()
	files[reporttemplate.TemplateFile] = strings.Replace(files[reporttemplate.TemplateFile], "ZEUS / RESEARCH", "ZEUS / RESEARCH v2", 1)
	version, err := store.Upload(ctx, tplRow.ID, admin.ID, files, "")
	require.NoError(t, err)
	require.Equal(t, "draft", version.Status)

	validated, err := store.Validate(ctx, version.ID)
	require.NoError(t, err)
	require.Equal(t, "validated", validated.Status)
	var validation Validation
	require.NoError(t, jsonUnmarshal(validated.Validation, &validation))
	require.True(t, validation.Passed)
	require.Len(t, validation.Checks, 8)

	require.NoError(t, store.Publish(ctx, version.ID, admin.ID))
	store.Invalidate(KindReport)
	tpl, err := store.ReportTemplate(ctx)
	require.NoError(t, err)
	out, err := tpl.Render(FixtureData())
	require.NoError(t, err)
	require.Contains(t, string(out), "ZEUS / RESEARCH v2")

	rolled, err := store.Rollback(ctx, KindReport, admin.ID)
	require.NoError(t, err)
	require.Equal(t, int32(1), rolled.Version)
	store.Invalidate(KindReport)
	tpl, err = store.ReportTemplate(ctx)
	require.NoError(t, err)
	out, err = tpl.Render(FixtureData())
	require.NoError(t, err)
	want, err := reporttemplate.RenderBuiltin(FixtureData())
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, out), "rollback must restore the previous render")

	var audits int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM page_template_audit").Scan(&audits))
	require.GreaterOrEqual(t, audits, 3) // upload + publish + rollback
}

func TestUploadRejectsBadFiles(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	require.NoError(t, EnsureBuiltin(ctx, q))
	store := New(q, pool, nil)
	tpl, err := q.GetPageTemplateByKind(ctx, KindReport)
	require.NoError(t, err)
	admin := testAdmin(t, q)

	_, err = store.Upload(ctx, tpl.ID, admin.ID, map[string]string{"report.html.tmpl": "x", "evil.sh": "#!/bin/sh"}, "")
	require.ErrorContains(t, err, "evil.sh")

	_, err = store.Upload(ctx, tpl.ID, admin.ID, map[string]string{"report.html.tmpl": "x"}, "")
	require.ErrorContains(t, err, reporttemplate.ChartScriptFile)

	_, err = store.Upload(ctx, tpl.ID, admin.ID, reporttemplate.BuiltinFiles(), "report-template/v99")
	require.ErrorContains(t, err, "契约版本")
}

func TestFailedValidationBlocksPublish(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	require.NoError(t, EnsureBuiltin(ctx, q))
	store := New(q, pool, nil)
	tpl, err := q.GetPageTemplateByKind(ctx, KindReport)
	require.NoError(t, err)
	admin := testAdmin(t, q)

	files := reporttemplate.BuiltinFiles()
	files[reporttemplate.TemplateFile] = strings.Replace(files[reporttemplate.TemplateFile], "</head>",
		`<script src="https://evil.test/x.js"></script></head>`, 1)
	version, err := store.Upload(ctx, tpl.ID, admin.ID, files, "")
	require.NoError(t, err)
	validated, err := store.Validate(ctx, version.ID)
	require.NoError(t, err)
	require.Equal(t, "draft", validated.Status)
	require.Error(t, store.Publish(ctx, version.ID, admin.ID))
}

func TestActiveTemplateFallsBackWhenVersionIsUnreadable(t *testing.T) {
	pool := testdb.New(t)
	q := db.New(pool)
	ctx := context.Background()
	require.NoError(t, EnsureBuiltin(ctx, q))
	store := New(q, pool, nil)
	// Simulate a corrupt release: the bound version no longer has a usable template.
	_, err := pool.Exec(ctx, "UPDATE page_template_versions SET files = '{}'::jsonb")
	require.NoError(t, err)

	reporttemplate.SetProvider(store)
	t.Cleanup(func() { reporttemplate.SetProvider(nil) })
	var logs bytes.Buffer
	restore := captureLogs(&logs)
	defer restore()

	got, err := reporttemplate.Render(FixtureData())
	require.NoError(t, err)
	want, err := reporttemplate.RenderBuiltin(FixtureData())
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, got))
	require.Contains(t, logs.String(), "page template fallback")
}

func jsonUnmarshal(raw []byte, v any) error { return json.Unmarshal(raw, v) }
