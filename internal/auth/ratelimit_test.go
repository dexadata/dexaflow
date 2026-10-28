package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// An anonymous route keys the limiter by address, which callers choose, so
// buckets whose window ended are dropped and the map stays bounded by what
// one window can hold.
func TestRateLimiterEvictsExpiredBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRateLimiter(3, time.Minute)
	r.now = func() time.Time { return now }
	for i := range 1000 {
		r.Allow(fmt.Sprintf("198.51.100.%d", i))
	}
	if got := r.size(); got != 1000 {
		t.Fatalf("buckets = %d, want 1000 within the window", got)
	}
	now = now.Add(time.Minute + time.Second)
	r.Allow("203.0.113.1")
	if got := r.size(); got != 1 {
		t.Fatalf("buckets after the window = %d, want only the new one", got)
	}
}

// Eviction never forgets a live bucket: a key over its limit stays blocked.
func TestRateLimiterEvictionKeepsLiveBuckets(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRateLimiter(1, time.Minute)
	r.now = func() time.Time { return now }
	r.Allow("a")
	now = now.Add(30 * time.Second)
	r.Allow("b")
	now = now.Add(31 * time.Second) // a's window ended, b's has not
	r.Allow("c")
	if r.Blocked("a") || !r.Blocked("b") {
		t.Fatalf("after eviction: a blocked=%v (want false), b blocked=%v (want true)", r.Blocked("a"), r.Blocked("b"))
	}
}

func TestAddressBucket(t *testing.T) {
	for in, want := range map[string]string{
		"198.51.100.7":               "198.51.100.7",
		"2001:db8:1:2:aaaa::1":       "2001:db8:1:2::/64",
		"2001:db8:1:2:bbbb:cccc::99": "2001:db8:1:2::/64",
		"::ffff:198.51.100.7":        "198.51.100.7",
		"not an address":             "not an address",
	} {
		if got := AddressBucket(in); got != want {
			t.Errorf("AddressBucket(%q) = %q, want %q", in, got, want)
		}
	}
}

// Allow checks and spends in one step under the lock, so concurrent callers
// of one key never get more than the limit between them, the property a
// caller relies on when it spends before the work it limits.
func TestRateLimiterAllowHoldsUnderConcurrency(t *testing.T) {
	r := NewRateLimiter(10, time.Minute)
	var allowed atomic.Int64
	var wg sync.WaitGroup
	for range 1000 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if r.Allow("k") {
				allowed.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := allowed.Load(); got != 10 {
		t.Fatalf("allowed = %d of 1000 concurrent calls, want exactly 10", got)
	}
}

// Refund gives back one event of the current window, so a caller can spend
// before the work and return the budget when the work succeeds.
func TestRateLimiterRefund(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	r := NewRateLimiter(2, time.Minute)
	r.now = func() time.Time { return now }
	r.Allow("k")
	r.Allow("k")
	if r.Allow("k") {
		t.Fatal("third Allow within the limit of 2")
	}
	r.Refund("k")
	if !r.Allow("k") {
		t.Fatal("Allow after a Refund was refused")
	}
	if r.Allow("k") {
		t.Fatal("one Refund bought more than one event")
	}
	r.Refund("unknown") // no bucket: nothing to give back, and no panic
	now = now.Add(2 * time.Minute)
	r.Refund("k") // the window ended: a refund never goes below zero
	r.Allow("k")
	r.Allow("k")
	if r.Allow("k") {
		t.Fatal("a refund after the window raised the next window's limit")
	}
}

func TestAddressBlock(t *testing.T) {
	for _, tc := range []struct {
		in   string
		bits int
		want string
	}{
		{"2001:db8:1:2aa:aaaa::1", 56, "2001:db8:1:200::/56"},
		{"2001:db8:1:2ff:bbbb::1", 56, "2001:db8:1:200::/56"},
		{"198.51.100.7", 56, "198.51.100.7"},
	} {
		if got := AddressBlock(tc.in, tc.bits); got != tc.want {
			t.Errorf("AddressBlock(%q, %d) = %q, want %q", tc.in, tc.bits, got, tc.want)
		}
	}
}
