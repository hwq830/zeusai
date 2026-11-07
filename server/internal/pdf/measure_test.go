package pdf

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestMeasureViewport drives real headless Chromium: a 900px block must overflow a 390px
// viewport, a 300px block must not. Skipped when ZEUS_TEST_CHROMIUM is not set.
func TestMeasureViewport(t *testing.T) {
	path := os.Getenv("ZEUS_TEST_CHROMIUM")
	if path == "" {
		t.Skip("ZEUS_TEST_CHROMIUM not set")
	}
	c := Chromium{Path: path, NoSandbox: true, Timeout: 30 * time.Second}
	wide := []byte(`<!doctype html><html><head><meta charset="utf-8"></head><body><div style="width:900px;height:40px">wide</div></body></html>`)
	viewport, err := c.MeasureViewport(context.Background(), wide, 390)
	require.NoError(t, err)
	require.Greater(t, viewport.ScrollWidth, viewport.InnerWidth, "900px block must overflow 390px")

	narrow := []byte(`<!doctype html><html><head><meta charset="utf-8"></head><body><div style="width:300px;height:40px">narrow</div></body></html>`)
	viewport, err = c.MeasureViewport(context.Background(), narrow, 390)
	require.NoError(t, err)
	require.LessOrEqual(t, viewport.ScrollWidth, viewport.InnerWidth+1)
}
