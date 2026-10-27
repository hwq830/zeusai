// Package providers builds the model and moderation stack from config, shared by the api
// and orchestrator commands.
package providers

import (
	"crypto/hmac"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strings"

	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/billing"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/browse"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/modelgw"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/notify"
	"github.com/hwq830/zeusai/server/internal/pdf"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
	"github.com/hwq830/zeusai/server/internal/search"
)

// Moderation returns the input/output checker: the keyword file (if any) plus the terms
// operators add in the admin console, with every block logged for review.
func Moderation(cfg config.Config, q *db.Queries, log *slog.Logger) (moderation.Checker, error) {
	chain := moderation.Chain{}
	if cfg.ModerationKeywordsFile != "" {
		f, err := os.Open(cfg.ModerationKeywordsFile)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		kw, err := moderation.NewKeywords(f)
		if err != nil {
			return nil, err
		}
		chain = append(chain, kw)
	}
	chain = append(chain, &moderation.Terms{Q: q, Log: log})
	return moderation.Recorder{Checker: chain, Q: q, Log: log}, nil
}

// LLM returns the metered model provider; rc supplies the billed-token weights (nil: weight 1).
func LLM(cfg config.Config, q *db.Queries, mod moderation.Checker, rc *remoteconfig.Cache) (llm.Provider, error) {
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
	return llm.Metered{Provider: p, Q: q, Billing: &billing.Policy{Config: rc}}, nil
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

type Identity struct {
	SMS      auth.SMSSender
	Apple    auth.AppleVerifier
	Wechat   auth.WechatExchanger
	OneClick auth.OneClickVerifier
}

// Identities picks a real provider wherever its settings exist.
func Identities(cfg config.Config, log *slog.Logger) Identity {
	var id Identity
	if cfg.IsDev() {
		id = Identity{SMS: auth.LogSMSSender{Log: log}, Apple: auth.DevApple{}, Wechat: auth.DevWechat{}, OneClick: auth.DevOneClick{}}
	} else {
		u := auth.Unconfigured{}
		id = Identity{SMS: u, Apple: u, Wechat: u, OneClick: u}
	}
	rpc := auth.AliyunRPC{AccessKeyID: cfg.AliyunAccessKeyID, AccessKeySecret: cfg.AliyunAccessKeySecret}
	hasAliyun := cfg.AliyunAccessKeyID != "" && cfg.AliyunAccessKeySecret != ""
	if hasAliyun && cfg.SMSSignName != "" && cfg.SMSTemplateCode != "" {
		id.SMS = auth.AliyunSMS{RPC: rpc, SignName: cfg.SMSSignName, TemplateCode: cfg.SMSTemplateCode}
	}
	if hasAliyun && cfg.OneClickEnabled {
		id.OneClick = auth.AliyunOneClick{RPC: rpc}
	}
	if cfg.AppleClientIDs != "" {
		var ids []string
		for _, s := range strings.Split(cfg.AppleClientIDs, ",") {
			if s = strings.TrimSpace(s); s != "" {
				ids = append(ids, s)
			}
		}
		id.Apple = &auth.Apple{ClientIDs: ids}
	}
	if cfg.WechatAppID != "" && cfg.WechatSecret != "" {
		id.Wechat = auth.Wechat{AppID: cfg.WechatAppID, Secret: cfg.WechatSecret}
	}
	return id
}

// Push returns the configured platform senders (APNs for iOS, EMAS for Android); with none
// configured it logs instead.
func Push(cfg config.Config, q *db.Queries, log *slog.Logger) (push.Notifier, error) {
	var senders push.Multi
	if cfg.APNsKeyFile != "" {
		b, err := os.ReadFile(cfg.APNsKeyFile)
		if err != nil {
			return nil, err
		}
		key, err := push.ParseP8(b)
		if err != nil {
			return nil, err
		}
		if cfg.APNsKeyID == "" || cfg.APNsTeamID == "" || cfg.APNsTopic == "" {
			return nil, fmt.Errorf("ZEUS_APNS_KEY_ID, ZEUS_APNS_TEAM_ID and ZEUS_APNS_TOPIC are required with ZEUS_APNS_KEY_FILE")
		}
		senders = append(senders, &push.APNs{Q: q, KeyID: cfg.APNsKeyID, TeamID: cfg.APNsTeamID, Topic: cfg.APNsTopic, Key: key, Log: log,
			Hosts: map[string]string{"production": push.APNsProduction, "sandbox": push.APNsSandbox}})
	}
	if cfg.EMASAppKey != "" {
		if cfg.AliyunAccessKeyID == "" || cfg.AliyunAccessKeySecret == "" || cfg.EMASPopupActivity == "" {
			return nil, fmt.Errorf("ZEUS_ALIYUN_ACCESS_KEY_ID, ZEUS_ALIYUN_ACCESS_KEY_SECRET and ZEUS_EMAS_POPUP_ACTIVITY are required with ZEUS_EMAS_APP_KEY")
		}
		senders = append(senders, &push.EMAS{Q: q, AppKey: cfg.EMASAppKey, AccessKeyID: cfg.AliyunAccessKeyID,
			AccessKeySecret: cfg.AliyunAccessKeySecret, PopupActivity: cfg.EMASPopupActivity, Log: log})
	}
	switch len(senders) {
	case 0:
		return &push.Log{L: log}, nil
	case 1:
		return senders[0], nil
	}
	return senders, nil
}

// Notifier wraps the push sender in the message center: every notification is saved for the
// in-app inbox and pushed according to the user's preferences.
func Notifier(cfg config.Config, q *db.Queries, log *slog.Logger) (*notify.Service, error) {
	sender, err := Push(cfg, q, log)
	if err != nil {
		return nil, err
	}
	return &notify.Service{Q: q, Sender: sender, Log: log}, nil
}
