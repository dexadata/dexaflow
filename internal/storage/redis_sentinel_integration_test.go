//go:build integration

// Package storage_test: Redis through Sentinel (#2358). A failover moves the
// master while tasks push and fetch XCom; every error in the window must be
// one the agent path retries (storeerr.Transient), so no task fails.
package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/storeerr"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// sentinelAddr is the sentinel of scripts/redis-sentinel-up.sh, or a skip.
func sentinelAddr(t *testing.T) string {
	t.Helper()
	addr := os.Getenv("DEXAFLOW_TEST_REDIS_SENTINEL")
	if addr == "" {
		t.Skip("DEXAFLOW_TEST_REDIS_SENTINEL is not set; start a pair with scripts/redis-sentinel-up.sh")
	}
	return addr
}

func masterOf(ctx context.Context, t *testing.T, sentinel *redis.SentinelClient) string {
	t.Helper()
	addr, err := sentinel.GetMasterAddrByName(ctx, "mymaster").Result()
	if err != nil {
		t.Fatalf("asking the sentinel for the master: %v", err)
	}
	return strings.Join(addr, ":")
}

// retry runs op until it succeeds, failing the test on an error the agent
// path would not retry or past the deadline.
func retry(ctx context.Context, t *testing.T, what string, transient *atomic.Int64, op func() error) {
	deadline := time.Now().Add(30 * time.Second)
	for {
		err := op()
		if err == nil {
			return
		}
		if !storeerr.Transient(err) {
			t.Errorf("%s: an error the agent would not retry, so the task would fail: %v", what, err)
			return
		}
		transient.Add(1)
		if time.Now().After(deadline) {
			t.Errorf("%s: still failing after 30 s: %v", what, err)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

func TestRedisSentinelFailoverKeepsXComWorking(t *testing.T) {
	addr := sentinelAddr(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	rd, err := storage.NewRedis(ctx, config.RedisSection{
		Sentinel:    config.RedisSentinelSection{Master: "mymaster", Addrs: []string{addr}},
		ReadTimeout: time.Second, DialTimeout: time.Second,
	})
	if err != nil {
		t.Fatalf("NewRedis through Sentinel: %v", err)
	}
	defer rd.Close()
	backend := xcom.NewRedisBackend(rd.Client)
	sentinel := redis.NewSentinelClient(&redis.Options{Addr: addr})
	defer sentinel.Close()
	before := masterOf(ctx, t, sentinel)

	var transient atomic.Int64
	stop := make(chan struct{})
	done := make(chan int)
	go func() {
		n := 0
		for {
			select {
			case <-stop:
				done <- n
				return
			default:
			}
			key := fmt.Sprintf("xcom:t:dag:run-failover:task:k%d", n)
			entry := xcom.Entry{Value: []byte(fmt.Sprintf(`{"n":%d}`, n)), ContentType: "application/json", CreatedAt: time.Now()}
			retry(ctx, t, "push", &transient, func() error { return backend.Push(ctx, key, entry, time.Minute) })
			retry(ctx, t, "fetch", &transient, func() error {
				got, err := backend.Fetch(ctx, key)
				if errors.Is(err, xcom.ErrNotFound) {
					// A write the old master took but did not replicate
					// before it was demoted: the agent pushes again.
					return backend.Push(ctx, key, entry, time.Minute)
				}
				if err == nil && string(got.Value) != string(entry.Value) {
					return fmt.Errorf("value %s, want %s", got.Value, entry.Value)
				}
				return err
			})
			n++
		}
	}()

	time.Sleep(500 * time.Millisecond)
	if err := sentinel.Failover(ctx, "mymaster").Err(); err != nil {
		t.Fatalf("SENTINEL FAILOVER: %v", err)
	}
	moved := false
	for i := 0; i < 100 && !moved; i++ {
		time.Sleep(200 * time.Millisecond)
		moved = masterOf(ctx, t, sentinel) != before
	}
	time.Sleep(2 * time.Second) // keep pushing on the new master
	close(stop)
	ops := <-done
	if !moved {
		t.Fatal("the sentinel did not move the master")
	}
	if ops < 10 {
		t.Errorf("only %d push and fetch rounds across the failover", ops)
	}
	t.Logf("master %s -> %s, %d rounds, %d transient errors retried", before, masterOf(ctx, t, sentinel), ops, transient.Load())
}
