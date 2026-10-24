// Command api serves the App-facing REST + SSE API.
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

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/httpapi"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/pg"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
	"github.com/hwq830/zeusai/server/internal/workflows"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("api exited", "err", err)
		os.Exit(1)
	}
}

func run(log *slog.Logger) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pool, err := pg.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	ropt, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return err
	}
	rdb := redis.NewClient(ropt)
	defer rdb.Close()
	tc, err := client.Dial(client.Options{HostPort: cfg.TemporalHost, Namespace: cfg.TemporalNamespace, Logger: log})
	if err != nil {
		return err
	}
	defer tc.Close()

	mod, err := providers.Moderation(cfg, db.New(pool), log)
	if err != nil {
		return err
	}
	model, err := providers.LLM(cfg, db.New(pool), mod)
	if err != nil {
		return err
	}
	ids := providers.Identities(cfg, log)
	srv := httpapi.New(httpapi.Deps{
		SMS: ids.SMS, Apple: ids.Apple, Wechat: ids.Wechat, OneClick: ids.OneClick,
		Log: log, Pool: pool, Redis: rdb, Orch: &workflows.Client{C: tc, TaskQueue: cfg.TaskQueue},
		LLM: model, Mod: mod, Blob: providers.Blob(cfg), Config: cfg,
	})
	// A failed first load is not fatal: the App falls back to its built-in defaults and the
	// cache retries every 30 seconds.
	if err := srv.RemoteConfig.Load(ctx); err != nil {
		log.Warn("remote config not loaded yet", "err", err)
	}
	go srv.RemoteConfig.Run(ctx)
	hs := &http.Server{Addr: cfg.HTTPAddr, Handler: srv.Handler(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		<-ctx.Done()
		sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = hs.Shutdown(sctx)
	}()
	log.Info("api listening", "addr", cfg.HTTPAddr, "env", cfg.Env)
	if err := hs.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
