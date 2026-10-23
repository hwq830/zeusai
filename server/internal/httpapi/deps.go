package httpapi

import (
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/hwq830/zeusai/server/internal/auth"
	"github.com/hwq830/zeusai/server/internal/briefing"
	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/llm"
	"github.com/hwq830/zeusai/server/internal/moderation"
	"github.com/hwq830/zeusai/server/internal/orch"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/report"
	"github.com/hwq830/zeusai/server/internal/task"
	"github.com/hwq830/zeusai/server/internal/template"
)

type Deps struct {
	Log    *slog.Logger
	Pool   *pgxpool.Pool
	Redis  *redis.Client
	Orch   orch.Client
	LLM    llm.Provider
	Mod    moderation.Checker // nil allows everything (dev)
	Config config.Config
	Now    func() time.Time
}

// New wires the domain services. Identity providers are the dev fakes until the Aliyun,
// Apple and WeChat accounts exist; config.Load refuses dev settings outside dev.
func New(d Deps) *Server {
	q := db.New(d.Pool)
	tokens := auth.NewTokens(d.Config.JWTSecret, d.Config.AccessTTL)
	qs := &quota.Service{Q: q, Now: d.Now}
	tpl := &template.Service{Q: q, LLM: d.LLM, Mod: d.Mod}
	return &Server{
		Log: d.Log, Q: q, Tokens: tokens, Quota: qs, Templates: tpl,
		Auth: &auth.Service{
			Q: q, Redis: d.Redis, Tokens: tokens, RefreshTTL: d.Config.RefreshTTL,
			SMS: auth.LogSMSSender{Log: d.Log}, Apple: auth.DevApple{}, Wechat: auth.DevWechat{}, OneClick: auth.DevOneClick{},
			FixedSMSCode: d.Config.DevSMSCode,
		},
		Briefings:  &briefing.Service{Q: q, Quota: qs, Templates: tpl, Orch: d.Orch, LLM: d.LLM, Mod: d.Mod, Now: d.Now},
		Tasks:      &task.Service{Q: q, Orch: d.Orch},
		Connectors: &connector.Service{Q: q},
		Reports:    &report.Service{Q: q, Quota: qs, Orch: d.Orch, Mod: d.Mod, PublicBaseURL: d.Config.PublicBaseURL},
		Bus:        &events.Bus{R: d.Redis},
	}
}
