//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
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

	a := storage.NewSchedulerLease(poolA)
	b := storage.NewSchedulerLease(poolB)

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
	a := storage.NewSchedulerLease(poolA)

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
	if held, err := storage.NewSchedulerLease(poolB).HoldsLock(ctx); err != nil || held {
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

// leaseTerm reads the lease's current term.
func leaseTerm(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int64 {
	t.Helper()
	var term int64
	if err := pool.QueryRow(ctx, `SELECT term FROM scheduler_lease WHERE id = 1`).Scan(&term); err != nil {
		t.Fatalf("reading the lease term: %v", err)
	}
	return term
}

// lockSessionPID is the backend pid of the session holding the scheduler lock.
func lockSessionPID(ctx context.Context, t *testing.T, pool *pgxpool.Pool) int {
	t.Helper()
	var pid int
	if err := pool.QueryRow(ctx,
		`SELECT pid FROM pg_locks WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND objsubid = 1`,
		int32(storage.SchedulerLockID>>32), int32(storage.SchedulerLockID&0xFFFFFFFF)).Scan(&pid); err != nil {
		t.Fatalf("finding the scheduler lock holder: %v", err)
	}
	return pid
}

// TestSchedulerLeaseTermPerAcquisition: every acquisition takes a new term
// (ADR 0106, point 1), and the holder is recorded.
func TestSchedulerLeaseTermPerAcquisition(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must be set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	poolA, poolB, probe := singleConnPool(t, url), singleConnPool(t, url), singleConnPool(t, url)
	defer poolA.Close()
	defer poolB.Close()
	defer probe.Close()
	a, b := storage.NewSchedulerLease(poolA), storage.NewSchedulerLease(poolB)
	a.SetHolder("replica-a")
	b.SetHolder("replica-b")

	before := leaseTerm(ctx, t, probe)
	if ok, err := a.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("A should acquire: ok=%v err=%v", ok, err)
	}
	if a.Term() != before+1 || leaseTerm(ctx, t, probe) != before+1 {
		t.Fatalf("A's term = %d, lease term = %d, want both %d", a.Term(), leaseTerm(ctx, t, probe), before+1)
	}
	var holder string
	if err := probe.QueryRow(ctx, `SELECT holder FROM scheduler_lease WHERE id = 1`).Scan(&holder); err != nil || holder != "replica-a" {
		t.Fatalf("holder = %q (%v), want replica-a", holder, err)
	}
	if ok, err := b.TryAcquire(ctx); err != nil || ok {
		t.Fatalf("B must not acquire while A holds: ok=%v err=%v", ok, err)
	}
	if leaseTerm(ctx, t, probe) != before+1 {
		t.Fatal("a failed campaign moved the term")
	}
	if err := a.Release(ctx); err != nil {
		t.Fatal(err)
	}
	if a.Term() != before+1 {
		t.Fatalf("release changed A's term to %d; a deposed leader keeps its last term", a.Term())
	}
	if ok, err := b.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("B should acquire after A released: ok=%v err=%v", ok, err)
	}
	defer b.Release(ctx) //nolint:errcheck // cleanup
	if b.Term() != before+2 {
		t.Fatalf("B's term = %d, want %d", b.Term(), before+2)
	}
}

// TestSchedulerLeaseRenewalStepsDownOnANewerTerm: a leader whose session
// dropped, while another replica took a newer term, learns it from its next
// renewal whatever its own lock check would say (ADR 0106, point 2).
func TestSchedulerLeaseRenewalStepsDownOnANewerTerm(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must be set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	poolA, poolB, probe := singleConnPool(t, url), singleConnPool(t, url), singleConnPool(t, url)
	defer poolA.Close()
	defer poolB.Close()
	defer probe.Close()
	a, b := storage.NewSchedulerLease(poolA), storage.NewSchedulerLease(poolB)

	if ok, err := a.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("A should acquire: ok=%v err=%v", ok, err)
	}
	if held, err := a.HoldsLock(ctx); err != nil || !held {
		t.Fatalf("A renews its own term: held=%v err=%v", held, err)
	}
	if _, err := probe.Exec(ctx, `SELECT pg_terminate_backend($1)`, lockSessionPID(ctx, t, probe)); err != nil {
		t.Fatal(err)
	}
	waitLockFree(ctx, t, probe)
	if ok, err := b.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("B should acquire once A's session is gone: ok=%v err=%v", ok, err)
	}
	defer b.Release(ctx) //nolint:errcheck // cleanup
	// The first check after the session dropped may fail on the dead
	// connection, which the watchdog counts as a failed check; the next one
	// runs on a new session and finds the newer term.
	cur, err := a.Current(ctx)
	for i := 0; i < 2 && err != nil; i++ { // the first read may meet the dead session
		cur, err = a.Current(ctx)
	}
	if err != nil || cur {
		t.Fatalf("A reads its superseded term: current=%v err=%v, want false", cur, err)
	}
	if cur, err := b.Current(ctx); err != nil || !cur {
		t.Fatalf("B's term is not current: %v %v", cur, err)
	}
	held, err := a.HoldsLock(ctx)
	for i := 0; i < 2 && err != nil && !errors.Is(err, scheduler.ErrLeaseSuperseded); i++ {
		held, err = a.HoldsLock(ctx)
	}
	if held || !errors.Is(err, scheduler.ErrLeaseSuperseded) {
		t.Fatalf("A after B took term %d: held=%v err=%v, want ErrLeaseSuperseded", b.Term(), held, err)
	}
	if held, err := b.HoldsLock(ctx); err != nil || !held {
		t.Fatalf("B renews its term: held=%v err=%v", held, err)
	}
}

