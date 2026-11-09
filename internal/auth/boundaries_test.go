package auth

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// TestRenewAgentTokenAtTheCeilingStillRenews pins the max-lifetime bound of an
// agent credential as strict: an attempt exactly max_lifetime old still
// renews, and only one past it lapses.
func TestRenewAgentTokenAtTheCeilingStillRenews(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0.Add(24 * time.Hour) }
	aged, err := a.mintAgentToken(agentIdentity(), 10*time.Minute, t0)
	if err != nil {
		t.Fatalf("mintAgentToken: %v", err)
	}
	renewed, ok, err := a.RenewAgentToken(aged, 10*time.Minute, 24*time.Hour)
	if err != nil {
		t.Fatalf("RenewAgentToken: %v", err)
	}
	if !ok || renewed == "" {
		t.Errorf("renewal exactly at the ceiling must succeed; got ok=%v renewed=%q", ok, renewed)
	}
}

// TestRenewUserTokenAtTheCeilingStillRenews pins the same strict bound for a
// user session: exactly max_lifetime since first login still renews.
func TestRenewUserTokenAtTheCeilingStillRenews(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	t0 := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return t0.Add(24 * time.Hour) }
	aged, err := a.mintUserToken(&User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, time.Hour, t0)
	if err != nil {
		t.Fatalf("mintUserToken: %v", err)
	}
	renewed, ok, err := a.RenewUserToken(context.Background(), aged, time.Hour, 24*time.Hour)
	if err != nil {
		t.Fatalf("RenewUserToken: %v", err)
	}
	if !ok || renewed == "" {
		t.Errorf("renewal exactly at max_lifetime must succeed; got ok=%v renewed=%q", ok, renewed)
	}
}

// TestSharedLimiterBlockedAtExactlyTheLimit: with a shared counter, a key
// whose window already holds limit events is blocked, the same as the
// in-process count (#2357).
func TestSharedLimiterBlockedAtExactlyTheLimit(t *testing.T) {
	counter := &memCounter{}
	r := NewRateLimiter(3, time.Minute).Share(counter, "login")
	for range 2 {
		r.Allow("ip")
	}
	if r.Blocked("ip") {
		t.Fatal("blocked one event under the limit")
	}
	r.Allow("ip")
	if !r.Blocked("ip") {
		t.Fatal("not blocked with exactly limit events in the window")
	}
}

// TestRateLimiterSweepsOnceAFullWindowPassed: the sweep runs as soon as one
// whole window has passed since the last one, not one tick later, so a
// bucket that ended is dropped on time.
func TestRateLimiterSweepsOnceAFullWindowPassed(t *testing.T) {
	t0 := time.Unix(1_700_000_000, 0)
	now := t0
	r := NewRateLimiter(3, time.Minute)
	r.now = func() time.Time { return now }
	r.Allow("first") // sweeps at t0; first ends at t0+60s
	now = t0.Add(30 * time.Second)
	r.Allow("mid") // no sweep; mid ends at t0+90s
	now = t0.Add(61 * time.Second)
	r.Allow("x") // sweeps at t0+61s: first ended, mid has not; x ends at t0+121s
	if got := r.size(); got != 2 {
		t.Fatalf("buckets at t0+61s = %d, want mid and x", got)
	}
	now = t0.Add(121 * time.Second) // exactly one window after the last sweep
	r.Allow("z")
	if got := r.size(); got != 2 {
		t.Fatalf("buckets one window after the last sweep = %d, want x and z (mid ended at t0+90s)", got)
	}
}

// TestAPITokenTouchLogsOnlyAFailedWrite: recording a token's use is best
// effort, and a write that succeeds logs nothing.
func TestAPITokenTouchLogsOnlyAFailedWrite(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	a, _, _, _, plain := newTokenFixture(t)
	if _, err := a.Authenticate(context.Background(), plain); err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	if strings.Contains(buf.String(), "recording api token use") {
		t.Errorf("a successful use write was logged as a failure: %s", buf.String())
	}
}
