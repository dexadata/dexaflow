package auth

import (
	"fmt"
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
