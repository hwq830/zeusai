// Package billing converts raw model tokens into the billed tokens the free quota is metered
// in. Users only ever see billed tokens; the per-model weights behind them are an operations
// decision kept in remote config (quota.token_weights), so the App never learns which model
// answered or what it cost.
package billing

import (
	"math"

	"github.com/google/uuid"

	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

// Weight multiplies each kind of raw token. Input applies to uncached input tokens only.
type Weight struct {
	Input  float64 `json:"input"`
	Output float64 `json:"output"`
	Cached float64 `json:"cached"`
}

// DefaultModel is the Table entry used for models without their own weights.
const DefaultModel = "default"

// Table holds weights by model name, as the gateway reports it.
type Table map[string]Weight

// Neutral bills every token at weight 1; it applies until operators publish weights.
var Neutral = Table{DefaultModel: {Input: 1, Output: 1, Cached: 1}}

func (t Table) weight(model string) Weight {
	if w, ok := t[model]; ok {
		return w
	}
	if w, ok := t[DefaultModel]; ok {
		return w
	}
	return Neutral[DefaultModel]
}

// Tokens is the billed tokens for one call. input includes cached tokens, as providers report it.
func (t Table) Tokens(model string, input, output, cached int) int64 {
	w := t.weight(model)
	uncached := input - cached
	if uncached < 0 {
		uncached = 0
	}
	v := w.Input*float64(uncached) + w.Cached*float64(cached) + w.Output*float64(output)
	if v <= 0 {
		return 0
	}
	return int64(math.Round(v))
}

// Policy reads the published weights and per-task cap; a nil Policy or cache uses Neutral
// weights and no cap.
type Policy struct {
	Config *remoteconfig.Cache
}

func userContext(userID *uuid.UUID) remoteconfig.Context {
	var ctx remoteconfig.Context
	if userID != nil {
		ctx.UserID = userID.String()
	}
	return ctx
}

// Weights returns the table that applies to a call made for userID (nil for system calls), so
// operators can target weights like any other key.
func (p *Policy) Weights(userID *uuid.UUID) Table {
	if p == nil || p.Config == nil {
		return Neutral
	}
	t := remoteconfig.Value(p.Config, userContext(userID), remoteconfig.KeyQuotaTokenWeights, Neutral)
	if len(t) == 0 {
		return Neutral
	}
	return t
}

// TaskCap is the most billed tokens one task may use; 0 means no cap.
func (p *Policy) TaskCap(userID *uuid.UUID) int64 {
	if p == nil || p.Config == nil {
		return 0
	}
	return int64(p.Config.Int(userContext(userID), remoteconfig.KeyQuotaTaskTokenCap, 0))
}
