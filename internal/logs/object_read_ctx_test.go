package logs

import (
	"context"
	"io"
	"sync"
	"testing"
)

type requestKey struct{}

// ctxStore records, for every store call, whether it carried the request's
// context, so a read can be checked to cancel its cloud calls with the client.
type ctxStore struct {
	*memStore
	noteMu  sync.Mutex
	calls   int
	foreign []string // keys and prefixes reached under another context
}

func (c *ctxStore) note(ctx context.Context, what string) {
	c.noteMu.Lock()
	defer c.noteMu.Unlock()
	c.calls++
	if ctx.Value(requestKey{}) == nil {
		c.foreign = append(c.foreign, what)
	}
}

func (c *ctxStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	c.note(ctx, key)
	return c.memStore.Get(ctx, key)
}

func (c *ctxStore) List(ctx context.Context, prefix, _ string) ([]string, error) {
	c.note(ctx, prefix)
	var keys []string
	c.mu.Lock()
	defer c.mu.Unlock()
	for k := range c.objs {
		if len(k) >= len(prefix) && k[:len(prefix)] == prefix {
			keys = append(keys, k)
		}
	}
	return keys, nil
}

// TestObjectSinkReadUsesTheRequestContext pins #647: a log read used the
// sink's server-lifetime context, so a client that went away left its cloud
// GETs running. Every store call of the read path, the listing, the first
// probe and each later segment, now carries the caller's context.
func TestObjectSinkReadUsesTheRequestContext(t *testing.T) {
	for _, layout := range []string{ObjectLayoutSingle, ObjectLayoutSegmented} {
		t.Run(layout, func(t *testing.T) {
			store := &ctxStore{memStore: newMemStore()}
			sink := NewObjectSink(context.Background(), store, "p", nil, WithObjectLayout(layout))
			ref := sampleRef()
			store.objs[sink.segmentKey(ref, 0)] = []byte("one\n")
			store.objs[sink.segmentKey(ref, 1)] = []byte("two\n")
			store.objs[sink.key(ref)] = []byte("marker\n")

			ctx := context.WithValue(context.Background(), requestKey{}, true)
			rc, err := ReadAttempts(ctx, sink, ref, TryEpochs{})
			if err != nil {
				t.Fatalf("ReadAttempts: %v", err)
			}
			if _, err := io.ReadAll(rc); err != nil {
				t.Fatalf("reading: %v", err)
			}
			if err := rc.Close(); err != nil {
				t.Fatalf("close: %v", err)
			}
			if store.calls == 0 {
				t.Fatal("the read made no store call")
			}
			if len(store.foreign) > 0 {
				t.Fatalf("store calls without the request context: %v", store.foreign)
			}
		})
	}
}

// TestObjectSinkReadStopsWithTheRequest: a canceled request reaches the store
// as a canceled context, which is what lets the SDK abort the GET.
func TestObjectSinkReadStopsWithTheRequest(t *testing.T) {
	store := &ctxStore{memStore: newMemStore()}
	canceled := &cancelSeen{ctxStore: store}
	sink := NewObjectSink(context.Background(), canceled, "p", nil)
	store.objs[sink.key(sampleRef())] = []byte("line\n")

	ctx, cancel := context.WithCancel(context.WithValue(context.Background(), requestKey{}, true))
	cancel()
	_, _ = sink.Read(ctx, sampleRef()) //nolint:errcheck // the context reaching the store is what is checked
	if !canceled.seen {
		t.Fatal("the store's Get did not see the request's cancellation")
	}
}

type cancelSeen struct {
	*ctxStore
	seen bool
}

func (c *cancelSeen) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if ctx.Err() != nil {
		c.seen = true
	}
	return c.ctxStore.Get(ctx, key)
}
