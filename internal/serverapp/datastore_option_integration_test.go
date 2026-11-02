//go:build integration

package serverapp

import (
	"context"
	"log/slog"
	"os"
	"testing"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// Against a real Redis: an XCom backend passed as an option replaces the
// Redis store, while Redis still serves the live log tail and its health
// check (ADR 0099 §3).
func TestSelectDatastoreOptionBackendKeepsTheRedisTailer(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL must be set for the integration test")
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	cfg := &config.ServerConfig{}
	cfg.Redis.URL = redisURL

	custom := &optionBackend{}
	backend, tailer, redisHealth, cleanup, err := selectDatastore(ctx, cfg, &storage.Postgres{}, custom, slog.Default(), observability.NewMetrics(prometheus.NewRegistry()))
	if err != nil {
		t.Fatalf("selectDatastore: %v", err)
	}
	defer cleanup()
	if backend != custom {
		t.Errorf("backend = %T, want the option's backend", backend)
	}
	if _, ok := tailer.(*logs.RedisTailer); !ok {
		t.Errorf("tailer = %T, want the Redis tailer", tailer)
	}
	if redisHealth == nil {
		t.Error("Redis is configured, so its health check must be registered")
	}
}
