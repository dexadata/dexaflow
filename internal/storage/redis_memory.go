package storage

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"
)

// RedisSafeEvictionPolicy is the only maxmemory-policy under which Redis never
// deletes a key to free memory. XCom values carry a TTL, so a volatile-*
// policy evicts exactly them first, and an allkeys-* policy evicts them too
// (#2354).
const RedisSafeEvictionPolicy = "noeviction"

// RedisMemoryInfo is what the server reports about its memory and evictions.
type RedisMemoryInfo struct {
	// Policy is maxmemory_policy, for example noeviction or volatile-lru.
	Policy string
	// UsedMemory is used_memory, in bytes.
	UsedMemory int64
	// EvictedKeys is evicted_keys: keys the server deleted to stay under
	// maxmemory since it started.
	EvictedKeys int64
}

// EvictionSafe reports whether the policy keeps every key until it expires.
func (i RedisMemoryInfo) EvictionSafe() bool {
	return i.Policy == RedisSafeEvictionPolicy
}

// MemoryInfo reads the eviction policy, the used memory and the evicted keys
// from INFO, which managed services allow where CONFIG GET is blocked. The
// sections are read one call each because Redis before 7 takes one section
// per INFO.
func (r *Redis) MemoryInfo(ctx context.Context) (RedisMemoryInfo, error) {
	mem, err := r.Client.Info(ctx, "memory").Result()
	if err != nil {
		return RedisMemoryInfo{}, fmt.Errorf("redis INFO memory: %w", err)
	}
	stats, err := r.Client.Info(ctx, "stats").Result()
	if err != nil {
		return RedisMemoryInfo{}, fmt.Errorf("redis INFO stats: %w", err)
	}
	fields := parseRedisInfo(mem + "\n" + stats)
	policy, ok := fields["maxmemory_policy"]
	if !ok {
		return RedisMemoryInfo{}, fmt.Errorf("redis INFO memory has no maxmemory_policy")
	}
	info := RedisMemoryInfo{Policy: policy}
	info.UsedMemory, _ = strconv.ParseInt(fields["used_memory"], 10, 64)   //nolint:errcheck // absent reads as 0
	info.EvictedKeys, _ = strconv.ParseInt(fields["evicted_keys"], 10, 64) //nolint:errcheck // absent reads as 0
	return info, nil
}

// parseRedisInfo turns the INFO text format (`# Section` headers and
// `key:value` lines) into a map.
func parseRedisInfo(text string) map[string]string {
	out := map[string]string{}
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		out[k] = v
	}
	return out
}

// RedisMemoryMetrics receives what WatchRedisMemory reads from INFO. It is a
// local interface so this package does not import internal/observability.
type RedisMemoryMetrics interface {
	SetRedisMemory(evictionSafe bool, usedBytes, evictedKeys int64)
}

// ErrRedisEvictionUnsafe is returned by CheckRedisEviction when
// redis.require_noeviction is on and the server may evict keys, or its policy
// cannot be read.
var ErrRedisEvictionUnsafe = errors.New("redis may evict keys")

// CheckRedisEviction is the boot check of the eviction policy (#2354). A
// policy other than noeviction lets Redis delete the XCom values of running
// DAGs under memory pressure, and the downstream task then fails with "not
// found". It logs a warning that names the risk, or, when require is set
// (redis.require_noeviction), returns ErrRedisEvictionUnsafe so the process
// refuses to boot. A policy that cannot be read is a warning, or a refusal in
// strict mode: strict mode fails closed.
func CheckRedisEviction(ctx context.Context, r *Redis, require bool, logger *slog.Logger) (RedisMemoryInfo, error) {
	info, err := r.MemoryInfo(ctx)
	if err != nil {
		if require {
			return info, fmt.Errorf("%w: cannot read maxmemory_policy (redis.require_noeviction is on): %w", ErrRedisEvictionUnsafe, err)
		}
		logger.Warn("redis: cannot read the eviction policy; if it is not noeviction, Redis may delete XCom values of running DAGs under memory pressure",
			"error", err)
		return info, nil
	}
	if info.EvictionSafe() {
		logger.Info("redis: eviction policy is noeviction", "maxmemory_policy", info.Policy)
		return info, nil
	}
	if require {
		return info, fmt.Errorf("%w: maxmemory_policy is %s, redis.require_noeviction needs %s",
			ErrRedisEvictionUnsafe, info.Policy, RedisSafeEvictionPolicy)
	}
	logger.Warn(unsafePolicyMessage, "maxmemory_policy", info.Policy, "want", RedisSafeEvictionPolicy)
	return info, nil
}

const unsafePolicyMessage = "redis: the eviction policy is not noeviction; under memory pressure Redis deletes XCom values of running DAGs " +
	"and their downstream tasks fail with not found. Set maxmemory-policy noeviction"

// WatchRedisMemory reads INFO every interval, publishes the eviction policy,
// used memory and evicted keys to m, and logs a warning when the policy turns
// unsafe after the first read (the boot check, CheckRedisEviction, already
// reported the policy it found). The returned function stops it.
func WatchRedisMemory(ctx context.Context, r *Redis, m RedisMemoryMetrics, logger *slog.Logger, interval time.Duration) func() {
	if r == nil || m == nil {
		return func() {}
	}
	safe, first := true, true
	scrape := func() {
		info, err := r.MemoryInfo(ctx)
		if err != nil {
			// Unreadable: leave the gauges at their last values. The
			// command failure counter already sees the failed INFO.
			return
		}
		m.SetRedisMemory(info.EvictionSafe(), info.UsedMemory, info.EvictedKeys)
		if !first && safe && !info.EvictionSafe() {
			logger.Warn(unsafePolicyMessage, "maxmemory_policy", info.Policy, "want", RedisSafeEvictionPolicy)
		}
		safe, first = info.EvictionSafe(), false
	}
	scrape()
	watchCtx, cancel := context.WithCancel(ctx)
	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-t.C:
				scrape()
			}
		}
	}()
	return cancel
}
