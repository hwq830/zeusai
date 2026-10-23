// Package testdb gives each test a fresh, migrated PostgreSQL database.
//
// Tests that need a database are skipped unless ZEUS_TEST_DATABASE_URL points at a server
// where the role may CREATE DATABASE, e.g. postgres://zeus:zeus@localhost:5432/postgres.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hwq830/zeusai/server/internal/platform/pg"
)

const envURL = "ZEUS_TEST_DATABASE_URL"

// New creates a database, migrates it and drops it when the test ends.
func New(t testing.TB) *pgxpool.Pool {
	t.Helper()
	admin := os.Getenv(envURL)
	if admin == "" {
		t.Skipf("%s not set; skipping database test", envURL)
	}
	ctx := context.Background()

	b := make([]byte, 6)
	_, _ = rand.Read(b)
	name := "zeus_test_" + hex.EncodeToString(b)

	conn, err := pgx.Connect(ctx, admin)
	if err != nil {
		t.Fatalf("connect admin db: %v", err)
	}
	if _, err := conn.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test db: %v", err)
	}
	_ = conn.Close(ctx)

	u, err := url.Parse(admin)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	dbURL := u.String()

	if err := pg.Migrate(ctx, dbURL); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	pool, err := pg.Connect(ctx, dbURL)
	if err != nil {
		t.Fatalf("connect test db: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		conn, err := pgx.Connect(ctx, admin)
		if err != nil {
			return
		}
		defer conn.Close(ctx)
		_, _ = conn.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	return pool
}
