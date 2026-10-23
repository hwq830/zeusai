// Package testenv connects tests to Redis and Temporal when ZEUS_TEST_REDIS_URL and
// ZEUS_TEST_TEMPORAL_HOST are set (CI and local dev), and skips them otherwise.
package testenv

import (
	"context"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"
	"go.temporal.io/sdk/client"
)

func Redis(t testing.TB) *redis.Client {
	t.Helper()
	url := os.Getenv("ZEUS_TEST_REDIS_URL")
	if url == "" {
		t.Skip("ZEUS_TEST_REDIS_URL not set; skipping")
	}
	opt, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opt)
	if err := c.Ping(context.Background()).Err(); err != nil {
		t.Fatalf("redis: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}

func Temporal(t testing.TB) client.Client {
	t.Helper()
	host := os.Getenv("ZEUS_TEST_TEMPORAL_HOST")
	if host == "" {
		t.Skip("ZEUS_TEST_TEMPORAL_HOST not set; skipping")
	}
	c, err := client.Dial(client.Options{HostPort: host})
	if err != nil {
		t.Fatalf("temporal: %v", err)
	}
	t.Cleanup(c.Close)
	return c
}
