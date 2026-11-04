package llm

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestReasoningEffortOnlyForSupportedDeepSeekRequests(t *testing.T) {
	for _, tc := range []struct {
		name, base, model, effort, want string
		disable                         bool
	}{
		{"official flash", "https://api.deepseek.com", "deepseek-flash", "low", "low", false},
		{"official pro", "https://api.deepseek.com/v1", "deepseek-v4-pro", "high", "high", false},
		{"empty", "https://api.deepseek.com", "deepseek-flash", "", "", false},
		{"invalid level", "https://api.deepseek.com", "deepseek-flash", "unsupported", "", false},
		{"older model", "https://api.deepseek.com", "deepseek-chat", "low", "", false},
		{"other host", "https://example.com/v1", "deepseek-flash", "low", "", false},
		{"lookalike host", "https://api.deepseek.com.example.com", "deepseek-flash", "low", "", false},
		{"qwen fallback", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3.8-max", "low", "", false},
		{"disabled takes precedence", "https://api.deepseek.com", "deepseek-flash", "low", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var body map[string]json.RawMessage
			client := &http.Client{Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"model":"test","choices":[{"message":{"content":"ok"},"finish_reason":"stop"}]}`))}, nil
			})}
			provider := OpenAICompat{Name: "deepseek", BaseURL: tc.base, HTTP: client}
			_, err := provider.Complete(context.Background(), Request{Model: tc.model, ReasoningEffort: tc.effort, DisableThinking: tc.disable})
			require.NoError(t, err)
			if tc.want == "" {
				require.NotContains(t, body, "reasoning_effort")
			} else {
				var effort string
				require.NoError(t, json.Unmarshal(body["reasoning_effort"], &effort))
				require.Equal(t, tc.want, effort)
			}
			if tc.disable {
				require.JSONEq(t, `{"type":"disabled"}`, string(body["thinking"]))
			}
		})
	}
}

func TestNoThinkingControlsOnlyKnownCompatibleModels(t *testing.T) {
	for _, tc := range []struct {
		name, provider, base, model string
		wantThinking, wantEnable    bool
	}{
		{"deepseek flash", "deepseek", "https://api.deepseek.com", "deepseek-flash", true, false},
		{"deepseek pro", "custom", "https://api.deepseek.com", "deepseek-v4-pro", true, false},
		{"legacy deepseek", "deepseek", "https://api.deepseek.com", "deepseek-chat", false, false},
		{"bailian qwen", "qwen", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen3.8-max", false, true},
		{"bailian older qwen", "qwen", "https://dashscope.aliyuncs.com/compatible-mode/v1", "qwen-max", false, false},
		{"unknown endpoint", "custom", "https://example.com/v1", "deepseek-flash", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := chatRequest{Model: tc.model}
			setNoThinking(&body, tc.provider, tc.base)
			require.Equal(t, tc.wantThinking, body.Thinking != nil)
			require.Equal(t, tc.wantEnable, body.EnableThinking != nil)
			if body.Thinking != nil {
				require.Equal(t, "disabled", body.Thinking.Type)
			}
			if body.EnableThinking != nil {
				require.False(t, *body.EnableThinking)
				encoded, err := json.Marshal(body)
				require.NoError(t, err)
				require.Contains(t, string(encoded), `"enable_thinking":false`)
			}
		})
	}
}

func TestDeepSeekReviewSendsNoThinkingAndRetainsOnlyReasoningMetadata(t *testing.T) {
	requestBody := make(chan map[string]json.RawMessage, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]json.RawMessage
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid request", http.StatusBadRequest)
			return
		}
		requestBody <- body
		_, _ = w.Write([]byte(`{"id":"id","model":"deepseek-flash","choices":[{"message":{"content":"{\"status\":\"pass\",\"issues\":[]}","reasoning_content":"private reasoning"},"finish_reason":"stop"}],"usage":{"prompt_tokens":100,"completion_tokens":140,"completion_tokens_details":{"reasoning_tokens":20}}}`))
	}))
	defer server.Close()
	provider := OpenAICompat{Name: "deepseek", BaseURL: server.URL, HTTP: server.Client()}
	var out struct {
		Status string `json:"status"`
	}
	resp, err := CompleteJSON(context.Background(), provider, Request{Purpose: "report.review", Model: "deepseek-flash", MaxTokens: 2400, DisableThinking: true}, &out)
	require.NoError(t, err)
	body := <-requestBody
	var thinking struct {
		Type string `json:"type"`
	}
	require.NoError(t, json.Unmarshal(body["thinking"], &thinking))
	require.Equal(t, "disabled", thinking.Type)
	require.Equal(t, json.RawMessage(`{"type":"json_object"}`), body["response_format"])
	require.Equal(t, "pass", out.Status)
	require.Equal(t, 140, resp.Usage.OutputTokens)
	require.Equal(t, 20, resp.ReasoningTokens)
	require.True(t, resp.ReasoningContentPresent)
	require.NotContains(t, resp.Content, "private reasoning")
}
