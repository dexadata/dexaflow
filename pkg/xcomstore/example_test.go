package xcomstore_test

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
func ExampleBackend() {
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
	// Output:
	// 42
	// [xcom:t-1:etl:r-1:extract:row_count]
	// true
}
