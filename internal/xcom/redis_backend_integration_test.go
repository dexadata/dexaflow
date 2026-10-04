//go:build integration

package xcom_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/dexadata/dexaflow/internal/xcom"
)

func testClient(t *testing.T) *redis.Client {
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

func TestRedisBackendRoundTrip(t *testing.T) {
	ctx := context.Background()
	b := xcom.NewRedisBackend(testClient(t))
	key := "xcom:test:dag:run:task:return_value"
	t.Cleanup(func() { _ = b.Delete(ctx, key) })

	entry := xcom.Entry{Value: []byte(`{"rows":5}`), ContentType: "application/json", SizeBytes: 10, CreatedAt: time.Now()}
	if err := b.Push(ctx, key, entry, time.Minute); err != nil {
		t.Fatalf("Push: %v", err)
	}
	got, err := b.Fetch(ctx, key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got.Value) != `{"rows":5}` {
		t.Errorf("value = %s", got.Value)
	}

	keys, err := b.List(ctx, "xcom:test:dag:run:*")
	if err != nil || len(keys) != 1 {
		t.Errorf("List = %v, %v; want one key", keys, err)
	}

	if derr := b.Delete(ctx, key); derr != nil {
		t.Fatalf("Delete: %v", derr)
	}
	if _, ferr := b.Fetch(ctx, key); ferr != xcom.ErrNotFound {
		t.Errorf("Fetch after delete = %v, want ErrNotFound", ferr)
	}
}

func TestRedisBackendFetchMany(t *testing.T) {
	ctx := context.Background()
	b := xcom.NewRedisBackend(testClient(t))
	k1, k2 := "xcom:test:dag:many:a:return_value", "xcom:test:dag:many:b:return_value"
	t.Cleanup(func() { _ = b.Delete(ctx, k1); _ = b.Delete(ctx, k2) })
	if err := b.Push(ctx, k1, xcom.Entry{Value: []byte(`1`), ContentType: "application/json", SizeBytes: 1}, time.Minute); err != nil {
		t.Fatalf("Push: %v", err)
	}
	entries, found, err := b.FetchMany(ctx, []string{k1, k2})
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	if !found[0] || string(entries[0].Value) != `1` || found[1] {
		t.Errorf("FetchMany = %+v %v, want only the first key found", entries, found)
	}
}
