//go:build integration

package serverapp

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// The boot path against a Redis set to volatile-lru (#2354): warn mode boots
// and sets the policy gauge to 0, strict mode (redis.require_noeviction)
// refuses to boot.
func TestSelectDatastoreRedisEvictionPolicyIntegration(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL must be set for the integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	admin := redis.NewClient(opts)
	defer func() { _ = admin.Close() }() //nolint:errcheck // test cleanup
	prev, err := admin.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		t.Fatalf("CONFIG GET: %v", err)
	}
	if serr := admin.ConfigSet(ctx, "maxmemory-policy", "volatile-lru").Err(); serr != nil {
		t.Fatalf("CONFIG SET: %v", serr)
	}
	defer func() {
		_ = admin.ConfigSet(context.Background(), "maxmemory-policy", prev["maxmemory-policy"]).Err() //nolint:errcheck // best effort restore
	}()

	cfg := &config.ServerConfig{}
	cfg.Redis.URL = redisURL
	metrics := observability.NewMetrics(prometheus.NewRegistry())
	_, _, _, cleanup, err := selectDatastore(ctx, cfg, &storage.Postgres{}, nil, slog.Default(), metrics)
	if err != nil {
		t.Fatalf("warn mode refused to boot: %v", err)
	}
	cleanup()
	var m dto.Metric
	if err := metrics.RedisEvictionPolicySafe.Write(&m); err != nil {
		t.Fatal(err)
	}
	if got := m.GetGauge().GetValue(); got != 0 {
		t.Errorf("dexaflow_redis_eviction_policy_safe = %v, want 0", got)
	}

	cfg.Redis.RequireNoEviction = true
	if _, _, _, _, err := selectDatastore(ctx, cfg, &storage.Postgres{}, nil, slog.Default(), observability.NewMetrics(prometheus.NewRegistry())); !errors.Is(err, storage.ErrRedisEvictionUnsafe) {
		t.Errorf("strict mode error = %v, want ErrRedisEvictionUnsafe", err)
	}
}
