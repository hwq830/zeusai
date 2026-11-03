package llm

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

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
