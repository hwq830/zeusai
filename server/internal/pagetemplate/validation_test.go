package pagetemplate

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func builtinTemplate(t *testing.T) (map[string]string, []string) {
	t.Helper()
	files := reporttemplate.BuiltinFiles()
	hashes := reporttemplate.ComputeCSPHashes(files[reporttemplate.TemplateFile], files[reporttemplate.ChartScriptFile])
	return files, hashes
}

// The seeded v1 must render byte-for-byte like the embedded template: the import may not
// change a single existing report.
func TestBuiltinImportRendersByteIdentical(t *testing.T) {
	files, hashes := builtinTemplate(t)
	tpl, err := reporttemplate.NewTemplate("builtin-v1", files, hashes)
	require.NoError(t, err)
	want, err := reporttemplate.RenderBuiltin(FixtureData())
	require.NoError(t, err)
	got, err := tpl.Render(FixtureData())
	require.NoError(t, err)
	require.True(t, bytes.Equal(want, got), "stored builtin template renders differently from embed")
}

func TestGatePassesBuiltinTemplate(t *testing.T) {
	files, _ := builtinTemplate(t)
	result, hashes := RunGate(context.Background(), KindReport, files, nil, passingVerifier(), true)
	require.True(t, result.Passed, "checks: %+v", result.Checks)
	require.NotEmpty(t, hashes)
	status := map[string]string{}
	for _, c := range result.Checks {
		status[c.ID] = c.Status
	}
	require.Len(t, result.Checks, 8)
	require.Equal(t, CheckPassed, status["render"])
	require.Equal(t, CheckPassed, status["charts"])
	require.Equal(t, CheckPassed, status["csp"])
	require.Equal(t, CheckPassed, status["external"])
	require.Equal(t, CheckPassed, status["viewport"])
	require.Equal(t, CheckPassed, status["budget"])
	require.Equal(t, CheckPassed, status["pdf"])
	require.Equal(t, CheckPassed, status["structure"])
}

func TestGateRejectsExternalResource(t *testing.T) {
	files, _ := builtinTemplate(t)
	files[reporttemplate.TemplateFile] = strings.Replace(files[reporttemplate.TemplateFile], "</head>",
		`<script src="https://evil.test/x.js"></script></head>`, 1)
	result, _ := RunGate(context.Background(), KindReport, files, nil, passingVerifier(), true)
	require.False(t, result.Passed)
	require.Equal(t, CheckFailed, checkStatus(result, "external"))
}

func TestGateRejectsUnhashedInlineScript(t *testing.T) {
	files, _ := builtinTemplate(t)
	// A script whose body is template data cannot be hashed from the source (the executed
	// value differs), so the gate must reject it.
	files[reporttemplate.TemplateFile] = files[reporttemplate.TemplateFile] + "<script>{{.Title}}</script>"
	result, _ := RunGate(context.Background(), KindReport, files, nil, passingVerifier(), true)
	require.False(t, result.Passed)
	require.Equal(t, CheckFailed, checkStatus(result, "csp"))
}

func TestGateRejectsBrokenTemplate(t *testing.T) {
	files, _ := builtinTemplate(t)
	files[reporttemplate.TemplateFile] = "{{.Title"
	result, _ := RunGate(context.Background(), KindReport, files, nil, passingVerifier(), true)
	require.False(t, result.Passed)
	require.Equal(t, CheckFailed, checkStatus(result, "render"))
}

func TestGateRejectsMissingChartScript(t *testing.T) {
	files, _ := builtinTemplate(t)
	delete(files, reporttemplate.ChartScriptFile)
	result, _ := RunGate(context.Background(), KindReport, files, nil, passingVerifier(), true)
	require.False(t, result.Passed)
}

func checkStatus(v Validation, id string) string {
	for _, c := range v.Checks {
		if c.ID == id {
			return c.Status
		}
	}
	return ""
}
