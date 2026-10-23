// Package providers builds the model and moderation stack from config, shared by the api
// and orchestrator commands.
package providers

import (
	"fmt"
	"os"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/platform/config"
)

// Moderation returns the input/output checker, or nil (allow all) in dev without a list.
func Moderation(cfg config.Config) (moderation.Checker, error) {
	if cfg.ModerationKeywordsFile == "" {
		return nil, nil
	}
	f, err := os.Open(cfg.ModerationKeywordsFile)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return moderation.NewKeywords(f)
}

// LLM returns the metered model provider.
func LLM(cfg config.Config, q *db.Queries, mod moderation.Checker) (llm.Provider, error) {
	var p llm.Provider
	switch cfg.LLMProvider {
	case "", "mock":
		p = llm.Mock{}
	case "gateway":
		gc, err := modelgw.LoadConfig(cfg.ModelGWConfig)
		if err != nil {
			return nil, err
		}
		gw, err := modelgw.New(gc, mod, nil)
		if err != nil {
			return nil, err
		}
		p = gw
	default:
		return nil, fmt.Errorf("unknown ZEUS_LLM_PROVIDER %q", cfg.LLMProvider)
	}
	return llm.Metered{Provider: p, Q: q}, nil
}
