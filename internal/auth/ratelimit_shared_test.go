package auth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// memCounter is a SharedCounter in memory: what two replicas reaching one
// Redis see.
type memCounter struct {
	mu   sync.Mutex
	n    map[string]int64
	fail bool
}

var errCounterDown = errors.New("counter down")

func (m *memCounter) Incr(_ context.Context, key string, _ time.Duration) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return 0, errCounterDown
	}
	if m.n == nil {
		m.n = map[string]int64{}
	}
	m.n[key]++
	return m.n[key], nil
}

func (m *memCounter) Get(_ context.Context, key string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return 0, errCounterDown
	}
	return m.n[key], nil
}

func (m *memCounter) Decr(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.fail {
		return errCounterDown
	}
	if m.n[key] > 0 {
		m.n[key]--
	}
	return nil
}

// TestSharedLimiterHoldsAcrossReplicas: two limiters sharing one counter
// allow the configured limit between them, not twice it (#2357).
func TestSharedLimiterHoldsAcrossReplicas(t *testing.T) {
	counter := &memCounter{}
	a := NewRateLimiter(5, time.Minute).Share(counter, "login")
	b := NewRateLimiter(5, time.Minute).Share(counter, "login")

	allowed := 0
	for i := 0; i < 10; i++ {
		limiter := a
		if i%2 == 1 {
			limiter = b
		}
		if limiter.Allow("10.0.0.1") {
			allowed++
		}
	}
	if allowed != 5 {
		t.Errorf("two replicas allowed %d attempts, want 5", allowed)
	}
	if !a.Blocked("10.0.0.1") || !b.Blocked("10.0.0.1") {
		t.Error("an address over the shared limit is not blocked on both replicas")
	}
	if a.Blocked("10.0.0.2") {
		t.Error("another address is blocked")
	}
}

// TestSharedLimiterScopesDoNotMix: limiters with different scopes keep their
// own budgets on the same counter.
func TestSharedLimiterScopesDoNotMix(t *testing.T) {
	counter := &memCounter{}
	login := NewRateLimiter(1, time.Minute).Share(counter, "login")
	renew := NewRateLimiter(1, time.Minute).Share(counter, "renew")
	if !login.Allow("k") || !renew.Allow("k") {
		t.Fatal("the first event of each scope was refused")
	}
	if login.Allow("k") {
		t.Error("login allowed a second event over its limit of 1")
	}
}

// TestSharedLimiterRefund: a refund gives the event back on the shared count.
func TestSharedLimiterRefund(t *testing.T) {
	counter := &memCounter{}
	a := NewRateLimiter(1, time.Minute).Share(counter, "start")
	b := NewRateLimiter(1, time.Minute).Share(counter, "start")
	if !a.Allow("k") {
		t.Fatal("first event refused")
	}
	a.Refund("k")
	if !b.Allow("k") {
		t.Error("the other replica did not get the refunded event")
	}
}

// TestSharedLimiterFallsBackWhenTheCounterFails: with the counter down the
// limiter keeps the per replica bound, so an outage locks nobody out and
// still limits each replica.
func TestSharedLimiterFallsBackWhenTheCounterFails(t *testing.T) {
	counter := &memCounter{fail: true}
	a := NewRateLimiter(2, time.Minute).Share(counter, "login")
	if a.Blocked("k") {
		t.Fatal("a fresh address is blocked while the counter is down")
	}
	if !a.Allow("k") || !a.Allow("k") {
		t.Fatal("the local limit refused events within it while the counter is down")
	}
	if a.Allow("k") {
		t.Error("the local limit allowed a third event over its limit of 2")
	}
	if !a.Blocked("k") {
		t.Error("an address over the local limit is not blocked while the counter is down")
	}
}

// TestSharedKeyIsPerWindow: the shared key names the scope, the key and the
// window, so a new window starts a new count.
func TestSharedKeyIsPerWindow(t *testing.T) {
	r := NewRateLimiter(1, time.Minute).Share(&memCounter{}, "login")
	t0 := time.Date(2026, 11, 6, 12, 0, 10, 0, time.UTC)
	if got, want := r.sharedKey("1.2.3.4", t0), "rl:login:1.2.3.4:"+itoa(t0.UnixNano()/int64(time.Minute)); got != want {
		t.Errorf("sharedKey = %q, want %q", got, want)
	}
	if r.sharedKey("k", t0) == r.sharedKey("k", t0.Add(time.Minute)) {
		t.Error("two windows share a key")
	}
}
