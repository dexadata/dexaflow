package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func TestPoolConfigAppliesPoolSizes(t *testing.T) {
	pc, err := poolConfig(config.DatabaseSection{
		URL:          "postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable",
		MaxOpenConns: 25,
		MaxIdleConns: 5,
	})
	if err != nil {
		t.Fatalf("poolConfig: %v", err)
	}
	if pc.MaxConns != 25 {
		t.Errorf("MaxConns = %d, want 25", pc.MaxConns)
	}
	if pc.MinConns != 5 {
		t.Errorf("MinConns = %d, want 5", pc.MinConns)
	}
}

func TestPoolConfigInvalidURL(t *testing.T) {
	if _, err := poolConfig(config.DatabaseSection{URL: "://bad"}); err == nil {
		t.Error("expected error for invalid database url")
	}
}

func TestRedisOptionsParsesAddr(t *testing.T) {
	opts, err := redisOptions("redis://localhost:6379/0", "")
	if err != nil {
		t.Fatalf("redisOptions: %v", err)
	}
	if opts.Addr != "localhost:6379" {
		t.Errorf("Addr = %q, want localhost:6379", opts.Addr)
	}
	if opts.DB != 0 {
		t.Errorf("DB = %d, want 0", opts.DB)
	}
}

func TestRedisOptionsInvalidURL(t *testing.T) {
	if _, err := redisOptions("not-a-redis-url", ""); err == nil {
		t.Error("expected error for invalid redis url")
	}
}