// waitLockFree waits until no session holds the scheduler lock.
func waitLockFree(ctx context.Context, t *testing.T, probe *pgxpool.Pool) {
	t.Helper()
	for {
		var n int
		if err := probe.QueryRow(ctx,
			`SELECT count(*) FROM pg_locks WHERE locktype = 'advisory' AND classid = $1 AND objid = $2 AND objsubid = 1`,
			int32(storage.SchedulerLockID>>32), int32(storage.SchedulerLockID&0xFFFFFFFF)).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatal("the scheduler lock was not freed")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// TestFencedSchedulerPoolRefusesAnOlderTerm is the fence itself (ADR 0106,
// points 3 and 4): once B holds a newer term, A's scheduler pool can no
// longer write runs or task instances, a transaction A had in flight does
// not commit, B's pool writes, and an untagged pool (the API, the agents) is
// not fenced at all.
func TestFencedSchedulerPoolRefusesAnOlderTerm(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must be set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cfg := config.DatabaseSection{URL: url, MaxOpenConns: 4}
	main, err := storage.NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer main.Close()
	leaderA, leaderB, probe := singleConnPool(t, url), singleConnPool(t, url), singleConnPool(t, url)
	defer leaderA.Close()
	defer leaderB.Close()
	defer probe.Close()
	a, b := storage.NewSchedulerLease(leaderA), storage.NewSchedulerLease(leaderB)
	a.EnableFencing()
	b.EnableFencing()
	var fencedA atomic.Int64
	schedA, releaseA, err := main.ForFencedScheduler(ctx, cfg, a, func() { fencedA.Add(1) })
	if err != nil {
		t.Fatal(err)
	}
	defer releaseA()
	mainB, err := storage.NewPostgres(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer mainB.Close()
	schedB, releaseB, err := mainB.ForFencedScheduler(ctx, cfg, b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer releaseB()

	// A no-op write still runs the statement trigger.
	write := `UPDATE task_instances SET state = state WHERE false`
	if ok, err := a.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("A should acquire: ok=%v err=%v", ok, err)
	}
	if _, err := schedA.Pool.Exec(ctx, write); err != nil {
		t.Fatalf("the current leader's write was refused: %v", err)
	}
	var app string
	if err := schedA.Pool.QueryRow(ctx, `SELECT current_setting('application_name')`).Scan(&app); err != nil || app != fmt.Sprintf("dexaflow-leader/%d", a.Term()) {
		t.Fatalf("A's session is named %q (%v), want dexaflow-leader/%d", app, err, a.Term())
	}
	inflight, err := schedA.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer inflight.Rollback(ctx) //nolint:errcheck // cleanup
	if _, err := inflight.Exec(ctx, write); err != nil {
		t.Fatalf("A's write before the new term: %v", err)
	}

	// A's lock session drops; B takes the next term and ends A's sessions.
	if _, err := probe.Exec(ctx, `SELECT pg_terminate_backend($1)`, lockSessionPID(ctx, t, probe)); err != nil {
		t.Fatal(err)
	}
	waitLockFree(ctx, t, probe)
	if ok, err := b.TryAcquire(ctx); err != nil || !ok {
		t.Fatalf("B should acquire: ok=%v err=%v", ok, err)
	}
	defer b.Release(ctx) //nolint:errcheck // cleanup

	if err := inflight.Commit(ctx); err == nil {
		t.Fatal("a transaction of the older term committed after the term moved")
	}
	_, err = schedA.Pool.Exec(ctx, write)
	if !storage.IsStaleLeaderTerm(err) {
		t.Fatalf("A's write after B took the term: err=%v, want stale_leader_term", err)
	}
	if fencedA.Load() == 0 {
		t.Error("the refused write was not counted")
	}
	if _, err := schedB.Pool.Exec(ctx, write); err != nil {
		t.Fatalf("the new leader's write was refused: %v", err)
	}
	if _, err := main.Pool.Exec(ctx, write); err != nil {
		t.Fatalf("an untagged session was fenced: %v", err)
	}
}
