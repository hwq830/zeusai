// Package llm is the orchestrator's view of the model gateway: one Complete call per model
// invocation. Implementations: Mock (tests and local dev) and OpenAICompat (any
// OpenAI-compatible endpoint, which is what modelgw and the domestic vendors expose).
package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/billing"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/moderation"
)

type Tier string

const (
	// TierStrong plans and writes; TierFast summarises, extracts and classifies.
	TierStrong Tier = "strong"
	TierFast   Tier = "fast"
)

type Message struct {
	Role    string `json:"role"` // system | user | assistant
	Content string `json:"content"`
}

type Request struct {
	// Purpose names the call site ("briefing.section"); used for metering, routing and mocks.
	Purpose string
	Tier    Tier
	// Model, when set, names the exact upstream model (the gateway sets it from console
	// overrides); otherwise the provider's model for Tier is used.
	Model     string
	System    string
	Messages  []Message
	JSON      bool
	MaxTokens int
	UserID    *uuid.UUID
	TaskID    *uuid.UUID
}

type Usage struct {
	InputTokens, OutputTokens, CachedTokens int
}

type Response struct {
	Content  string
	Provider string
	Model    string
	Usage    Usage
	// CostMicros is in 1e-6 CNY, filled in by the gateway when known.
	CostMicros int64
}

type Provider interface {
	Complete(ctx context.Context, req Request) (Response, error)
}

// CompleteJSON calls p and decodes the reply into out, tolerating ```json fences.
func CompleteJSON(ctx context.Context, p Provider, req Request, out any) (Response, error) {
	req.JSON = true
	resp, err := p.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	s := strings.TrimSpace(resp.Content)
	s = strings.TrimPrefix(s, "```json")
	s = strings.TrimPrefix(s, "```")
	s = strings.TrimSuffix(s, "```")
	if i := strings.IndexByte(s, '{'); i > 0 {
		s = s[i:]
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(s)), out); err != nil {
		return resp, fmt.Errorf("%s: model returned invalid JSON: %w", req.Purpose, err)
	}
	return resp, nil
}

// Metered records token usage per user and task after every successful call, including the
// billed tokens the free quota is metered in.
type Metered struct {
	Provider
	Q *db.Queries
	// Billing converts raw tokens into billed tokens and caps each task; nil bills every
	// token at weight 1 with no cap.
	Billing *billing.Policy
}

// ErrTaskBudget is returned instead of calling the model once a task has used its cap. The
// task then fails, and failed tasks are not charged to the user's quota.
var ErrTaskBudget = errors.New("task exceeded its token budget")

func (m Metered) Complete(ctx context.Context, req Request) (Response, error) {
	if req.UserID != nil {
		ctx = moderation.WithUser(ctx, *req.UserID) // attributes output blocks in the moderation log
	}
	if limit := m.Billing.TaskCap(req.UserID); limit > 0 && req.TaskID != nil {
		used, err := m.Q.SumTaskBilledTokens(ctx, req.TaskID)
		if err != nil {
			return Response{}, err
		}
		if used >= limit {
			return Response{}, ErrTaskBudget
		}
	}
	resp, err := m.Provider.Complete(ctx, req)
	if err != nil {
		return resp, err
	}
	_ = m.Q.RecordModelUsage(ctx, db.RecordModelUsageParams{
		UserID: req.UserID, TaskID: req.TaskID, Provider: resp.Provider, Model: resp.Model, Tier: string(req.Tier),
		InputTokens: int32(resp.Usage.InputTokens), OutputTokens: int32(resp.Usage.OutputTokens),
		CachedTokens: int32(resp.Usage.CachedTokens), CostMicros: resp.CostMicros,
		BilledTokens: m.Billing.Weights(req.UserID).Tokens(resp.Model, resp.Usage.InputTokens, resp.Usage.OutputTokens, resp.Usage.CachedTokens),
	})
	return resp, nil
}
