package remoteconfig_test

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/hwq830/zeusai/server/internal/db"
	"github.com/hwq830/zeusai/server/internal/platform/testdb"
	"github.com/hwq830/zeusai/server/internal/platform/testenv"
	"github.com/hwq830/zeusai/server/internal/remoteconfig"
)

// A publish reaches a running cache through Redis well before the polling fallback.
func TestCacheFollowsPublishes(t *testing.T) {
	pool := testdb.New(t)
	rdb := testenv.Redis(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	q := db.New(pool)
	if _, err := remoteconfig.SeedBuiltins(ctx, q); err != nil {
		t.Fatal(err)
	}
	owner, err := q.CreateAdminUser(ctx, db.CreateAdminUserParams{Email: "o@zeus.test", PasswordHash: "x", Role: "owner"})
	if err != nil {
		t.Fatal(err)
	}
	cache := &remoteconfig.Cache{Q: q, Redis: rdb, Poll: time.Hour}
	if err := cache.Load(ctx); err != nil {
		t.Fatal(err)
	}
	go cache.Run(ctx)
	time.Sleep(100 * time.Millisecond) // let the subscription start

	rel := &remoteconfig.Releases{Pool: pool, Redis: rdb, RequireApproval: true}
	a := remoteconfig.Actor{ID: owner.ID, Role: "owner"}
	if _, _, err := rel.Submit(ctx, a, "v1"); err != nil {
		t.Fatal(err)
	}
	wait := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(3 * time.Second)
		for cache.Version() != want {
			if time.Now().After(deadline) {
				t.Fatalf("cache at version %d, want %d", cache.Version(), want)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	wait(1)
	if !cache.Bool(remoteconfig.Context{}, remoteconfig.KeyReportCreateEnabled, false) {
		t.Fatal("default not served")
	}
	if _, err := q.UpdateConfigKey(ctx, db.UpdateConfigKeyParams{Key: remoteconfig.KeyReportCreateEnabled, DefaultValue: json.RawMessage("false"), UpdatedBy: &owner.ID}); err != nil {
		t.Fatal(err)
	}
	r, _, err := rel.Submit(ctx, a, "kill")
	if err != nil || r.Status != "published" {
		t.Fatalf("kill switch publish = %+v %v", r, err)
	}
	wait(2)
	if cache.Bool(remoteconfig.Context{}, remoteconfig.KeyReportCreateEnabled, true) {
		t.Fatal("kill switch not applied")
	}
	// Unknown keys and wrong types fall back to the caller's default.
	if cache.Int(remoteconfig.Context{}, "no.such_key", 42) != 42 || cache.Int(remoteconfig.Context{}, remoteconfig.KeyReportCreateEnabled, 7) != 7 {
		t.Fatal("fallback to default failed")
	}
}
