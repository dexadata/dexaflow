---
title: "pkg/xcomstore"
linkTitle: "pkg/xcomstore"
weight: 18
---

```go
import "github.com/dexadata/dexaflow/pkg/xcomstore"
```

Package xcomstore is the contract of the XCom value store \(ADR 0099\): where the control plane keeps the small payloads tasks pass to each other \(ADR 0006\), for as long as their time to live.

The server checks the size limit and the declared schema of a value before it reaches the [Backend](<#Backend>), and records the metadata of each value in Postgres for listing and expiry, so a backend only stores bytes under a key and forgets them once their time to live has passed.

A [Backend](<#Backend>) that is also a [BatchBackend](<#BatchBackend>) reads several keys in one round trip, which the server uses when a task pulls many values at once; any other backend is read one key at a time.

Everything exported here follows the pkg/ compatibility rule of ADR 0099: no breaking change in a patch release.

## Index

- [Variables](<#variables>)
- [type Backend](<#Backend>)
- [type BatchBackend](<#BatchBackend>)
- [type Entry](<#Entry>)


## Variables

<a name="ErrNotFound"></a>ErrNotFound is the error, or the wrapped error, a [Backend](<#Backend>) returns from Fetch for a key that is absent or expired.

```go
var ErrNotFound = errors.New("xcom not found")
```

<a name="Backend"></a>
## type [Backend](<https://github.com/dexadata/dexaflow/blob/main/pkg/xcomstore/xcomstore.go#L49-L54>)

Backend stores XCom entries with a time to live.

A key has the form xcom:\{tenant\_id\}:\{dag\_id\}:\{run\_id\}:\{task\_id\}:\{name\}, and the server builds it; a backend treats it as an opaque string. Push replaces any entry under the key, and the entry expires ttl after the push. Fetch returns [ErrNotFound](<#ErrNotFound>) for a key that is absent or expired, and Delete of such a key is not an error. List returns the unexpired keys that match a Redis style glob pattern, where \* matches any run of characters and ? one character.

```go
type Backend interface {
    Push(ctx context.Context, key string, entry Entry, ttl time.Duration) error
    Fetch(ctx context.Context, key string) (Entry, error)
    Delete(ctx context.Context, key string) error
    List(ctx context.Context, pattern string) ([]string, error)
}
```

<details><summary>Example</summary>
<p>

A backend that keeps values in memory: a value is found until its time to live passes, and then Fetch answers ErrNotFound.

```go
package main

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/pkg/xcomstore"
)

// memoryBackend keeps XCom values in memory until they expire.
type memoryBackend struct {
	mu      sync.Mutex
	now     func() time.Time
	entries map[string]stored
}

type stored struct {
	entry   xcomstore.Entry
	expires time.Time
}

func (m *memoryBackend) Push(_ context.Context, key string, e xcomstore.Entry, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries[key] = stored{entry: e, expires: m.now().Add(ttl)}
	return nil
}

func (m *memoryBackend) Fetch(_ context.Context, key string) (xcomstore.Entry, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.entries[key]
	if !ok || !m.now().Before(s.expires) {
		return xcomstore.Entry{}, fmt.Errorf("%s: %w", key, xcomstore.ErrNotFound)
	}
	return s.entry, nil
}

func (m *memoryBackend) Delete(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.entries, key)
	return nil
}

// List matches with path.Match, whose * and ? behave as the glob of the
// contract for keys that hold no '/'.
func (m *memoryBackend) List(_ context.Context, pattern string) ([]string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var keys []string
	for k, s := range m.entries {
		if ok, _ := path.Match(pattern, k); ok && m.now().Before(s.expires) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return keys, nil
}

// A backend that keeps values in memory: a value is found until its time to
// live passes, and then Fetch answers ErrNotFound.
func main() {
	now := time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)
	var b xcomstore.Backend = &memoryBackend{now: func() time.Time { return now }, entries: map[string]stored{}}
	ctx := context.Background()
	key := "xcom:t-1:etl:r-1:extract:row_count"

	_ = b.Push(ctx, key, xcomstore.Entry{Value: []byte("42"), ContentType: "application/json", SizeBytes: 2, CreatedAt: now}, time.Hour)
	e, _ := b.Fetch(ctx, key)
	fmt.Println(string(e.Value))
	keys, _ := b.List(ctx, "xcom:t-1:etl:r-1:*")
	fmt.Println(keys)

	now = now.Add(2 * time.Hour)
	_, err := b.Fetch(ctx, key)
	fmt.Println(errors.Is(err, xcomstore.ErrNotFound))
}
```

#### Output

```
42
[xcom:t-1:etl:r-1:extract:row_count]
true
```

</p>
</details>

<a name="BatchBackend"></a>
## type [BatchBackend](<https://github.com/dexadata/dexaflow/blob/main/pkg/xcomstore/xcomstore.go#L60-L62>)

BatchBackend is an optional interface of a [Backend](<#Backend>) that reads several keys in one round trip. entries and found have the length of keys, and found\[i\] is false when keys\[i\] is absent or expired; err is for a failure of the whole read.

```go
type BatchBackend interface {
    FetchMany(ctx context.Context, keys []string) (entries []Entry, found []bool, err error)
}
```

<a name="Entry"></a>
## type [Entry](<https://github.com/dexadata/dexaflow/blob/main/pkg/xcomstore/xcomstore.go#L29-L38>)

Entry is a stored XCom value together with its metadata.

```go
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
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
