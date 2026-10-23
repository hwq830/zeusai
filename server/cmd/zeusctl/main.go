// Command zeusctl runs one-off operations: `migrate` applies database migrations and `seed`
// loads official templates and the connector catalog. Both are idempotent.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/pg"
	"github.com/hwq830/zeusai/server/internal/template"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: zeusctl migrate|seed")
		os.Exit(2)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "zeusctl:", err)
		os.Exit(1)
	}
}

func run(cmd string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	ctx := context.Background()
	switch cmd {
	case "migrate":
		return pg.Migrate(ctx, cfg.DatabaseURL)
	case "seed":
		pool, err := pg.Connect(ctx, cfg.DatabaseURL)
		if err != nil {
			return err
		}
		defer pool.Close()
		q := db.New(pool)
		nt, err := (&template.Service{Q: q}).SeedOfficial(ctx)
		if err != nil {
			return err
		}
		nc, err := (&connector.Service{Q: q}).SeedCatalog(ctx)
		if err != nil {
			return err
		}
		fmt.Printf("seeded %d official templates, %d connectors\n", nt, nc)
		return nil
	}
	return fmt.Errorf("unknown command %q", cmd)
}
