//go:build integration

package storage

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// evictionTestRedis connects to the integration Redis and sets its
// maxmemory-policy to policy for the test, restoring the previous one after.
func evictionTestRedis(t *testing.T, policy string) *Redis {
	t.Helper()
	url := os.Getenv("REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	ctx := context.Background()
	if perr := c.Ping(ctx).Err(); perr != nil {
		t.Skipf("redis unavailable: %v", perr)
	}
	prev, err := c.ConfigGet(ctx, "maxmemory-policy").Result()
	if err != nil {
		t.Fatalf("CONFIG GET: %v", err)
	}
	if err := c.ConfigSet(ctx, "maxmemory-policy", policy).Err(); err != nil {
		t.Fatalf("CONFIG SET: %v", err)
	}
	t.Cleanup(func() {
		_ = c.ConfigSet(context.Background(), "maxmemory-policy", prev["maxmemory-policy"]).Err() //nolint:errcheck // best effort restore
		_ = c.Close()                                                                             //nolint:errcheck // test cleanup
	})
	return &Redis{Client: c}
}

type recordedMemory struct {
	mu    sync.Mutex
	calls int
	safe  bool
	used  int64
}

func (r *recordedMemory) SetRedisMemory(safe bool, used, _ int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	r.safe, r.used = safe, used
}

func TestCheckRedisEvictionVolatileLRUIntegration(t *testing.T) {
	r := evictionTestRedis(t, "volatile-lru")
	ctx := context.Background()

	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	info, err := CheckRedisEviction(ctx, r, false, logger)
	if err != nil {
		t.Fatalf("warn mode refused to boot: %v", err)
	}
	if info.Policy != "volatile-lru" || info.EvictionSafe() {
		t.Errorf("info = %+v, want volatile-lru and unsafe", info)
	}
	if !strings.Contains(buf.String(), "level=WARN") || !strings.Contains(buf.String(), "XCom") {
		t.Errorf("no warning naming the risk: %s", buf.String())
	}

	if _, err := CheckRedisEviction(ctx, r, true, logger); !errors.Is(err, ErrRedisEvictionUnsafe) {
		t.Errorf("strict mode error = %v, want ErrRedisEvictionUnsafe", err)
	}

	m := &recordedMemory{safe: true}
	stop := WatchRedisMemory(ctx, r, m, logger, time.Hour)
	defer stop()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.calls != 1 || m.safe || m.used <= 0 {
		t.Errorf("gauges after the first read: calls=%d safe=%v used=%d; want 1, false, > 0", m.calls, m.safe, m.used)
	}
}

func TestCheckRedisEvictionNoevictionIntegration(t *testing.T) {
	r := evictionTestRedis(t, "noeviction")
	var buf bytes.Buffer
	info, err := CheckRedisEviction(context.Background(), r, true, slog.New(slog.NewTextHandler(&buf, nil)))
	if err != nil {
		t.Fatalf("strict mode refused a noeviction server: %v", err)
	}
	if !info.EvictionSafe() || strings.Contains(buf.String(), "level=WARN") {
		t.Errorf("info = %+v, log = %s; want safe and no warning", info, buf.String())
	}
}
