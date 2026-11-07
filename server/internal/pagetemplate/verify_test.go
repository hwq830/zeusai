package pagetemplate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// fakeVerifier returns a canned result; err simulates an unreachable service.
type fakeVerifier struct {
	result VerifyResult
	err    error
}

func (f fakeVerifier) Verify(context.Context, []byte) (VerifyResult, error) { return f.result, f.err }

func passingVerifier() fakeVerifier {
	var result VerifyResult
	result.Viewport.OK = true
	result.Viewport.ScrollWidth, result.Viewport.InnerWidth = 390, 390
	result.PDF.OK = true
	result.PDF.Bytes = 120000
	return fakeVerifier{result: result}
}

func TestRunGateUsesRealVerifierResults(t *testing.T) {
	files, _ := builtinTemplate(t)
	result, _ := RunGate(context.Background(), KindReport, files, nil, passingVerifier(), true)
	require.True(t, result.Passed, "checks: %+v", result.Checks)
	require.Equal(t, CheckPassed, checkStatus(result, "viewport"))
	require.Equal(t, CheckPassed, checkStatus(result, "pdf"))
	require.Contains(t, detailOf(result, "viewport"), "390")
	require.Contains(t, detailOf(result, "pdf"), "字节")
}

func TestRunGateFailsOnViewportOverflow(t *testing.T) {
	files, _ := builtinTemplate(t)
	v := passingVerifier()
	v.result.Viewport.OK = false
	v.result.Viewport.ScrollWidth, v.result.Viewport.InnerWidth = 520, 390
	result, _ := RunGate(context.Background(), KindReport, files, nil, v, true)
	require.False(t, result.Passed)
	require.Equal(t, CheckFailed, checkStatus(result, "viewport"))
	require.Contains(t, detailOf(result, "viewport"), "scrollWidth 520")
}

func TestRunGateFailsOnBrokenPDF(t *testing.T) {
	files, _ := builtinTemplate(t)
	v := passingVerifier()
	v.result.PDF.OK = false
	v.result.PDF.Error = "打印结果不是 PDF"
	result, _ := RunGate(context.Background(), KindReport, files, nil, v, true)
	require.False(t, result.Passed)
	require.Equal(t, CheckFailed, checkStatus(result, "pdf"))
	require.Contains(t, detailOf(result, "pdf"), "打印结果不是 PDF")
}

func TestRunGateFailsClosedWhenVerifierIsUnreachable(t *testing.T) {
	files, _ := builtinTemplate(t)
	result, _ := RunGate(context.Background(), KindReport, files, nil, fakeVerifier{err: errors.New("connection refused")}, true)
	require.False(t, result.Passed)
	require.Equal(t, CheckFailed, checkStatus(result, "viewport"))
	require.Equal(t, CheckFailed, checkStatus(result, "pdf"))
	require.Contains(t, detailOf(result, "viewport"), "页面校验服务不可达")
	require.Contains(t, detailOf(result, "pdf"), "已阻断发布")
}

func TestRunGateSkipsWhenVerificationIsExplicitlyOptional(t *testing.T) {
	files, _ := builtinTemplate(t)
	result, _ := RunGate(context.Background(), KindReport, files, nil, fakeVerifier{err: errors.New("connection refused")}, false)
	require.True(t, result.Passed)
	require.Equal(t, CheckSkipped, checkStatus(result, "viewport"))
	require.Equal(t, CheckSkipped, checkStatus(result, "pdf"))
	require.Contains(t, detailOf(result, "viewport"), "VERIFY_REQUIRED=false")
}

func TestRemoteVerifierCallsOrchestrator(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		if r.URL.Path != "/internal/page-template-verify" {
			t.Errorf("path %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"viewport":{"ok":true,"scrollWidth":390,"innerWidth":390},"pdf":{"ok":true,"bytes":1000}}`))
	}))
	defer srv.Close()
	v := NewRemoteVerifier(srv.URL, "test-token")
	result, err := v.Verify(context.Background(), []byte("<html></html>"))
	require.NoError(t, err)
	require.True(t, result.Viewport.OK)
	require.True(t, result.PDF.OK)
	require.Equal(t, "Bearer test-token", gotAuth)
}

func TestRemoteVerifierUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()
	_, err := NewRemoteVerifier(url, "t").Verify(context.Background(), []byte("<html></html>"))
	require.Error(t, err)
}

func TestVerifyHandlerAuthAndShape(t *testing.T) {
	handler := VerifyHandler("secret", nil, passingVerifier())
	// Missing token.
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/page-template-verify", strings.NewReader(`{"html":"<html></html>"}`))
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusUnauthorized, rec.Code)

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/internal/page-template-verify", strings.NewReader(`{"html":"<html></html>"}`))
	req.Header.Set("Authorization", "Bearer secret")
	handler.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	require.Contains(t, rec.Body.String(), `"viewport"`)
}

func detailOf(v Validation, id string) string {
	for _, c := range v.Checks {
		if c.ID == id {
			return c.Detail
		}
	}
	return ""
}
