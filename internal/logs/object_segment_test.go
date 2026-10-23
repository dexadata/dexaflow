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
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
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
	rc, err := sink.Read(t.Context(), ref)
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
//
// The flush is triggered explicitly with flushTick, never by the ticker: with a
// short interval, a WriteEvent that runs after the interval elapsed finds the
// tail due and runs the gated Put itself, on the test goroutine, which then
// waits forever for a release only it would send (#2050). Every step that can
// block is bounded by one deadline, so a regression fails fast instead of
// hanging until the test binary times out.
func TestObjectWriterFlusherPutDoesNotBlockWriter(t *testing.T) {
	// Neither threshold can fire on its own during the test: no WriteEvent flushes
	// inline and the flusher's ticker never beats.
	setFlushTuning(t, 1<<20, time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	store := newGatedStore()
	t.Cleanup(store.open)
	sink := NewObjectSink(context.Background(), store, "", nil)
	lw, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	w := lw.(*objectWriter)
	within(ctx, t, "WriteEvent(first)", func() error { return w.WriteEvent(Event{Message: "first"}) })

	// Make the tail due on the cadence, then run one flusher beat: it takes
	// flushMu and parks in the gated Put.
	w.mu.Lock()
	w.lastFlush = time.Now().Add(-2 * objectFlushInterval)
	w.mu.Unlock()
	tick := make(chan struct{})
	go func() {
		defer close(tick)
		w.flushTick(context.Background())
	}()
	select {
	case <-store.entered:
	case <-ctx.Done():
		t.Fatal("the flusher beat never started a Put")
	}

	// The tail is due, but the Put in flight owns flushMu: the write must buffer
	// its line and return without waiting for it.
	within(ctx, t, "WriteEvent(second) behind the flusher's in-flight Put", func() error {
		return w.WriteEvent(Event{Message: "second"})
	})

	store.open()
	select {
	case <-tick:
	case <-ctx.Done():
		t.Fatal("the flusher beat did not finish after the store was released")
	}
	within(ctx, t, "Close()", w.Close)
	if got := messages(readAll(t, sink, sampleRef())); strings.Join(got, ",") != "first,second" {
		t.Fatalf("read back %v, want [first second]", got)
	}
}

// within runs fn and fails the test if it errors or is still running when ctx
// ends, so a step that blocks fails fast instead of hanging the test binary.
func within(ctx context.Context, t *testing.T, what string, fn func() error) {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("%s error = %v", what, err)
		}
	case <-ctx.Done():
		t.Fatalf("%s blocked: %v", what, ctx.Err())
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

// deniedStore answers a Get of a missing key with an error that is not
// ErrObjectNotFound, the way S3 answers 403 AccessDenied instead of 404 to a
// caller without s3:ListBucket. It counts Gets so a test can pin the round trips
// a read costs.
type deniedStore struct {
	*memStore
	gets int
}

func (d *deniedStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	d.mu.Lock()
	d.gets++
	_, ok := d.objs[key]
	d.mu.Unlock()
	if !ok {
		return nil, errAccessDenied
	}
	return d.memStore.Get(ctx, key)
}

var errAccessDenied = fmt.Errorf("api error AccessDenied: Access Denied")

// writeOne stores a one-line attempt through sink.
func writeOne(t *testing.T, sink *ObjectSink, ref Ref, msg string) {
	t.Helper()
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

// TestObjectSinkSingleReadSurvivesDeniedSegmentProbe pins the upgrade path of a
// least-privilege bucket policy (GetObject and PutObject only): the default
// layout reads {try}.log with the single GET it cost before segments existed,
// so a store that answers a missing segment with AccessDenied cannot fail it.
func TestObjectSinkSingleReadSurvivesDeniedSegmentProbe(t *testing.T) {
	store := &deniedStore{memStore: newMemStore()}
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	writeOne(t, sink, ref, "single")
	store.mu.Lock()
	store.gets = 0
	store.mu.Unlock()
	if got := messages(readAll(t, sink, ref)); strings.Join(got, ",") != "single" {
		t.Fatalf("read back %v, want the single object", got)
	}
	store.mu.Lock()
	gets := store.gets
	store.mu.Unlock()
	if gets != 1 {
		t.Errorf("reading a single-layout attempt issued %d GETs, want 1", gets)
	}
}

// TestObjectSinkSegmentedReadFallsBackOnDeniedProbe: a segmented sink reading
// an attempt stored as one object must not fail because the segment probe was
// refused; it falls back to {try}.log.
func TestObjectSinkSegmentedReadFallsBackOnDeniedProbe(t *testing.T) {
	setProbeBackoff(t, time.Millisecond)
	store := &deniedStore{memStore: newMemStore()}
	ref := sampleRef()
	writeOne(t, NewObjectSink(context.Background(), store, "", nil), ref, "legacy")
	seg := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	if got := messages(readAll(t, seg, ref)); strings.Join(got, ",") != "legacy" {
		t.Fatalf("read back %v, want the single object", got)
	}
}

// TestObjectSinkSegmentedKeepsTheAttemptCap: sealing a segment frees its
// memory, but it must not lift the per-attempt ceiling. A runaway task gets the
// same loud error in both layouts once the attempt passes
// maxBufferedAttemptBytes, and the stored attempt never exceeds it.
func TestObjectSinkSegmentedKeepsTheAttemptCap(t *testing.T) {
	lineLen := len(EncodeLine(fixedEvent("line 000")) + "\n")
	setFlushTuning(t, lineLen, time.Hour)
	setSegmentBytes(t, 2*lineLen)
	orig := maxBufferedAttemptBytes
	maxBufferedAttemptBytes = 10 * lineLen
	t.Cleanup(func() { maxBufferedAttemptBytes = orig })

	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	var capErr error
	written := 0
	for i := 0; i < 100 && capErr == nil; i++ {
		if capErr = w.WriteEvent(fixedEvent(fmt.Sprintf("line %03d", i))); capErr == nil {
			written++
		}
	}
	if capErr == nil {
		t.Fatal("the segmented layout accepted 100 lines past a 10-line attempt cap")
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if written != 10 {
		t.Errorf("accepted %d lines before the cap, want 10", written)
	}
	if got := len(readAll(t, sink, ref)); got > maxBufferedAttemptBytes {
		t.Errorf("stored %d bytes for the attempt, want at most %d", got, maxBufferedAttemptBytes)
	}
}
