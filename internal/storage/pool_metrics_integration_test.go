//go:build integration

package storage

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestPoolCollectorSeesAnExhaustedRequestPool pins #1049: once the probe has
// its own pool (#1046), a request pool that is fully lent out leaves readiness
// green, so the pool statistics are the only signal that callers are waiting.
// The test lends out the only connection, makes one caller give up and another
// wait, and reads both on the scrape.
func TestPoolCollectorSeesAnExhaustedRequestPool(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := NewPostgres(ctx, config.DatabaseSection{URL: url, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	c := NewPoolCollector(pg)

	held, err := pg.Pool.Acquire(ctx)
	if err != nil {
		t.Fatalf("acquiring the only request connection: %v", err)
	}
	giveUp, cancel := context.WithTimeout(ctx, 50*time.Millisecond)
	defer cancel()
	if _, aerr := pg.Pool.Acquire(giveUp); !errors.Is(aerr, context.DeadlineExceeded) {
		t.Fatalf("acquire on an exhausted pool: %v, want a deadline", aerr)
	}
	got := make(chan error, 1)
	go func() {
		conn, aerr := pg.Pool.Acquire(ctx)
		if aerr == nil {
			conn.Release()
		}
		got <- aerr
	}()
	time.Sleep(50 * time.Millisecond)
	if v := scrape(t, c)["dexaflow_db_pool_acquired_conns"]; v["main"] != 1 || v["health"] != 0 {
		t.Fatalf("acquired conns = %v, want main fully lent out and health idle", v)
	}
	held.Release()
	if aerr := <-got; aerr != nil {
		t.Fatalf("waiting acquire: %v", aerr)
	}

	s := scrape(t, c)
	if v := s["dexaflow_db_pool_canceled_acquires_total"]; v["main"] != 1 || v["health"] != 0 {
		t.Errorf("canceled acquires = %v, want the one that gave up, on main", v)
	}
	if v := s["dexaflow_db_pool_empty_acquires_total"]; v["main"] < 2 {
		t.Errorf("empty acquires = %v, want at least the 2 that found main empty", v)
	}
	if v := s["dexaflow_db_pool_empty_acquire_wait_seconds_total"]; v["main"] <= 0 {
		t.Errorf("wait seconds = %v, want the time the second caller waited", v)
	}
}

// TestPoolCollectorFollowsADedicatedSchedulerPool drives ForScheduler, the path
// the server takes with database.scheduler_max_conns set: the scheduler pool
// shows while it is open and leaves the scrape when it is released.
func TestPoolCollectorFollowsADedicatedSchedulerPool(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	db := config.DatabaseSection{URL: url, MaxOpenConns: 2, SchedulerMaxConns: 3}
	pg, err := NewPostgres(ctx, db)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	c := NewPoolCollector(pg)

	sched, release, err := pg.ForScheduler(ctx, db)
	if err != nil || sched == pg {
		t.Fatalf("scheduler_max_conns set must open a dedicated pool: %v", err)
	}
	if got := scrape(t, c)["dexaflow_db_pool_max_conns"]; got["scheduler"] != 3 || len(got) != 3 {
		t.Fatalf("max conns = %v, want main, health and a scheduler pool of 3", got)
	}
	release()
	if got := scrape(t, c)["dexaflow_db_pool_max_conns"]; len(got) != 2 {
		t.Fatalf("max conns = %v after release, want the scheduler pool gone", got)
	}
}
