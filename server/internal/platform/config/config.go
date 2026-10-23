// Package config loads process configuration from ZEUS_* environment variables.
// Secrets come from the environment (synced from KMS by External Secrets), never from the repo.
package config

import (
	"errors"
	"strings"
	"time"

	"github.com/knadh/koanf/providers/env"
	"github.com/knadh/koanf/v2"
)

type Config struct {
	Env               string        `koanf:"env"` // dev | staging | prod
	HTTPAddr          string        `koanf:"http_addr"`
	PublicBaseURL     string        `koanf:"public_base_url"` // used for share links
	DatabaseURL       string        `koanf:"database_url"`
	RedisURL          string        `koanf:"redis_url"`
	TemporalHost      string        `koanf:"temporal_host"`
	TemporalNamespace string        `koanf:"temporal_namespace"`
	TaskQueue         string        `koanf:"task_queue"`
	JWTSecret         string        `koanf:"jwt_secret"`
	AccessTTL         time.Duration `koanf:"access_ttl"`
	RefreshTTL        time.Duration `koanf:"refresh_ttl"`
	DevSMSCode        string        `koanf:"dev_sms_code"`   // dev only: fixed SMS code
	LLMProvider       string        `koanf:"llm_provider"`   // mock | gateway
	ModelGWConfig     string        `koanf:"modelgw_config"` // YAML routing file for the gateway
	// ModerationKeywordsFile lists blocked terms, one per line. Required outside dev.
	ModerationKeywordsFile string `koanf:"moderation_keywords_file"`
	SearchProvider         string `koanf:"search_provider"` // mock | bocha
	BochaAPIKey            string `koanf:"bocha_api_key"`
	// EgressProxy is the outbound proxy for page reads (http://host:port); empty = direct.
	EgressProxy string `koanf:"egress_proxy"`
}

func defaults() Config {
	return Config{
		Env:               "dev",
		HTTPAddr:          ":8080",
		PublicBaseURL:     "http://localhost:8080",
		DatabaseURL:       "postgres://zeus:zeus@localhost:5432/zeus?sslmode=disable",
		RedisURL:          "redis://localhost:6379/0",
		TemporalHost:      "localhost:7233",
		TemporalNamespace: "default",
		TaskQueue:         "zeus-tasks",
		AccessTTL:         15 * time.Minute,
		RefreshTTL:        30 * 24 * time.Hour,
		LLMProvider:       "mock",
		SearchProvider:    "mock",
	}
}

// Load reads ZEUS_DATABASE_URL style variables over the defaults.
func Load() (Config, error) {
	k := koanf.New(".")
	if err := k.Load(env.Provider("ZEUS_", ".", func(s string) string {
		return strings.ToLower(strings.TrimPrefix(s, "ZEUS_"))
	}), nil); err != nil {
		return Config{}, err
	}
	cfg := defaults()
	if err := k.UnmarshalWithConf("", &cfg, koanf.UnmarshalConf{Tag: "koanf"}); err != nil {
		return Config{}, err
	}
	if cfg.JWTSecret == "" {
		if !cfg.IsDev() {
			return Config{}, errors.New("ZEUS_JWT_SECRET is required outside dev")
		}
		cfg.JWTSecret = "dev-only-insecure-secret"
	}
	if !cfg.IsDev() && (cfg.DevSMSCode != "" || cfg.LLMProvider == "mock" || cfg.SearchProvider == "mock") {
		return Config{}, errors.New("dev-only settings (DEV_SMS_CODE, mock LLM or search) are not allowed outside dev")
	}
	if cfg.SearchProvider == "bocha" && cfg.BochaAPIKey == "" {
		return Config{}, errors.New("ZEUS_BOCHA_API_KEY is required when ZEUS_SEARCH_PROVIDER=bocha")
	}
	if !cfg.IsDev() && cfg.ModerationKeywordsFile == "" {
		return Config{}, errors.New("ZEUS_MODERATION_KEYWORDS_FILE is required outside dev")
	}
	if cfg.LLMProvider == "gateway" && cfg.ModelGWConfig == "" {
		return Config{}, errors.New("ZEUS_MODELGW_CONFIG is required when ZEUS_LLM_PROVIDER=gateway")
	}
	return cfg, nil
}

func (c Config) IsDev() bool { return c.Env == "dev" }
