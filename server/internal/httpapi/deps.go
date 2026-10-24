package httpapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/blob"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
	"github.com/hwq830/zeusai/server/internal/report"
	"github.com/hwq830/zeusai/server/internal/task"
	"github.com/hwq830/zeusai/server/internal/template"
)

type Deps struct {
	Log   *slog.Logger
	Pool  *pgxpool.Pool
	Redis *redis.Client
	Orch  orch.Client
	LLM   llm.Provider
	Mod   moderation.Checker // nil allows everything (dev)
	Blob  blob.Store         // nil: PDF export unavailable
	// Identity providers; nil fields fall back to the dev fakes (tests).
	SMS      auth.SMSSender
	Apple    auth.AppleVerifier
	Wechat   auth.WechatExchanger
	OneClick auth.OneClickVerifier
	Config   config.Config
	Now      func() time.Time
}

// New wires the domain services.
func New(d Deps) *Server {
	if d.SMS == nil {
		d.SMS = auth.LogSMSSender{Log: d.Log}
	}
	if d.Apple == nil {
		d.Apple = auth.DevApple{}
	}
	if d.Wechat == nil {
		d.Wechat = auth.DevWechat{}
	}
	if d.OneClick == nil {
		d.OneClick = auth.DevOneClick{}
	}
	q := db.New(d.Pool)
	tokens := auth.NewTokens(d.Config.JWTSecret, d.Config.AccessTTL)
	rc := &remoteconfig.Cache{Q: q, Redis: d.Redis, Log: d.Log, Region: d.Config.Region}
	qs := &quota.Service{Q: q, Config: rc, Now: d.Now}
	tpl := &template.Service{Q: q, LLM: d.LLM, Mod: d.Mod}
	return &Server{
		Log: d.Log, Q: q, Tokens: tokens, Quota: qs, Templates: tpl,
		Auth: &auth.Service{
			Q: q, Redis: d.Redis, Tokens: tokens, RefreshTTL: d.Config.RefreshTTL,
			SMS: d.SMS, Apple: d.Apple, Wechat: d.Wechat, OneClick: d.OneClick,
			FixedSMSCode: d.Config.DevSMSCode,
		},
		Briefings:    &briefing.Service{Q: q, Quota: qs, Templates: tpl, Orch: d.Orch, LLM: d.LLM, Mod: d.Mod, Now: d.Now},
		Tasks:        &task.Service{Q: q, Orch: d.Orch},
		Connectors:   &connector.Service{Q: q},
		Reports:      &report.Service{Q: q, Quota: qs, Orch: d.Orch, Mod: d.Mod, Blob: d.Blob, Config: rc, PublicBaseURL: d.Config.PublicBaseURL},
		Bus:          &events.Bus{R: d.Redis},
		RemoteConfig: rc,
		Region:       d.Config.Region,
		Files:        files(d.Blob),
	}
}

func files(b blob.Store) http.Handler {
	if l, ok := b.(*blob.Local); ok {
		return l.Handler()
	}
	return nil
}
