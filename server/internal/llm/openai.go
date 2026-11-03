package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/hwq830/zeusai/server/internal/calllog"
)

// OpenAICompat calls an OpenAI-compatible /chat/completions endpoint. Models maps a tier to
// the model name on that endpoint.
type OpenAICompat struct {
	Calls   *calllog.Recorder
	BaseURL string
	APIKey  string
	// KeyFunc, when set, supplies the key per call instead of APIKey (keys switched in the
	// admin console take effect without a restart).
	KeyFunc func(context.Context) string
	// BaseURLFunc, when set and non-empty, overrides BaseURL per call (an address saved with
	// the console key).
	BaseURLFunc func(context.Context) string
	Name        string
	Models      map[Tier]string
	HTTP        *http.Client
}

type chatRequest struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	MaxTokens      int       `json:"max_tokens,omitempty"`
	Temperature    *float64  `json:"temperature,omitempty"`
	ResponseFormat any       `json:"response_format,omitempty"`
}

type chatResponse struct {
	ID      string `json:"id"`
	Model   string `json:"model"`
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens        int `json:"prompt_tokens"`
		CompletionTokens    int `json:"completion_tokens"`
		PromptTokensDetails struct {
			CachedTokens int `json:"cached_tokens"`
		} `json:"prompt_tokens_details"`
	} `json:"usage"`
}

// StatusError is a non-200 reply from an upstream endpoint.
type StatusError struct {
	Provider string
	Status   int
	Body     string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("%s: HTTP %d: %s", e.Provider, e.Status, e.Body)
}

// Retryable reports whether another attempt (or another provider) may succeed.
func (e *StatusError) Retryable() bool {
	return e.Status == http.StatusTooManyRequests || e.Status >= 500
}

func (c OpenAICompat) Complete(ctx context.Context, req Request) (out Response, err error) {
	msgs := req.Messages
	if req.System != "" {
		msgs = append([]Message{{Role: "system", Content: req.System}}, msgs...)
	}
	body := chatRequest{Model: req.Model, Messages: msgs, MaxTokens: req.MaxTokens, Temperature: req.Temperature}
	if body.Model == "" {
		body.Model = c.Models[req.Tier]
	}
	if body.Model == "" {
		body.Model = c.Models[TierStrong]
	}
	if req.JSON {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.base(ctx)+"/chat/completions", bytes.NewReader(b))
	if err != nil {
		return Response{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	if key := c.key(ctx); key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 120 * time.Second}
	}
	scope := calllog.FromContext(ctx)
	if req.UserID != nil {
		scope.UserID = req.UserID
	}
	if req.TaskID != nil {
		scope.TaskID = req.TaskID
	}
	if req.Purpose != "" {
		scope.Purpose = req.Purpose
	}
	if req.Origin != "" {
		scope.Origin = req.Origin
	}
	trace, err := c.Calls.Start(ctx, calllog.Begin{GroupID: req.CallGroupID, Attempt: req.Attempt, Scope: scope,
		Kind: "model", Provider: c.Name, Model: body.Model, Dispatched: true}, hc.Timeout)
	if err != nil {
		return Response{}, err
	}
	httpStatus := 0
	requestID := ""
	defer func() {
		status, code := calllog.Failure(err, httpStatus)
		if err == nil && out.FinishReason == "length" {
			status, code = "failed", "output_truncated"
		}
		if requestID == "" && out.RequestID != "" {
			requestID = out.RequestID
		}
		trace.Finish(ctx, calllog.End{Status: status, ResponseModel: out.Model, ErrorCode: code, RequestID: requestID, HTTPStatus: httpStatus,
			UsageKnown: out.UsageKnown, InputTokens: out.Usage.InputTokens, OutputTokens: out.Usage.OutputTokens, CachedTokens: out.Usage.CachedTokens})
	}()
	resp, err := hc.Do(hreq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	httpStatus = resp.StatusCode
	requestID = resp.Header.Get("X-Request-Id")
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if readErr != nil {
		return Response{}, readErr
	}
	if resp.StatusCode != http.StatusOK {
		return Response{}, &StatusError{Provider: c.Name, Status: resp.StatusCode, Body: string(bytes.TrimSpace(raw[:min(len(raw), 512)]))}
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Response{}, err
	}
	out = Response{Provider: c.Name, Model: cr.Model, RequestID: cr.ID, UsageKnown: cr.Usage != nil}
	if cr.Usage != nil {
		out.Usage = Usage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens, CachedTokens: cr.Usage.PromptTokensDetails.CachedTokens}
	}
	if len(cr.Choices) == 0 {
		return out, fmt.Errorf("%s: empty choices", c.Name)
	}
	out.Content = cr.Choices[0].Message.Content
	out.FinishReason = cr.Choices[0].FinishReason
	return out, nil
}

func (c OpenAICompat) key(ctx context.Context) string {
	if c.KeyFunc != nil {
		return c.KeyFunc(ctx)
	}
	return c.APIKey
}

func (c OpenAICompat) base(ctx context.Context) string {
	if c.BaseURLFunc != nil {
		if u := c.BaseURLFunc(ctx); u != "" {
			return u
		}
	}
	return c.BaseURL
}

// ListModels returns the model ids the endpoint offers this key (GET /models).
func (c OpenAICompat) ListModels(ctx context.Context) ([]string, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base(ctx)+"/models", nil)
	if err != nil {
		return nil, err
	}
	if key := c.key(ctx); key != "" {
		hreq.Header.Set("Authorization", "Bearer "+key)
	}
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	resp, err := hc.Do(hreq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		return nil, &StatusError{Provider: c.Name, Status: resp.StatusCode, Body: string(b)}
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, fmt.Errorf("%s: models: %w", c.Name, err)
	}
	ids := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}
