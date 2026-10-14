package auth

import (
	"net"
	"sync"
	"time"
)

// RateLimiter is a per-key fixed-window limiter used to throttle failed logins
// per client IP (ADR 0008).
type RateLimiter struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	buckets map[string]*windowState
	now     func() time.Time
	// swept is when expired buckets were last dropped. On routes anyone can
	// call, the keys are addresses the caller picks, so a bucket whose
	// window ended is dropped at most one window later and the map holds
	// only what one window can. The sweep runs under mu, so the one call per
	// window that sweeps waits for a pass over the map; every other call in
	// that window skips it.
	swept time.Time
}

type windowState struct {
	count   int
	resetAt time.Time
}

// NewRateLimiter builds a limiter allowing limit events per window per key.
func NewRateLimiter(limit int, window time.Duration) *RateLimiter {
	return &RateLimiter{
		limit:   limit,
		window:  window,
		buckets: make(map[string]*windowState),
		now:     time.Now,
	}
}

// Blocked reports whether key has already reached its limit in the current
// window, WITHOUT recording an attempt (a peek). The login handler uses it to
// reject an over-limit caller up front while calling Allow only for actual
// failures — so a successful login never consumes the budget and a user who
// mistypes a few times is not locked out the moment they finally get it right.
func (r *RateLimiter) Blocked(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.buckets[key]
	if !ok || r.now().After(st.resetAt) {
		return false
	}
	return st.count >= r.limit
}

// Allow records an event for key and reports whether it is within the limit.
// The check and the spend are one step under the lock, so concurrent callers
// of a key never get more than the limit between them: a caller that must
// bound concurrent work spends with Allow before the work, not after a
// Blocked peek, and gives the event back with Refund when it should not
// count.
func (r *RateLimiter) Allow(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	r.sweep(now)
	st, ok := r.buckets[key]
	if !ok || now.After(st.resetAt) {
		r.buckets[key] = &windowState{count: 1, resetAt: now.Add(r.window)}
		return true
	}
	if st.count >= r.limit {
		return false
	}
	st.count++
	return true
}

// Refund gives back one event of key's window, never going below zero. A
// window that ended needs none: the next Allow starts a fresh one.
func (r *RateLimiter) Refund(key string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	st, ok := r.buckets[key]
	if !ok || st.count == 0 {
		return
	}
	st.count--
}

// sweep drops the buckets whose window ended, at most once a window, so it
// costs nothing per event on average. The caller holds the lock.
func (r *RateLimiter) sweep(now time.Time) {
	if now.Sub(r.swept) < r.window {
		return
	}
	for k, st := range r.buckets {
		if now.After(st.resetAt) {
			delete(r.buckets, k)
		}
	}
	r.swept = now
}

// size is how many buckets the limiter holds.
func (r *RateLimiter) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.buckets)
}

// AddressBucket is the rate-limit key of a client address: an IPv4 address
// (or an IPv4-mapped IPv6 one) as is, and an IPv6 address by its /64, the
// smallest block one subscriber is usually given, so rotating addresses
// within it does not buy a fresh budget. Anything that does not parse is its
// own key.
func AddressBucket(ip string) string {
	return AddressBlock(ip, 64)
}

// AddressBlock is AddressBucket with the IPv6 prefix length v6Bits: an IPv4
// address as is, an IPv6 one by its /v6Bits, anything else as is.
func AddressBlock(ip string, v6Bits int) string {
	parsed := net.ParseIP(ip)
	if parsed == nil {
		return ip
	}
	if v4 := parsed.To4(); v4 != nil {
		return v4.String()
	}
	mask := net.CIDRMask(v6Bits, 128)
	return (&net.IPNet{IP: parsed.Mask(mask), Mask: mask}).String()
}
