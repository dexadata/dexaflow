package storage

import (
	"context"
	"errors"
	"testing"
	"time"
)

// TestConnectWithRetrySucceedsAfterTransientFailures: cold-start race —
// `pg_isready` says OK before Postgres accepts client connections; the first N
// pings hit "connection reset by peer", then the server is up. The control
// plane must retry instead of dying. This is the test that would have caught
// the Lima 2026-06-01 boot failure where docker compose reported the Postgres
// container Healthy but the pgx ping immediately got reset.
func TestConnectWithRetrySucceedsAfterTransientFailures(t *testing.T) {
	calls := 0
	transient := errors.New("read tcp 127.0.0.1->127.0.0.1: read: connection reset by peer")
	ping := func(_ context.Context) error {
		calls++
		if calls < 3 {
			return transient
		}
		return nil
	}
	if err := connectWithRetry(context.Background(), ping, 2*time.Second, 1*time.Millisecond); err != nil {
		t.Fatalf("retry must recover from transient failures, got: %v", err)
	}
	if calls != 3 {
		t.Errorf("expected 3 ping attempts (2 fail + 1 success), got %d", calls)
	}
}

// TestConnectWithRetryGivesUpAfterBudget: Postgres never comes back; the
// caller MUST see the last underlying error wrapped, not a generic timeout,
// so the operator can see e.g. "wrong DSN" vs "auth failed" vs "host
// unreachable".
func TestConnectWithRetryGivesUpAfterBudget(t *testing.T) {
	underlying := errors.New("pg_hba.conf rejects connection: wrong password")
	ping := func(_ context.Context) error { return underlying }
	err := connectWithRetry(context.Background(), ping, 50*time.Millisecond, 1*time.Millisecond)
	if err == nil {
		t.Fatal("expected an error after the budget elapsed, got nil")
	}
	if !errors.Is(err, underlying) {
		t.Errorf("budget-exhausted error must wrap the last underlying error; got: %v", err)
	}
}

// TestConnectWithRetryRespectsContextCancel: if the operator hits Ctrl-C
// during startup, the retry loop must exit immediately, not run out the full
// budget. Otherwise a stuck `dexaflow lite` ignores SIGINT for 30s.
func TestConnectWithRetryRespectsContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	ping := func(_ context.Context) error {
		calls++
		if calls == 1 {
			cancel() // cancel after the first failed ping
		}
		return errors.New("never resolves")
	}
	start := time.Now()
	err := connectWithRetry(ctx, ping, 10*time.Second, 200*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("expected error after context cancel")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected ctx.Canceled error, got %v", err)
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("loop must exit on ctx cancel, but ran for %s", elapsed)
	}
}
