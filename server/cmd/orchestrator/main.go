// Command orchestrator is the Temporal worker that runs briefing and report workflows.
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
	"go.temporal.io/sdk/worker"

	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/events"
	"github.com/hwq830/zeusai/server/internal/pagetemplate"
	"github.com/hwq830/zeusai/server/internal/pdf"
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
	pageTemplates := pagetemplate.New(q, pool, nil, true)
	if err := pagetemplate.EnsureBuiltin(ctx, q); err != nil {
		log.Warn("page template bootstrap failed", "err", err)
	}
	reporttemplate.SetProvider(pageTemplates)
	mod, err := providers.Moderation(cfg, q, log)
	if err != nil {
		return err
	}
	// The orchestrator reads remote config only for billed-token weights.
	rc := &remoteconfig.Cache{Q: q, Redis: rdb, Log: log, Region: cfg.Region}
	if err := rc.Load(ctx); err != nil {
		log.Warn("remote config not loaded yet", "err", err)
	}
	go rc.Run(ctx)
	keys, err := providers.Keys(cfg, q, log)
	if err != nil {
		return err
	}
	model, err := providers.LLM(cfg, q, mod, rc, keys)
	if err != nil {
		return err
	}
	searcher, err := providers.Search(cfg, q, keys, log)
	if err != nil {
		return err
	}
	reader, err := providers.Browse(cfg)
	if err != nil {
		return err
	}
	notifier, err := providers.Notifier(cfg, q, log)
	if err != nil {
		return err
	}
	w := worker.New(tc, cfg.TaskQueue, worker.Options{})
	workflows.Register(w, &workflows.Activities{
		Q: q, Bus: &events.Bus{R: rdb}, LLM: model,
		Search: searcher, Browse: reader, Tools: &connector.Toolbox{Q: q},
		PDF: providers.PDF(cfg), Blob: providers.Blob(cfg), Push: notifier, Notify: notifier,
		Quota: &quota.Service{Q: q, Config: rc},
	})
	// Internal page-template verification: layout at 390px plus one PDF print. The admin
	// process calls this before publishing; it is not exposed publicly.
	if cfg.PageTemplateVerifyAddr != "" {
		verifier := pagetemplate.ChromiumVerifier{Chromium: pdf.Chromium{
			Path: cfg.ChromiumPath, NoSandbox: cfg.ChromiumNoSandbox, Timeout: pagetemplate.VerifyTimeout,
		}}
		mux := http.NewServeMux()
		mux.Handle("/internal/page-template-verify", pagetemplate.VerifyHandler(cfg.PageTemplateVerifyToken, q, verifier))
		verifyServer := &http.Server{Addr: cfg.PageTemplateVerifyAddr, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
		go func() {
			if err := verifyServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
				log.Error("page template verify server failed", "addr", cfg.PageTemplateVerifyAddr, "err", err)
			}
		}()
		go func() {
			<-ctx.Done()
			sctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			_ = verifyServer.Shutdown(sctx)
		}()
		log.Info("page template verify listening", "addr", cfg.PageTemplateVerifyAddr)
	}

	// Quiet-hours pushes are sent when the window ends.
	go notifier.Run(ctx, time.Minute)
	log.Info("orchestrator started", "queue", cfg.TaskQueue)
	return w.Run(worker.InterruptCh())
}
