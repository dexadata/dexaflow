//go:build integration

package storage_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/url"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/storage"
)

// emptyDatabase creates a private database with no schema at all and returns
// its URL: a schema from before migration 060, as far as the lease goes.
func emptyDatabase(t *testing.T) string {
	t.Helper()
	base := os.Getenv("DATABASE_URL")
	if base == "" {
		t.Skip("DATABASE_URL must be set")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = admin.Close(ctx) }()
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	name := "lease_" + hex.EncodeToString(b[:])
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Skipf("cannot create a private test database (%v); the role needs CREATEDB", err)
	}
	t.Cleanup(func() {
		c, err := pgx.Connect(context.Background(), base)
		if err != nil {
			return
		}
		defer func() { _ = c.Close(context.Background()) }()
		_, _ = c.Exec(context.Background(), "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
	})
	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	u.Path = "/" + name
	return u.String()
}

// TestSchedulerLeaseWithoutTheTable: on a schema without the lease (a server
// started before migration 060, or after its rollback), a server with fencing
// off leads on the advisory lock alone, as before 0.6.0, and one with fencing
// on refuses to lead and gives the lock back.
func TestSchedulerLeaseWithoutTheTable(t *testing.T) {
	ctx := context.Background()
	dsn := emptyDatabase(t)

	off := storage.NewSchedulerLease(singleConnPool(t, dsn))
	ok, err := off.TryAcquire(ctx)
	if err != nil || !ok {
		t.Fatalf("fencing off, no lease table: TryAcquire = %v, %v; want leadership", ok, err)
	}
	if off.Term() != 0 {
		t.Fatalf("term = %d without a lease, want 0", off.Term())
	}
	if held, err := off.HoldsLock(ctx); err != nil || !held {
		t.Fatalf("HoldsLock = %v, %v; want the lock held and no renewal error", held, err)
	}
	if err := off.Release(ctx); err != nil {
		t.Fatal(err)
	}

	on := storage.NewSchedulerLease(singleConnPool(t, dsn))
	on.EnableFencing()
	if ok, err := on.TryAcquire(ctx); err == nil || ok {
		t.Fatalf("fencing on, no lease table: TryAcquire = %v, %v; want an error", ok, err)
	}
	other := storage.NewSchedulerLease(singleConnPool(t, dsn))
	if ok, err := other.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("the refused replica kept the lock: TryAcquire = %v, %v", ok, err)
	}
}
