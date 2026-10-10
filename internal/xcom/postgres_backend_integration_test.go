//go:build integration

package xcom

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// openPGBackend connects to the migrated integration database (DATABASE_URL) or
// skips. The xcom_store table comes from migration 014.
func openPGBackend(t *testing.T) (*PostgresBackend, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatalf("connecting to %s: %v", url, err)
	}
	t.Cleanup(pool.Close)
	return NewPostgresBackend(pool), pool
}

// TestPostgresBackendRoundTripIntegration exercises the full Backend contract
// against real Postgres: push, fetch, list by glob, delete, and not-found.
func TestPostgresBackendRoundTripIntegration(t *testing.T) {
	b, _ := openPGBackend(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("xcom:itest:%d", time.Now().UnixNano())
	key := prefix + ":dag:run:task:value"

	entry := Entry{Value: []byte(`{"x":1}`), ContentType: "application/json", SizeBytes: 7, CreatedAt: time.Now().UTC().Truncate(time.Second)}
	if err := b.Push(ctx, key, entry, time.Hour); err != nil {
		t.Fatalf("Push: %v", err)
	}

	got, err := b.Fetch(ctx, key)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if string(got.Value) != string(entry.Value) || got.ContentType != entry.ContentType || got.SizeBytes != entry.SizeBytes {
		t.Errorf("Fetch = %+v, want value/type/size of %+v", got, entry)
	}

	keys, err := b.List(ctx, prefix+":*")
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(keys) != 1 || keys[0] != key {
		t.Errorf("List = %v, want [%s]", keys, key)
	}

	if err := b.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := b.Fetch(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Fetch after Delete = %v, want ErrNotFound", err)
	}
}

// TestPostgresBackendExpiryIntegration proves an entry past its TTL is invisible
// to Fetch and removed by DeleteExpired (the durable equivalent of Redis expiry).
func TestPostgresBackendExpiryIntegration(t *testing.T) {
	b, _ := openPGBackend(t)
	ctx := context.Background()
	key := fmt.Sprintf("xcom:itest-exp:%d:d:r:t:v", time.Now().UnixNano())

	// now() an hour in the past, so a 1-minute TTL lands expires_at before real now.
	b.now = func() time.Time { return time.Now().Add(-time.Hour) }
	if err := b.Push(ctx, key, Entry{Value: []byte("v"), ContentType: "text/plain", SizeBytes: 1, CreatedAt: time.Now()}, time.Minute); err != nil {
		t.Fatalf("Push: %v", err)
	}

	if _, err := b.Fetch(ctx, key); !errors.Is(err, ErrNotFound) {
		t.Errorf("Fetch of expired entry = %v, want ErrNotFound", err)
	}
	n, err := b.DeleteExpired(ctx)
	if err != nil {
		t.Fatalf("DeleteExpired: %v", err)
	}
	if n < 1 {
		t.Errorf("DeleteExpired removed %d rows, want >= 1", n)
	}
}

// TestPostgresBackendFetchManyIntegration reads several keys in one query and
// leaves out expired and absent ones.
func TestPostgresBackendFetchManyIntegration(t *testing.T) {
	b, _ := openPGBackend(t)
	ctx := context.Background()
	prefix := fmt.Sprintf("xcom:itest:%d", time.Now().UnixNano())
	live, expired, absent := prefix+":a", prefix+":b", prefix+":c"
	t.Cleanup(func() { _ = b.Delete(ctx, live); _ = b.Delete(ctx, expired) })
	if err := b.Push(ctx, live, Entry{Value: []byte(`1`), ContentType: "application/json", SizeBytes: 1, CreatedAt: time.Now()}, time.Hour); err != nil {
		t.Fatalf("Push: %v", err)
	}
	if err := b.Push(ctx, expired, Entry{Value: []byte(`2`), CreatedAt: time.Now()}, time.Hour); err != nil {
		t.Fatalf("Push: %v", err)
	}
	b.now = func() time.Time { return time.Now().Add(-2 * time.Hour) }
	if err := b.Push(ctx, expired, Entry{Value: []byte(`2`), CreatedAt: time.Now()}, time.Hour); err != nil {
		t.Fatalf("Push: %v", err)
	}
	entries, found, err := b.FetchMany(ctx, []string{live, expired, absent, live})
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	want := []bool{true, false, false, true}
	for i := range want {
		if found[i] != want[i] {
			t.Errorf("found[%d] = %v, want %v", i, found[i], want[i])
		}
	}
	if string(entries[0].Value) != `1` || string(entries[3].Value) != `1` {
		t.Errorf("values = %q, %q, want 1", entries[0].Value, entries[3].Value)
	}
}
