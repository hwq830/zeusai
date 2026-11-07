// Command admin serves the admin console API (/admin/v1) and, optionally, the built console.
// Run it on an internal address only; it never shares a port with the App-facing api.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/pagetemplate"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/pg"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
	"github.com/hwq830/zeusai/server/internal/quota"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
	"github.com/hwq830/zeusai/server/internal/workflows"
	reporttemplate "github.com/hwq830/zeusai/web/report-template"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("admin exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if cfg.AdminJWTSecret == "" {
		return errors.New("ZEUS_ADMIN_JWT_SECRET is required outside dev")
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	tc, err := client.Dial(client.Options{HostPort: cfg.TemporalHost, Namespace: cfg.TemporalNamespace, Logger: log})
	if err != nil {
		return err
	}
	defer tc.Close()

	ropt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(ropt)
	defer rdb.Close()

	q := db.New(pool)
	pageTemplates := pagetemplate.New(q, pool, providers.PDF(cfg))
	if err := pagetemplate.EnsureBuiltin(ctx, q); err != nil {
		log.Warn("page template bootstrap failed", "err", err)
	}
	reporttemplate.SetProvider(pageTemplates)
	notifier, err := providers.Notifier(cfg, q, log)
	if err != nil {
		return err
	}
	if _, err := remoteconfig.SeedBuiltins(ctx, q); err != nil {
		return err
	}
	keys, err := providers.Keys(cfg, q, log)
	if err != nil {
		return err
	}
	rc := &remoteconfig.Cache{Q: q, Redis: rdb, Log: log, Region: cfg.Region}
	if err := rc.Load(ctx); err != nil {
		log.Warn("remote config not loaded yet", "err", err)
	}
	go rc.Run(ctx)
	srv := &adminapi.Server{
		Log: log, Q: q, Tokens: adminapi.NewTokens(cfg.AdminJWTSecret, cfg.AdminTokenTTL),
		Quota: &quota.Service{Q: q, Config: rc}, Orch: &workflows.Client{C: tc, TaskQueue: cfg.TaskQueue},
		Releases:    &remoteconfig.Releases{Pool: pool, Redis: rdb, Log: log, RequireApproval: cfg.Env == "prod"},
		Region:      cfg.Region,
		LLMProvider: cfg.LLMProvider, ModelGWConfig: cfg.ModelGWConfig, PublicBaseURL: cfg.PublicBaseURL,
		SearchProvider: cfg.SearchProvider, Keys: keys,
		StaticDir: cfg.AdminStaticDir, Blob: providers.Blob(cfg), Push: notifier, Notify: notifier,
		PageTemplates: pageTemplates,
	}
	hs := &http.Server{Addr: cfg.AdminHTTPAddr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("admin listening", "addr", cfg.AdminHTTPAddr, "env", cfg.Env)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
