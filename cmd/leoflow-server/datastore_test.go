package main

import (
	"context"
	"log/slog"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// TestSelectDatastoreEmbeddedWhenNoRedis pins the switch that broke the embedded
// edition: with no Redis URL configured, selectDatastore must pick the embedded
// backends (Postgres XCom + in-process tailer) and dial no Redis — not fall into
// the Redis branch (which a non-empty default redis.url once forced, crashing the
// server on a refused localhost:6379 dial). Guards ADR 0026.
func TestSelectDatastoreEmbeddedWhenNoRedis(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel() // stops the XCom sweep goroutine

	cfg := &config.ServerConfig{} // zero value: Redis.URL == ""
	pg := &storage.Postgres{}     // nil pool; the embedded path does not dial it here

	// Lite path takes the embedded branch BEFORE touching metrics, so a nil
	// Metrics is safe here (and lets the test avoid registering Prometheus).
	backend, tailer, redisHealth, cleanup, err := selectDatastore(ctx, cfg, pg, slog.Default(), nil)
	if err != nil {
		t.Fatalf("selectDatastore: %v", err)
	}
	defer cleanup()

	if redisHealth != nil {
		t.Error("embedded mode must register no Redis health check")
	}
	if _, ok := backend.(*xcom.PostgresBackend); !ok {
		t.Errorf("embedded backend = %T, want *xcom.PostgresBackend", backend)
	}
	if _, ok := tailer.(*logs.MemoryTailer); !ok {
		t.Errorf("embedded tailer = %T, want *logs.MemoryTailer", tailer)
	}
}
