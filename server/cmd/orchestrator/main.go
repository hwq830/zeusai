// Command orchestrator is the Temporal worker that runs briefing and report workflows.
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
	"go.temporal.io/sdk/worker"

	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/pg"
	"github.com/hwq830/zeusai/server/internal/platform/providers"
	"github.com/hwq830/zeusai/server/internal/push"
	"github.com/hwq830/zeusai/server/internal/workflows"
)

func main() {
	log := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	if err := run(log); err != nil {
		log.Error("orchestrator exited", "err", err)
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

	q := db.New(pool)
	mod, err := providers.Moderation(cfg)
	if err != nil {
		return err
	}
	model, err := providers.LLM(cfg, q, mod)
	if err != nil {
		return err
	}
	searcher, err := providers.Search(cfg)
	if err != nil {
		return err
	}
	reader, err := providers.Browse(cfg)
	if err != nil {
		return err
	}
	w := worker.New(tc, cfg.TaskQueue, worker.Options{})
	workflows.Register(w, &workflows.Activities{
		Q: q, Bus: &events.Bus{R: rdb}, LLM: model,
		Search: searcher, Browse: reader, Tools: &connector.Toolbox{Q: q},
		PDF: providers.PDF(cfg), Blob: providers.Blob(cfg), Push: &push.Log{L: log},
	})
	log.Info("orchestrator started", "queue", cfg.TaskQueue)
	return w.Run(worker.InterruptCh())
}
