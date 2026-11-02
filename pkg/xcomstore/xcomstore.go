// Package xcomstore is the contract of the XCom value store (ADR 0099): where
// the control plane keeps the small payloads tasks pass to each other (ADR
// 0006), for as long as their time to live.
//
// The server checks the size limit and the declared schema of a value before
// it reaches the [Backend], and records the metadata of each value in
// Postgres for listing and expiry, so a backend only stores bytes under a key
// and forgets them once their time to live has passed.
//
// A [Backend] that is also a [BatchBackend] reads several keys in one round
// trip, which the server uses when a task pulls many values at once; any
// other backend is read one key at a time.
//
// Everything exported here follows the pkg/ compatibility rule of ADR 0099:
// no breaking change in a patch release.
package xcomstore

import (
	"context"
	"errors"
	"time"
)

// ErrNotFound is the error, or the wrapped error, a [Backend] returns from
// Fetch for a key that is absent or expired.
var ErrNotFound = errors.New("xcom not found")

// Entry is a stored XCom value together with its metadata.
type Entry struct {
	// Value is the payload, at most 256 KiB.
	Value []byte `json:"value"`
	// ContentType is the media type the task declared for Value.
	ContentType string `json:"content_type"`
	// SizeBytes is the size of Value in bytes.
	SizeBytes int `json:"size_bytes"`
	// CreatedAt is when the value was pushed.
	CreatedAt time.Time `json:"created_at"`
}

// Backend stores XCom entries with a time to live.
//
// A key has the form xcom:{tenant_id}:{dag_id}:{run_id}:{task_id}:{name}, and
// the server builds it; a backend treats it as an opaque string. Push replaces
// any entry under the key, and the entry expires ttl after the push. Fetch
// returns [ErrNotFound] for a key that is absent or expired, and Delete of
// such a key is not an error. List returns the unexpired keys that match a
// Redis style glob pattern, where * matches any run of characters and ? one
// character.
type Backend interface {
	Push(ctx context.Context, key string, entry Entry, ttl time.Duration) error
	Fetch(ctx context.Context, key string) (Entry, error)
	Delete(ctx context.Context, key string) error
	List(ctx context.Context, pattern string) ([]string, error)
}

// BatchBackend is an optional interface of a [Backend] that reads several
// keys in one round trip. entries and found have the length of keys, and
// found[i] is false when keys[i] is absent or expired; err is for a failure of
// the whole read.
type BatchBackend interface {
	FetchMany(ctx context.Context, keys []string) (entries []Entry, found []bool, err error)
}
