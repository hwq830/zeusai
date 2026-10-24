// Command zeusctl runs one-off operations: `migrate` applies database migrations, `seed`
// loads official templates and the connector catalog (both idempotent), and
// `admin-create <email> [role]` creates an admin console account.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/hwq830/zeusai/server/internal/adminapi"
	"github.com/hwq830/zeusai/server/internal/connector"
	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/config"
	"github.com/hwq830/zeusai/server/internal/platform/pg"
	"github.com/hwq830/zeusai/server/internal/template"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: zeusctl migrate | seed | admin-create <email> [owner|operator|moderator|viewer]")
		os.Exit(2)
	}
	if err := run(os.Args[1], os.Args[2:]); err != nil {
		fmt.Fprintln(os.Stderr, "zeusctl:", err)
		os.Exit(1)
	}
}

func run(cmd string, args []string) error {
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
	case "admin-create":
		return adminCreate(ctx, cfg, args)
	}
	return fmt.Errorf("unknown command %q", cmd)
}

// adminCreate makes the first console account. The password comes from ZEUS_ADMIN_PASSWORD,
// or is generated and printed once.
func adminCreate(ctx context.Context, cfg config.Config, args []string) error {
	if len(args) < 1 {
		return fmt.Errorf("usage: zeusctl admin-create <email> [role]")
	}
	email, role := strings.ToLower(strings.TrimSpace(args[0])), "owner"
	if len(args) > 1 {
		role = args[1]
	}
	pw := os.Getenv("ZEUS_ADMIN_PASSWORD")
	generated := pw == ""
	if generated {
		pw = adminapi.NewPassword()
	}
	hash, err := adminapi.HashPassword(pw)
	if err != nil {
		return err
	}
	pool, err := pg.Connect(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()
	a, err := db.New(pool).CreateAdminUser(ctx, db.CreateAdminUserParams{Email: email, PasswordHash: hash, Role: role})
	if err != nil {
		return err
	}
	fmt.Printf("created admin %s (%s, id %s)\n", a.Email, a.Role, a.ID)
	if generated {
		fmt.Printf("password: %s\n(shown once; change it after the first login)\n", pw)
	}
	return nil
}
