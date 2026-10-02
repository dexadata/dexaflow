package logs

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

// gatedStore wraps memStore so a test can hold a Put in flight: every Put
// announces itself on entered and then waits for release before storing.
type gatedStore struct {
	*memStore
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func newGatedStore() *gatedStore {
	return &gatedStore{memStore: newMemStore(), entered: make(chan struct{}, 64), release: make(chan struct{})}
}

func (g *gatedStore) Put(ctx context.Context, key string, r io.Reader) error {
	select {
	case g.entered <- struct{}{}:
	default:
	}
	<-g.release
	return g.memStore.Put(ctx, key, r)
}

func (g *gatedStore) open() { g.once.Do(func() { close(g.release) }) }

// setSegmentBytes lowers the segment size for a test and restores it on cleanup.
func setSegmentBytes(t *testing.T, n int) {
	t.Helper()
	orig := objectSegmentBytes
	objectSegmentBytes = n
	t.Cleanup(func() { objectSegmentBytes = orig })
}

// readAll reads an attempt back through the sink's public read path.
func readAll(t *testing.T, sink *ObjectSink, ref Ref) string {
	t.Helper()
	rc, err := sink.Read(ref)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading log back: %v", err)
	}
	return string(b)
}

// messages decodes a JSONL body into its messages, in order.
func messages(body string) []string {
	var out []string
	for _, l := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if l != "" {
			out = append(out, DecodeLine(l).Message)
		}
	}
	return out
}

// TestObjectWriterFlusherPutDoesNotBlockWriter pins the lock scope: the time
// cadence flush used to hold the buffer lock across the network Put, so a slow
// store stalled every WriteEvent and, through it, the agent's log stream. A Put
// in flight must not keep a new line from being buffered.
func TestObjectWriterFlusherPutDoesNotBlockWriter(t *testing.T) {
	setFlushTuning(t, 1<<20, 5*time.Millisecond)
	store := newGatedStore()
	t.Cleanup(store.open)
	sink := NewObjectSink(context.Background(), store, "", nil)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "first"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	select {
	case <-store.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("the flusher never started a Put")
	}
	done := make(chan error, 1)
	go func() { done <- w.WriteEvent(Event{Message: "second"}) }()
	select {
	case werr := <-done:
		if werr != nil {
			t.Fatalf("WriteEvent() error = %v", werr)
		}
	case <-time.After(time.Second):
		t.Fatal("WriteEvent blocked behind the flusher's in-flight Put")
	}
	store.open()
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if got := messages(readAll(t, sink, sampleRef())); strings.Join(got, ",") != "first,second" {
		t.Fatalf("read back %v, want [first second]", got)
	}
}

