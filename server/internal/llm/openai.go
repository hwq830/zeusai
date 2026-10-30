package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// OpenAICompat calls an OpenAI-compatible /chat/completions endpoint. Models maps a tier to
// the model name on that endpoint.
type OpenAICompat struct {
	BaseURL string
	APIKey  string
	// KeyFunc, when set, supplies the key per call instead of APIKey (keys switched in the
	// admin console take effect without a restart).
	KeyFunc func(context.Context) string
	Name    string
	Models  map[Tier]string
	HTTP    *http.Client
}

type chatRequest struct {
	Model          string    `json:"model"`
	Messages       []Message `json:"messages"`
	MaxTokens      int       `json:"max_tokens,omitempty"`
	ResponseFormat any       `json:"response_format,omitempty"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Choices []struct {
		Message Message `json:"message"`
	} `json:"choices"`
	Usage struct {
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

func (c OpenAICompat) Complete(ctx context.Context, req Request) (Response, error) {
	msgs := req.Messages
	if req.System != "" {
		msgs = append([]Message{{Role: "system", Content: req.System}}, msgs...)
	}
	body := chatRequest{Model: c.Models[req.Tier], Messages: msgs, MaxTokens: req.MaxTokens}
	if body.Model == "" {
		body.Model = c.Models[TierStrong]
	}
	if req.JSON {
		body.ResponseFormat = map[string]string{"type": "json_object"}
	}
	b, _ := json.Marshal(body)
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/chat/completions", bytes.NewReader(b))
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
	resp, err := hc.Do(hreq)
	if err != nil {
		return Response{}, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if resp.StatusCode != http.StatusOK {
		return Response{}, &StatusError{Provider: c.Name, Status: resp.StatusCode, Body: string(bytes.TrimSpace(raw[:min(len(raw), 512)]))}
	}
	var cr chatResponse
	if err := json.Unmarshal(raw, &cr); err != nil {
		return Response{}, err
	}
	if len(cr.Choices) == 0 {
		return Response{}, fmt.Errorf("%s: empty choices", c.Name)
	}
	return Response{
		Content: cr.Choices[0].Message.Content, Provider: c.Name, Model: cr.Model,
		Usage: Usage{InputTokens: cr.Usage.PromptTokens, OutputTokens: cr.Usage.CompletionTokens, CachedTokens: cr.Usage.PromptTokensDetails.CachedTokens},
	}, nil
}

func (c OpenAICompat) key(ctx context.Context) string {
	if c.KeyFunc != nil {
		return c.KeyFunc(ctx)
	}
	return c.APIKey
}
