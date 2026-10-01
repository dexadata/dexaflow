//go:build integration

package storage_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

func TestPostgresAndRedisConnectivity(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	redisURL := os.Getenv("REDIS_URL")
	if dbURL == "" || redisURL == "" {
		t.Skip("DATABASE_URL and REDIS_URL must be set for the integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: dbURL})
	if err != nil {
		t.Fatalf("NewPostgres: %v", err)
	}
	defer pg.Close()
	if err := pg.Ping(ctx); err != nil {
		t.Errorf("postgres ping: %v", err)
	}

	rd, err := storage.NewRedis(ctx, config.RedisSection{URL: redisURL})
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	defer rd.Close()
	if err := rd.Ping(ctx); err != nil {
		t.Errorf("redis ping: %v", err)
	}
}
