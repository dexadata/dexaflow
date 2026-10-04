package xcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisBackend is the production XCom Backend: it stores each entry as a JSON
// envelope under its key with a TTL, so Redis expires values natively (ADR 0006).
type RedisBackend struct {
	client *redis.Client
}

// NewRedisBackend builds a RedisBackend over the given go-redis client.
func NewRedisBackend(client *redis.Client) *RedisBackend {
	return &RedisBackend{client: client}
}

// Push stores the entry under key with the given TTL.
func (b *RedisBackend) Push(ctx context.Context, key string, entry Entry, ttl time.Duration) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return fmt.Errorf("encoding xcom entry: %w", err)
	}
	if err := b.client.Set(ctx, key, data, ttl).Err(); err != nil {
		return fmt.Errorf("writing xcom to redis: %w", err)
	}
	return nil
}

// Fetch returns the entry at key, or ErrNotFound when it is absent or expired.
func (b *RedisBackend) Fetch(ctx context.Context, key string) (Entry, error) {
	raw, err := b.client.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("reading xcom from redis: %w", err)
	}
	var entry Entry
	if uerr := json.Unmarshal(raw, &entry); uerr != nil {
		return Entry{}, fmt.Errorf("decoding xcom entry: %w", uerr)
	}
	return entry, nil
}

// FetchMany reads every key with one MGET, in order. Absent or expired keys
// come back as not found.
func (b *RedisBackend) FetchMany(ctx context.Context, keys []string) ([]Entry, []bool, error) {
	entries := make([]Entry, len(keys))
	found := make([]bool, len(keys))
	if len(keys) == 0 {
		return entries, found, nil
	}
	vals, err := b.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, nil, fmt.Errorf("reading xcom from redis: %w", err)
	}
	for i, v := range vals {
		raw, ok := v.(string)
		if !ok {
			continue
		}
		if uerr := json.Unmarshal([]byte(raw), &entries[i]); uerr != nil {
			return nil, nil, fmt.Errorf("decoding xcom entry: %w", uerr)
		}
		found[i] = true
	}
	return entries, found, nil
}

// Delete removes the entry at key.
func (b *RedisBackend) Delete(ctx context.Context, key string) error {
	if err := b.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("deleting xcom from redis: %w", err)
	}
	return nil
}

// List returns the keys matching the glob pattern, using SCAN to avoid blocking.
func (b *RedisBackend) List(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	iter := b.client.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scanning xcom keys: %w", err)
	}
	return keys, nil
}