// TestObjectSinkSegmentedUploadsOnlyTheOpenSegment: with the segmented layout a
// flush uploads the open segment, never the whole accumulated attempt, so the
// bytes uploaded per flush stay bounded by the segment size however long the
// task logs. Reading the attempt back concatenates the segments in order.
func TestObjectSinkSegmentedUploadsOnlyTheOpenSegment(t *testing.T) {
	lineLen := len(EncodeLine(fixedEvent("line 000")) + "\n")
	setFlushTuning(t, 2*lineLen, time.Hour)
	setSegmentBytes(t, 4*lineLen)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	const n = 50
	for i := 0; i < n; i++ {
		if werr := w.WriteEvent(fixedEvent(fmt.Sprintf("line %03d", i))); werr != nil {
			t.Fatalf("WriteEvent(%d) error = %v", i, werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	store.mu.Lock()
	puts := append([]int(nil), store.puts...)
	store.mu.Unlock()
	limit := objectSegmentBytes + objectFlushBytes
	total := 0
	for i, size := range puts {
		if size > limit {
			t.Fatalf("Put #%d uploaded %d bytes, want at most %d (segment plus one flush)", i, size, limit)
		}
		total += size
	}
	if total >= 3*n*lineLen {
		t.Errorf("uploaded %d bytes for a %d-byte log, want well under 3x", total, n*lineLen)
	}
	if _, ok := store.object(sink.key(ref)); ok {
		t.Error("the segmented layout must not write the single-object key")
	}
	got := messages(readAll(t, sink, ref))
	if len(got) != n {
		t.Fatalf("read back %d lines, want %d", len(got), n)
	}
	for i, m := range got {
		if want := fmt.Sprintf("line %03d", i); m != want {
			t.Fatalf("line %d = %q, want %q", i, m, want)
		}
	}
}

// TestObjectSinkSegmentedReadsLegacySingleObject: an attempt stored before the
// layout changed (one object at {try}.log) stays readable forever.
func TestObjectSinkSegmentedReadsLegacySingleObject(t *testing.T) {
	store := newMemStore()
	legacy := NewObjectSink(context.Background(), store, "p", nil)
	ref := sampleRef()
	w, err := legacy.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "old layout"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	seg := NewObjectSink(context.Background(), store, "p", nil, WithObjectLayout(ObjectLayoutSegmented))
	if got := messages(readAll(t, seg, ref)); strings.Join(got, ",") != "old layout" {
		t.Fatalf("read back %v, want the legacy object", got)
	}
}

// TestObjectSinkSingleReadsSegmentedAttempt: turning the gate back off must not
// hide attempts written while it was on.
func TestObjectSinkSingleReadsSegmentedAttempt(t *testing.T) {
	store := newMemStore()
	seg := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	ref := sampleRef()
	w, err := seg.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "segmented"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	single := NewObjectSink(context.Background(), store, "", nil)
	if got := messages(readAll(t, single, ref)); strings.Join(got, ",") != "segmented" {
		t.Fatalf("read back %v, want the segmented attempt", got)
	}
}

// TestObjectSinkSegmentedMarkerFollowsSegments: the reaper's marker lands after
// the streamed output, and never overwrites a segment the live writer owns.
func TestObjectSinkSegmentedMarkerFollowsSegments(t *testing.T) {
	lineLen := len(EncodeLine(fixedEvent("line 0")) + "\n")
	setFlushTuning(t, lineLen, time.Hour)
	setSegmentBytes(t, 2*lineLen)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if werr := w.WriteEvent(fixedEvent(fmt.Sprintf("line %d", i))); werr != nil {
			t.Fatalf("WriteEvent(%d) error = %v", i, werr)
		}
	}
	if aerr := sink.AppendEvent(ref, Event{Message: "agent lost"}); aerr != nil {
		t.Fatalf("AppendEvent() error = %v", aerr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	got := messages(readAll(t, sink, ref))
	want := "line 0,line 1,line 2,line 3,line 4,agent lost"
	if strings.Join(got, ",") != want {
		t.Fatalf("read back %v, want %s", got, want)
	}
}

// TestObjectSinkSegmentedReopenAppends: a second stream for the same attempt
// continues after the segments already stored instead of overwriting them.
func TestObjectSinkSegmentedReopenAppends(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	ref := sampleRef()
	for _, msg := range []string{"first stream", "second stream"} {
		w, err := sink.Open(ref)
		if err != nil {
			t.Fatalf("Open() error = %v", err)
		}
		if werr := w.WriteEvent(Event{Message: msg}); werr != nil {
			t.Fatalf("WriteEvent() error = %v", werr)
		}
		if cerr := w.Close(); cerr != nil {
			t.Fatalf("Close() error = %v", cerr)
		}
	}
	if got := strings.Join(messages(readAll(t, sink, ref)), ","); got != "first stream,second stream" {
		t.Fatalf("read back %q, want both streams in order", got)
	}
}

// TestNewDurableSinkRejectsUnknownLayout: a typo in the layout fails at boot.
func TestNewDurableSinkRejectsUnknownLayout(t *testing.T) {
	if _, err := NewDurableSink(context.Background(), "s3", "", newMemStore(), "", nil, WithObjectLayout("striped")); err == nil {
		t.Fatal("NewDurableSink accepted an unknown object layout")
	}
}
