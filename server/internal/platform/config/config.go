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
	Env               string        `koanf:"env"`    // dev | staging | prod
	Region            string        `koanf:"region"` // china | international; targeted by remote config rules
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
	SearchProvider         string `koanf:"search_provider"` // mock | console (bocha = console, kept for old .env files)
	BochaAPIKey            string `koanf:"bocha_api_key"`
	// SecretsKey seals provider API keys saved from the admin console (openssl rand -hex 32).
	// Empty disables saving keys there; keys then come only from the environment.
	SecretsKey string `koanf:"secrets_key"`
	// EgressProxy is the outbound proxy for page reads (http://host:port); empty = direct.
	EgressProxy string `koanf:"egress_proxy"`
	// BlobDir stores generated files on local disk (dev, single host); empty disables PDF.
	// Shared by api (serves signed links) and orchestrator (writes). OSS replaces it in prod.
	BlobDir     string `koanf:"blob_dir"`
	FilesSecret string `koanf:"files_secret"` // signs /files links; defaults to one derived from JWT_SECRET
	// ChromiumPath enables PDF printing in the orchestrator.
	ChromiumPath      string `koanf:"chromium_path"`
	ChromiumNoSandbox bool   `koanf:"chromium_no_sandbox"`

	// Identity and push providers. Each one is used when its settings are present; otherwise
	// dev gets the fakes and other environments answer "not available yet".
	AliyunAccessKeyID     string `koanf:"aliyun_access_key_id"`
	AliyunAccessKeySecret string `koanf:"aliyun_access_key_secret"`
	SMSSignName           string `koanf:"sms_sign_name"`
	SMSTemplateCode       string `koanf:"sms_template_code"`
	OneClickEnabled       bool   `koanf:"oneclick_enabled"` // Aliyun 号码认证, same AccessKey
	GoogleClientIDs       string `koanf:"google_client_ids"`
	SMTPHost              string `koanf:"smtp_host"`
	SMTPPort              int    `koanf:"smtp_port"`
	SMTPUsername          string `koanf:"smtp_username"`
	SMTPPassword          string `koanf:"smtp_password"`
	SMTPFrom              string `koanf:"smtp_from"`
	DevEmailCode          string `koanf:"dev_email_code"`
	AppleClientIDs        string `koanf:"apple_client_ids"` // comma-separated audiences
	WechatAppID           string `koanf:"wechat_app_id"`
	WechatSecret          string `koanf:"wechat_secret"`
	APNsKeyFile           string `koanf:"apns_key_file"` // .p8
	APNsKeyID             string `koanf:"apns_key_id"`
	APNsTeamID            string `koanf:"apns_team_id"`
	APNsTopic             string `koanf:"apns_topic"` // bundle id
	// Aliyun Mobile Push (EMAS) for Android, signed with the Aliyun AccessKey above.
	EMASAppKey        string `koanf:"emas_app_key"`
	EMASPopupActivity string `koanf:"emas_popup_activity"` // vendor-channel popup Activity class

	// Admin console process (server/cmd/admin). Keep it on an internal address.
	AdminHTTPAddr  string        `koanf:"admin_http_addr"`
	AdminJWTSecret string        `koanf:"admin_jwt_secret"` // separate from JWT_SECRET; required outside dev
	AdminTokenTTL  time.Duration `koanf:"admin_token_ttl"`
	// AdminStaticDir serves the built console (zeusai-admin dist/) next to /admin/v1; optional.
	AdminStaticDir string `koanf:"admin_static_dir"`
}

func defaults() Config {
	return Config{
		Env:               "dev",
		Region:            "china",
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
		AdminHTTPAddr:     ":8081",
		AdminTokenTTL:     12 * time.Hour,
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
	if cfg.AdminJWTSecret == "" && cfg.IsDev() {
		cfg.AdminJWTSecret = "dev-only-insecure-admin-secret"
	}
	if !cfg.IsDev() && (cfg.DevSMSCode != "" || cfg.DevEmailCode != "" || cfg.LLMProvider == "mock" || cfg.SearchProvider == "mock") {
		return Config{}, errors.New("dev-only settings (DEV_SMS_CODE, mock LLM or search) are not allowed outside dev")
	}
	if cfg.SearchProvider != "" && cfg.SearchProvider != "mock" && cfg.BochaAPIKey == "" && cfg.SecretsKey == "" {
		return Config{}, errors.New("ZEUS_SECRETS_KEY (to set search keys in the admin console) or ZEUS_BOCHA_API_KEY is required when ZEUS_SEARCH_PROVIDER is not mock")
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
