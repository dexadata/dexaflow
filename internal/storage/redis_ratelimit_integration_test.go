//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestRedisRateLimitAcrossReplicasIntegration: two limiters, as two API
// replicas behind one Service, sharing one Redis, allow the configured login
// limit between them; with Redis unreachable each falls back to its own
// limit (#2357).
func TestRedisRateLimitAcrossReplicasIntegration(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL must be set for the integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rd, err := storage.NewRedis(ctx, config.RedisSection{URL: redisURL})
	if err != nil {
		t.Fatalf("NewRedis: %v", err)
	}
	defer rd.Close()

	scope := fmt.Sprintf("login_it_%d", time.Now().UnixNano())
	counter := storage.NewRedisRateCounter(rd.Client)
	replicaA := auth.NewRateLimiter(5, time.Minute).Share(counter, scope)
	replicaB := auth.NewRateLimiter(5, time.Minute).Share(counter, scope)
	allowed := 0
	for i := 0; i < 12; i++ {
		limiter := replicaA
		if i%2 == 1 {
			limiter = replicaB
		}
		if limiter.Allow("203.0.113.7") {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("two replicas allowed %d failed logins, want the configured 5", allowed)
	}
	if !replicaB.Blocked("203.0.113.7") {
		t.Error("the address is not blocked on the other replica")
	}
	replicaA.Refund("203.0.113.7")
	if n, gerr := counter.Get(ctx, "missing-key"); gerr != nil || n != 0 {
		t.Errorf("Get of an absent key = %d, %v; want 0, nil", n, gerr)
	}

	down := storage.NewRedisRateCounter(redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 50 * time.Millisecond}))
	outA := auth.NewRateLimiter(2, time.Minute).Share(down, scope)
	outB := auth.NewRateLimiter(2, time.Minute).Share(down, scope)
	for _, l := range []*auth.RateLimiter{outA, outB} {
		if !l.Allow("198.51.100.9") || !l.Allow("198.51.100.9") {
			t.Fatal("with Redis down, a replica refused events within its own limit")
		}
		if l.Allow("198.51.100.9") {
			t.Error("with Redis down, a replica allowed more than its own limit")
		}
	}
}
