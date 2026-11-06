package xcom

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisBackend is the production XCom Backend: it stores each entry as a JSON
// envelope under its key with a TTL, so Redis expires values natively (ADR 0006).
// The client is a single node, a Sentinel-managed master or a Redis Cluster
// (#2358).
type RedisBackend struct {
	client redis.UniversalClient
	// cluster is the client of a Redis Cluster, whose keys spread over hash
	// slots on several nodes; nil otherwise.
	cluster *redis.ClusterClient
}

// NewRedisBackend builds a RedisBackend over the given go-redis client.
func NewRedisBackend(client redis.UniversalClient) *RedisBackend {
	b := &RedisBackend{client: client}
	if c, ok := client.(*redis.ClusterClient); ok {
		b.cluster = c
	}
	return b
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
func (b *RedisBackend) FetchMany(ctx context.Context, keys []string) (entries []Entry, found []bool, err error) {
	entries = make([]Entry, len(keys))
	found = make([]bool, len(keys))
	if len(keys) == 0 {
		return entries, found, nil
	}
	vals, err := b.getMany(ctx, keys)
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

// getMany reads keys in order: one MGET on a single node, and on a Cluster a
// pipeline of GETs, which the client sends as one batch per node, since an
// MGET across hash slots is refused (CROSSSLOT). The keys keep their format,
// so values pushed before a move to a Cluster stay readable.
func (b *RedisBackend) getMany(ctx context.Context, keys []string) ([]any, error) {
	if b.cluster == nil {
		return b.client.MGet(ctx, keys...).Result()
	}
	cmds := make([]*redis.StringCmd, len(keys))
	if _, err := b.cluster.Pipelined(ctx, func(p redis.Pipeliner) error {
		for i, k := range keys {
			cmds[i] = p.Get(ctx, k)
		}
		return nil
	}); err != nil && !errors.Is(err, redis.Nil) {
		return nil, err
	}
	vals := make([]any, len(keys))
	for i, c := range cmds {
		v, err := c.Result()
		switch {
		case errors.Is(err, redis.Nil):
		case err != nil:
			return nil, err
		default:
			vals[i] = v
		}
	}
	return vals, nil
}

// Delete removes the entry at key.
func (b *RedisBackend) Delete(ctx context.Context, key string) error {
	if err := b.client.Del(ctx, key).Err(); err != nil {
		return fmt.Errorf("deleting xcom from redis: %w", err)
	}
	return nil
}

// List returns the keys matching the glob pattern, using SCAN to avoid
// blocking; on a Cluster it scans every master, since each holds its own
// slots.
func (b *RedisBackend) List(ctx context.Context, pattern string) ([]string, error) {
	if b.cluster == nil {
		return scanKeys(ctx, b.client, pattern)
	}
	var (
		mu   sync.Mutex
		keys []string
	)
	err := b.cluster.ForEachMaster(ctx, func(ctx context.Context, node *redis.Client) error {
		got, err := scanKeys(ctx, node, pattern)
		mu.Lock()
		keys = append(keys, got...)
		mu.Unlock()
		return err
	})
	if err != nil {
		return nil, err
	}
	return keys, nil
}

func scanKeys(ctx context.Context, c redis.Cmdable, pattern string) ([]string, error) {
	var keys []string
	iter := c.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		keys = append(keys, iter.Val())
	}
	if err := iter.Err(); err != nil {
		return nil, fmt.Errorf("scanning xcom keys: %w", err)
	}
	return keys, nil
}
