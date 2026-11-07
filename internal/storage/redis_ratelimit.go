package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisRateCounter is the auth.SharedCounter of the API replicas on Redis
// (#2357): one INCR and EXPIRE per counted event, in one round trip, so every
// replica behind the Service spends the same budget.
type RedisRateCounter struct {
	client *redis.Client
}

// NewRedisRateCounter builds the shared rate limit counter over client.
func NewRedisRateCounter(client *redis.Client) *RedisRateCounter {
	return &RedisRateCounter{client: client}
}

// decrFloor takes one event back and never goes below zero, in one step.
var decrFloor = redis.NewScript(`
local v = tonumber(redis.call('GET', KEYS[1]) or '0')
if v > 0 then return redis.call('DECR', KEYS[1]) end
return 0`)

// Incr counts one event of key and returns the count. key expires after ttl,
// which outlives the window the key names, since the key is created inside
// it.
func (r *RedisRateCounter) Incr(ctx context.Context, key string, ttl time.Duration) (int64, error) {
	pipe := r.client.TxPipeline()
	incr := pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, ttl)
	if _, err := pipe.Exec(ctx); err != nil {
		return 0, fmt.Errorf("counting rate limit event: %w", err)
	}
	return incr.Val(), nil
}

// Get returns the count of key, 0 when it has none.
func (r *RedisRateCounter) Get(ctx context.Context, key string) (int64, error) {
	n, err := r.client.Get(ctx, key).Int64()
	if errors.Is(err, redis.Nil) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("reading rate limit count: %w", err)
	}
	return n, nil
}

// Decr gives one event of key back.
func (r *RedisRateCounter) Decr(ctx context.Context, key string) error {
	if err := decrFloor.Run(ctx, r.client, []string{key}).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return fmt.Errorf("refunding rate limit event: %w", err)
	}
	return nil
}
