package auth

import (
	"context"
	"log/slog"
	"net"
	"strconv"
	"sync"
	"time"
)

// SharedCounter is a counter every API replica reaches, such as Redis, that
// a RateLimiter shares its budget through (#2357). Incr adds one event to key
// and returns the count, expiring key after ttl; Get returns the count; Decr
// takes one event back without going below zero.
type SharedCounter interface {
	Incr(ctx context.Context, key string, ttl time.Duration) (int64, error)
	Get(ctx context.Context, key string) (int64, error)
	Decr(ctx context.Context, key string) error
}

// sharedTimeout bounds one call to the shared counter, so a slow counter
// costs a sign-in at most this long before the limiter falls back to its
// own map.
const sharedTimeout = 250 * time.Millisecond

// RateLimiter is a per-key fixed-window limiter used to throttle failed logins
// per client IP (ADR 0008).
//
// By itself it counts in process, so N API replicas allow N times the limit.
// Share gives it a SharedCounter: the limit then holds across every replica,
// counted in fixed windows aligned to the window length, and when the counter
// fails the limiter falls back to its own map, which keeps the per replica
// bound and locks nobody out.
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

	shared SharedCounter
	scope  string
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

// Share makes the limiter count through counter under scope, which keeps the
// keys of each limiter apart (login, renew, ...), and returns it. A nil
// counter leaves the limiter in process.
func (r *RateLimiter) Share(counter SharedCounter, scope string) *RateLimiter {
	r.shared, r.scope = counter, scope
	return r
}

// sharedKey is the counter key of key in the window that holds now:
// rl:{scope}:{key}:{window index}.
func (r *RateLimiter) sharedKey(key string, now time.Time) string {
	return "rl:" + r.scope + ":" + key + ":" + itoa(now.UnixNano()/int64(r.window))
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// sharedCall runs fn against the shared counter with sharedTimeout, and
// reports false when there is no counter or it failed, so the caller uses
// the in-process count.
func (r *RateLimiter) sharedCall(op string, fn func(ctx context.Context, key string) error, key string) bool {
	if r.shared == nil {
		return false
	}
	ctx, cancel := context.WithTimeout(context.Background(), sharedTimeout)
	defer cancel()
	if err := fn(ctx, r.sharedKey(key, r.now())); err != nil {
		slog.Warn("shared rate limit unavailable, counting in process", "scope", r.scope, "op", op, "error", err)
		return false
	}
	return true
}

// Blocked reports whether key has already reached its limit in the current
// window, WITHOUT recording an attempt (a peek). The login handler uses it to
// reject an over-limit caller up front while calling Allow only for actual
// failures — so a successful login never consumes the budget and a user who
// mistypes a few times is not locked out the moment they finally get it right.
func (r *RateLimiter) Blocked(key string) bool {
	var n int64
	if r.sharedCall("get", func(ctx context.Context, k string) (err error) {
		n, err = r.shared.Get(ctx, k)
		return err
	}, key) {
		return n >= int64(r.limit)
	}
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
	var n int64
	if r.sharedCall("incr", func(ctx context.Context, k string) (err error) {
		n, err = r.shared.Incr(ctx, k, r.window)
		return err
	}, key) {
		return n <= int64(r.limit)
	}
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
	if r.sharedCall("decr", func(ctx context.Context, k string) error {
		return r.shared.Decr(ctx, k)
	}, key) {
		return
	}
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
