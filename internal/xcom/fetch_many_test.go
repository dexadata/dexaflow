package xcom

import (
	"context"
	"errors"
	"testing"
)

// batchingBackend is a fakeBackend that also reads several keys in one call.
type batchingBackend struct {
	*fakeBackend
	batches int
	fetches int
	err     error
}

func (b *batchingBackend) Fetch(ctx context.Context, key string) (Entry, error) {
	b.fetches++
	return b.fakeBackend.Fetch(ctx, key)
}

func (b *batchingBackend) FetchMany(_ context.Context, keys []string) ([]Entry, []bool, error) {
	b.batches++
	if b.err != nil {
		return nil, nil, b.err
	}
	entries := make([]Entry, len(keys))
	found := make([]bool, len(keys))
	for i, k := range keys {
		entries[i], found[i] = b.entries[k]
	}
	return entries, found, nil
}

func manyKeys() []Key {
	a, b, c := testKey(), testKey(), testKey()
	b.TaskID = "transform"
	c.TaskID = "missing"
	return []Key{a, b, c}
}

func checkMany(t *testing.T, got []Result) {
	t.Helper()
	if len(got) != 3 {
		t.Fatalf("got %d results, want 3", len(got))
	}
	if !got[0].Found || string(got[0].Entry.Value) != `1` {
		t.Errorf("result 0 = %+v, want found 1", got[0])
	}
	if !got[1].Found || string(got[1].Entry.Value) != `2` {
		t.Errorf("result 1 = %+v, want found 2", got[1])
	}
	if got[2].Found {
		t.Errorf("result 2 = %+v, want not found", got[2])
	}
}

func seed(b *fakeBackend) {
	keys := manyKeys()
	b.entries[keys[0].String()] = Entry{Value: []byte(`1`)}
	b.entries[keys[1].String()] = Entry{Value: []byte(`2`)}
}

// TestFetchManyUsesOneBackendRead: a backend that can read several keys at
// once (Redis MGET, one Postgres query) is asked once for the whole batch.
func TestFetchManyUsesOneBackendRead(t *testing.T) {
	b := &batchingBackend{fakeBackend: newFakeBackend()}
	seed(b.fakeBackend)
	got, err := newService(b, &fakeIndex{}).FetchMany(context.Background(), manyKeys())
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	checkMany(t, got)
	if b.batches != 1 || b.fetches != 0 {
		t.Errorf("backend reads = %d batches + %d single, want 1 + 0", b.batches, b.fetches)
	}
}

// TestFetchManyFallsBackToFetch: a backend without a batch read is read one
// key at a time with the same results.
func TestFetchManyFallsBackToFetch(t *testing.T) {
	b := newFakeBackend()
	seed(b)
	got, err := newService(b, &fakeIndex{}).FetchMany(context.Background(), manyKeys())
	if err != nil {
		t.Fatalf("FetchMany: %v", err)
	}
	checkMany(t, got)
}

func TestFetchManyPropagatesBackendErrors(t *testing.T) {
	b := &batchingBackend{fakeBackend: newFakeBackend(), err: errors.New("redis down")}
	if _, err := newService(b, &fakeIndex{}).FetchMany(context.Background(), manyKeys()); err == nil {
		t.Fatal("FetchMany hid a backend error")
	}
}
