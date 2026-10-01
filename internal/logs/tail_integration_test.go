//go:build integration

package logs_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/logs"
)

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	url := os.Getenv("LEOFLOW_TEST_REDIS_URL")
	if url == "" {
		url = "redis://localhost:6379/0"
	}
	opts, err := redis.ParseURL(url)
	if err != nil {
		t.Fatal(err)
	}
	c := redis.NewClient(opts)
	if perr := c.Ping(context.Background()).Err(); perr != nil {
		t.Skipf("redis unavailable: %v", perr)
	}
	return c
}

func TestRedisTailerPublishSubscribe(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	tailer := logs.NewRedisTailer(testRedis(t))
	ref := logs.Ref{TenantID: "t", DagID: "d", RunID: "r", TaskID: "task", TryNumber: 1}

	lines, stop := tailer.Subscribe(ctx, ref)
	defer stop()
	time.Sleep(100 * time.Millisecond) // let the subscription register

	if err := tailer.Publish(ctx, ref, "hello live"); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	select {
	case got := <-lines:
		if got != "hello live" {
			t.Errorf("tailed line = %q, want %q", got, "hello live")
		}
	case <-ctx.Done():
		t.Fatal("timed out waiting for the published line")
	}
}
