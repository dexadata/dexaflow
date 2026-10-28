package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
)

// memStore is an in-memory ObjectStore for exercising ObjectSink without a real
// bucket. It records every Put so tests can assert the key layout.
type memStore struct {
	mu     sync.Mutex
	objs   map[string][]byte
	puts   []int // body size of every Put, in order
	putErr error
	getErr error
}

func newMemStore() *memStore { return &memStore{objs: map[string][]byte{}} }

func (m *memStore) Put(_ context.Context, key string, r io.Reader) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.putErr != nil {
		return m.putErr
	}
	b, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	m.objs[key] = b
	m.puts = append(m.puts, len(b))
	return nil
}

// setPutErr swaps the injected Put failure under the lock, so a test can flip it
// while a writer's flusher goroutine is running.
func (m *memStore) setPutErr(err error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.putErr = err
}

// object returns a copy of the stored body for key and whether it exists.
func (m *memStore) object(key string) ([]byte, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.objs[key]
	return append([]byte(nil), b...), ok
}

// putCount reports how many Puts succeeded so far.
func (m *memStore) putCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.puts)
}

// Get reads under the same lock Put takes, and the WHOLE body is inside it —
// getErr included. A writer's flusher goroutine Puts concurrently with the
// test's own reads, so a field left outside the lock makes the fake itself the
// source of a race report, pointing the detector at the wrong file. This is
// also what lets a future failure case flip getErr mid-test the way setPutErr
// already does.
func (m *memStore) Get(_ context.Context, key string) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.getErr != nil {
		return nil, m.getErr
	}
	b, ok := m.objs[key]
	if !ok {
		return nil, ErrObjectNotFound
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

func sampleRef() Ref {
	return Ref{TenantID: "t1", DagID: "dag", RunID: "scheduled__2026-07-30T12:00:00+00:00", TaskID: "task", TryNumber: 1}
}

func TestObjectSinkRoundTrip(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "logs", nil)
	ref := sampleRef()

	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	events := []Event{
		{Level: "info", Stream: "stdout", Message: "starting"},
		{Level: "error", Stream: "stderr", Message: "boom"},
	}
	for _, ev := range events {
		if werr := w.WriteEvent(ev); werr != nil {
			t.Fatalf("WriteEvent() error = %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}

	rc, err := sink.Read(t.Context(), ref)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != len(events) {
		t.Fatalf("got %d lines, want %d: %q", len(lines), len(events), string(raw))
	}
	first := DecodeLine(lines[0])
	if first.Message != "starting" || first.Level != "info" {
		t.Errorf("first line = %+v, want starting/info", first)
	}
	second := DecodeLine(lines[1])
	if second.Message != "boom" || second.Level != "error" {
		t.Errorf("second line = %+v, want boom/error", second)
	}
}

// TestObjectSinkAppendEventPreservesExisting is the #861 regression: over an
// object store (no native append), adding a marker MUST read-modify-write, not
// overwrite the agent's streamed log with a marker-only object. This is the
// EKS/S3 path the disk-sink tests never exercise.
func TestObjectSinkAppendEventPreservesExisting(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "logs", nil)
	ref := sampleRef()

	// The agent streamed two lines and its object was Put.
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for _, ev := range []Event{{Level: "info", Message: "starting"}, {Level: "info", Message: "Running with dbt=1.12.3"}} {
		if werr := w.WriteEvent(ev); werr != nil {
			t.Fatalf("WriteEvent() error = %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}

	// The reaper appends a marker via the MarkerSink seam.
	if aerr := sink.AppendEvent(ref, Event{Level: "error", Stream: "system", Message: "killed: agent_lost (last heartbeat ...)"}); aerr != nil {
		t.Fatalf("AppendEvent() error = %v", aerr)
	}

	rc, err := sink.Read(t.Context(), ref)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, _ := io.ReadAll(rc)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("want 3 lines (2 streamed + marker), got %d: %q", len(lines), string(raw))
	}
	if got := DecodeLine(lines[0]).Message; got != "starting" {
		t.Errorf("first line = %q, want the agent's original output preserved", got)
	}
	if got := DecodeLine(lines[2]).Message; !strings.Contains(got, "agent_lost") {
		t.Errorf("last line = %q, want the appended agent_lost marker", got)
	}
}

// TestObjectSinkAppendEventCreatesWhenAbsent: appending to a ref with no prior
// object (the agent Put nothing yet) writes a marker-only object, tolerating the
// not-found rather than erroring.
func TestObjectSinkAppendEventCreatesWhenAbsent(t *testing.T) {
	sink := NewObjectSink(context.Background(), newMemStore(), "", nil)
	ref := sampleRef()
	if err := sink.AppendEvent(ref, Event{Level: "error", Message: "killed: agent_lost"}); err != nil {
		t.Fatalf("AppendEvent(absent) error = %v", err)
	}
	rc, err := sink.Read(t.Context(), ref)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, _ := io.ReadAll(rc)
	if !strings.Contains(string(raw), "agent_lost") {
		t.Errorf("stored object = %q, want the marker", string(raw))
	}
}

func TestObjectSinkKeyLayoutIncludesPrefix(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "acme/logs", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "x"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	wantKey := "acme/logs/t1/dag/scheduled__2026-07-30T12:00:00+00:00/task/1.log"
	if _, ok := store.objs[wantKey]; !ok {
		keys := make([]string, 0, len(store.objs))
		for k := range store.objs {
			keys = append(keys, k)
		}
		t.Fatalf("object not stored at %q; got keys %v", wantKey, keys)
	}
}

func TestObjectSinkRejectsUnsafeRef(t *testing.T) {
	sink := NewObjectSink(context.Background(), newMemStore(), "", nil)
	bad := sampleRef()
	bad.RunID = "../escape"
	if _, err := sink.Open(bad); !errors.Is(err, ErrUnsafeRef) {
		t.Fatalf("Open(unsafe) error = %v, want ErrUnsafeRef", err)
	}
	if _, err := sink.Read(t.Context(), bad); !errors.Is(err, ErrUnsafeRef) {
		t.Fatalf("Read(unsafe) error = %v, want ErrUnsafeRef", err)
	}
}

func TestObjectSinkReadMissing(t *testing.T) {
	sink := NewObjectSink(context.Background(), newMemStore(), "", nil)
	if _, err := sink.Read(t.Context(), sampleRef()); !errors.Is(err, ErrObjectNotFound) {
		t.Fatalf("Read(missing) error = %v, want ErrObjectNotFound", err)
	}
}

func TestObjectSinkClosePropagatesPutError(t *testing.T) {
	store := newMemStore()
	store.setPutErr(errors.New("network down"))
	sink := NewObjectSink(context.Background(), store, "", nil)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "x"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr == nil {
		t.Fatal("Close() error = nil, want the underlying put error")
	}
}

// TestObjectWriterBufferCapFailsLoud pins MEDIUM-1: the object sink buffers a
// whole attempt in the control plane (no append), so it must fail loudly at the
// cap rather than grow memory without bound — while still flushing what it held.
func TestObjectWriterBufferCapFailsLoud(t *testing.T) {
	orig := maxBufferedAttemptBytes
	maxBufferedAttemptBytes = 64
	defer func() { maxBufferedAttemptBytes = orig }()

	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var capErr error
	for i := 0; i < 100 && capErr == nil; i++ {
		capErr = w.WriteEvent(Event{Message: "a log line that pushes us past the tiny cap"})
	}
	if capErr == nil {
		t.Fatal("WriteEvent should fail loudly once the buffer cap is exceeded")
	}
	// Close still flushes the lines buffered before the cap — a partial log beats none.
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close after cap should still flush buffered lines: %v", cerr)
	}
	if _, ok := store.objs[sink.key(sampleRef())]; !ok {
		t.Error("buffered lines before the cap should still be written on Close")
	}
}

func TestDiskSinkImplementsPrunerObjectSinkDoesNot(t *testing.T) {
	var disk Sink = NewDiskSink(t.TempDir())
	if _, ok := disk.(Pruner); !ok {
		t.Error("DiskSink should implement Pruner")
	}
	var obj Sink = NewObjectSink(context.Background(), newMemStore(), "", nil)
	if _, ok := obj.(Pruner); ok {
		t.Error("ObjectSink must not implement Pruner (bucket lifecycle owns retention)")
	}
}

func TestNewDurableSinkDefaultsToDisk(t *testing.T) {
	dir := t.TempDir()
	for _, backend := range []string{"", "disk"} {
		sink, err := NewDurableSink(context.Background(), backend, dir, nil, "", nil)
		if err != nil {
			t.Fatalf("NewDurableSink(%q) error = %v", backend, err)
		}
		if _, ok := sink.(*DiskSink); !ok {
			t.Errorf("NewDurableSink(%q) = %T, want *DiskSink", backend, sink)
		}
	}
}

func TestNewDurableSinkObject(t *testing.T) {
	for _, backend := range []string{"s3", "gcs"} {
		sink, err := NewDurableSink(context.Background(), backend, "", newMemStore(), "p", nil)
		if err != nil {
			t.Fatalf("NewDurableSink(%q) error = %v", backend, err)
		}
		if _, ok := sink.(*ObjectSink); !ok {
			t.Errorf("NewDurableSink(%q) = %T, want *ObjectSink", backend, sink)
		}
	}
}

func TestNewDurableSinkObjectRequiresStore(t *testing.T) {
	for _, backend := range []string{"s3", "gcs"} {
		if _, err := NewDurableSink(context.Background(), backend, "", nil, "", nil); err == nil {
			t.Fatalf("NewDurableSink(%q, nil store) error = nil, want error", backend)
		}
	}
}

func TestNewDurableSinkUnknownBackend(t *testing.T) {
	if _, err := NewDurableSink(context.Background(), "gopher", "", nil, "", nil); err == nil {
		t.Fatal("NewDurableSink(unknown) error = nil, want error")
	}
}
