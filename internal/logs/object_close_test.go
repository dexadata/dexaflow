package logs

import (
	"context"
	"errors"
	"io"
	"testing"
)

type closingStore struct {
	*memStore
	closed int
	err    error
}

func (c *closingStore) Close() error {
	c.closed++
	return c.err
}

// TestObjectSinkCloseReleasesTheStoreClient pins the Close seam of #647: the
// GCS client holds an HTTP/2 connection pool, and the sink is how the server
// reaches it at shutdown. A store without Close is left alone.
func TestObjectSinkCloseReleasesTheStoreClient(t *testing.T) {
	store := &closingStore{memStore: newMemStore()}
	sink := NewObjectSink(context.Background(), store, "p", nil)
	var _ io.Closer = sink
	if err := sink.Close(); err != nil || store.closed != 1 {
		t.Fatalf("Close = %v with %d store closes, want nil and 1", err, store.closed)
	}

	store.err = errors.New("boom")
	if err := sink.Close(); !errors.Is(err, store.err) {
		t.Fatalf("Close = %v, want the store's error", err)
	}

	if err := NewObjectSink(context.Background(), newMemStore(), "p", nil).Close(); err != nil {
		t.Fatalf("Close over a store without Close = %v, want nil", err)
	}
}

func TestGCSStoreCloses(t *testing.T) {
	store, err := NewGCSStore(context.Background(), GCSConfig{Bucket: "task-logs", CredentialsFile: writeFakeServiceAccount(t)})
	if err != nil {
		t.Fatalf("NewGCSStore: %v", err)
	}
	var c io.Closer = store
	if err := c.Close(); err != nil {
		t.Fatalf("Close = %v", err)
	}
}
