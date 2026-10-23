// Package providers builds the model and moderation stack from config, shared by the api
// and orchestrator commands.
package providers

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"net/url"
	"os"

	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/browse"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/pdf"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/search"
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

func Search(cfg config.Config) (search.Searcher, error) {
	switch cfg.SearchProvider {
	case "", "mock":
		return search.Mock{}, nil
	case "bocha":
		return search.Bocha{APIKey: cfg.BochaAPIKey}, nil
	default:
		return nil, fmt.Errorf("unknown ZEUS_SEARCH_PROVIDER %q", cfg.SearchProvider)
	}
}

// Browse returns the page reader, or nil with mock search: fake result URLs do not resolve.
func Browse(cfg config.Config) (browse.Reader, error) {
	if cfg.SearchProvider == "" || cfg.SearchProvider == "mock" {
		return nil, nil
	}
	h := &browse.HTTP{}
	if cfg.EgressProxy != "" {
		u, err := url.Parse(cfg.EgressProxy)
		if err != nil {
			return nil, fmt.Errorf("ZEUS_EGRESS_PROXY: %w", err)
		}
		h.Proxy = u
	}
	return h, nil
}

// Blob returns the file store, or nil when none is configured (PDF export then answers
// "coming soon").
func Blob(cfg config.Config) blob.Store {
	if cfg.BlobDir == "" {
		return nil
	}
	secret := []byte(cfg.FilesSecret)
	if len(secret) == 0 {
		m := hmac.New(sha256.New, []byte(cfg.JWTSecret))
		m.Write([]byte("zeus files"))
		secret = m.Sum(nil)
	}
	return &blob.Local{Dir: cfg.BlobDir, BaseURL: cfg.PublicBaseURL, Secret: secret}
}

// PDF returns the printer, or nil when Chromium is not configured.
func PDF(cfg config.Config) pdf.Renderer {
	if cfg.ChromiumPath == "" {
		return nil
	}
	return pdf.Chromium{Path: cfg.ChromiumPath, NoSandbox: cfg.ChromiumNoSandbox}
}
