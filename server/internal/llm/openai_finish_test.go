package llm_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/hwq830/zeusai/server/internal/calllog"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/stretchr/testify/require"
)

type captureCalls struct{ ends []calllog.End }

func (*captureCalls) Start(context.Context, calllog.Begin) error { return nil }
func (c *captureCalls) Finish(_ context.Context, _ uuid.UUID, e calllog.End) error {
	c.ends = append(c.ends, e)
	return nil
}
func TestOpenAITruncatedJSONPreservesUsageAndRecordsFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"id","model":"m","choices":[{"message":{"role":"assistant","content":"{\"status\":\"pass\",\"issues\":[]}"},"finish_reason":"length"}],"usage":{"prompt_tokens":100,"completion_tokens":2400}}`))
	}))
	defer server.Close()
	calls := &captureCalls{}
	provider := llm.OpenAICompat{Name: "test", BaseURL: server.URL, HTTP: server.Client(), Calls: &calllog.Recorder{Store: calls}}
	var out map[string]any
	resp, err := llm.CompleteJSON(context.Background(), provider, llm.Request{Purpose: "report.review", MaxTokens: 2400}, &out)
	require.True(t, errors.Is(err, llm.ErrOutputTruncated))
	require.True(t, resp.UsageKnown)
	require.Equal(t, 2400, resp.Usage.OutputTokens)
	require.Len(t, calls.ends, 1)
	require.Equal(t, "failed", calls.ends[0].Status)
	require.Equal(t, "output_truncated", calls.ends[0].ErrorCode)
	require.Equal(t, 200, calls.ends[0].HTTPStatus)
	require.Equal(t, 2400, calls.ends[0].OutputTokens)
}
