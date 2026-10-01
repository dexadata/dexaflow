//go:build integration

package scheduler_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/scheduler"
)

func singleConnPool(t *testing.T, url string) *pgxpool.Pool {
	t.Helper()
	cfg, err := pgxpool.ParseConfig(url)
	if err != nil {
		t.Fatal(err)
	}
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatal(err)
	}
	return pool
}

func TestLeaderElectionMutualExclusion(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must be set for the leader election integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	poolA := singleConnPool(t, url)
	defer poolA.Close()
	poolB := singleConnPool(t, url)
	defer poolB.Close()

	a := scheduler.NewLeader(poolA)
	b := scheduler.NewLeader(poolB)

	gotA, err := a.TryAcquire(ctx)
	if err != nil || !gotA {
		t.Fatalf("A should acquire: ok=%v err=%v", gotA, err)
	}
	gotB, err := b.TryAcquire(ctx)
	if err != nil || gotB {
		t.Fatalf("B should not acquire while A holds: ok=%v err=%v", gotB, err)
	}
	if err := a.Release(ctx); err != nil {
		t.Fatalf("A release: %v", err)
	}
	gotB, err = b.TryAcquire(ctx)
	if err != nil || !gotB {
		t.Fatalf("B should acquire after A releases: ok=%v err=%v", gotB, err)
	}
	if err := b.Release(ctx); err != nil {
		t.Fatalf("B release: %v", err)
	}
}

func TestLeaderHoldsLockReflectsOwnership(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must be set for the leadership-check integration test")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	poolA := singleConnPool(t, url)
	defer poolA.Close()
	a := scheduler.NewLeader(poolA)

	// Before acquiring, this session does not hold the lock.
	if held, err := a.HoldsLock(ctx); err != nil || held {
		t.Fatalf("should not hold before acquire: held=%v err=%v", held, err)
	}
	if ok, err := a.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("A should acquire: ok=%v err=%v", ok, err)
	}
	// After acquiring, this session holds it.
	if held, err := a.HoldsLock(ctx); err != nil || !held {
		t.Fatalf("should hold after acquire: held=%v err=%v", held, err)
	}
	// A different session does not see itself as the holder (pid-scoped check).
	poolB := singleConnPool(t, url)
	defer poolB.Close()
	if held, err := scheduler.NewLeader(poolB).HoldsLock(ctx); err != nil || held {
		t.Fatalf("a non-holding session must report held=false: held=%v err=%v", held, err)
	}
	// After releasing, it no longer holds it.
	if err := a.Release(ctx); err != nil {
		t.Fatalf("A release: %v", err)
	}
	if held, err := a.HoldsLock(ctx); err != nil || held {
		t.Fatalf("should not hold after release: held=%v err=%v", held, err)
	}
}
