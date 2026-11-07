package pagetemplate

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/hwq830/zeusai/server/internal/pdf"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

// TestGatePassesWithRealChromium runs the layout and PDF checks against the embedded template
// with a real browser. Skipped when ZEUS_TEST_CHROMIUM is not set.
func TestGatePassesWithRealChromium(t *testing.T) {
	path := os.Getenv("ZEUS_TEST_CHROMIUM")
	if path == "" {
		t.Skip("ZEUS_TEST_CHROMIUM not set")
	}
	files := reporttemplate.BuiltinFiles()
	hashes := reporttemplate.ComputeCSPHashes(files[reporttemplate.TemplateFile], files[reporttemplate.ChartScriptFile])
	verifier := ChromiumVerifier{Chromium: pdf.Chromium{Path: path, NoSandbox: true, Timeout: 30 * time.Second}}
	result, _ := RunGate(context.Background(), KindReport, files, hashes, verifier, true)
	require.True(t, result.Passed, "checks: %+v", result.Checks)
	require.Equal(t, CheckPassed, checkStatus(result, "viewport"), detailOf(result, "viewport"))
	require.Equal(t, CheckPassed, checkStatus(result, "pdf"), detailOf(result, "pdf"))
}
