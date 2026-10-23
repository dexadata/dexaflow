package logs

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

// setFlushTuning lowers the object sink's flush thresholds for a test and
// restores them on cleanup. The production values (1 MiB / 5 s) would make every
// flush test buffer megabytes or sleep seconds.
func setFlushTuning(t *testing.T, bytesThreshold int, interval time.Duration) {
	t.Helper()
	origBytes, origInterval := objectFlushBytes, objectFlushInterval
	objectFlushBytes, objectFlushInterval = bytesThreshold, interval
	t.Cleanup(func() { objectFlushBytes, objectFlushInterval = origBytes, origInterval })
}

// waitFor polls cond until it holds or the deadline passes.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(2 * time.Millisecond)
	}
	return cond()
}

// fixedEvent builds an event with a fixed timestamp so every encoded line has the
// same length (EncodeLine stamps a variable-length time.Now() into a zero Time),
// which keeps the flush-boundary arithmetic in these tests exact.
func fixedEvent(msg string) Event {
	return Event{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Message: msg}
}

func lineCount(b []byte) int {
	return len(strings.Split(strings.TrimRight(string(b), "\n"), "\n"))
}

// TestObjectWriterFlushesAtSizeThreshold pins the durability fix for the
// eviction bug class: the object sink used to hold the WHOLE attempt in memory
// and Put once on Close, so a control plane killed mid-attempt (deferred Close
// never runs) lost the complete log. A writer must now rewrite the stored object
// once the unflushed tail reaches the size threshold, BEFORE Close.
func TestObjectWriterFlushesAtSizeThreshold(t *testing.T) {
	// Threshold of exactly three encoded lines: flushes land on lines 3, 6, ...,
	// 18, and the two lines after 18 are below both the threshold and the damping
	// fraction, so an unflushed tail is guaranteed to exist at the check.
	lineLen := len(EncodeLine(fixedEvent("line 00")) + "\n")
	setFlushTuning(t, 3*lineLen, time.Hour)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	const n = 20 // not a multiple of three: the last two lines stay unflushed
	for i := 0; i < n; i++ {
		if werr := w.WriteEvent(fixedEvent(fmt.Sprintf("line %02d", i))); werr != nil {
			t.Fatalf("WriteEvent(%d) error = %v", i, werr)
		}
	}
	// Mid-attempt, with no Close yet: a partial object exists and is a strict
	// prefix of what was written.
	partial, ok := store.object(sink.key(ref))
	if !ok {
		t.Fatal("no object stored before Close: the sink still buffers the whole attempt")
	}
	if got := lineCount(partial); got == 0 || got >= n {
		t.Fatalf("partial object has %d lines, want a non-empty strict prefix of %d", got, n)
	}
	if store.putCount() < 2 {
		t.Errorf("Put count before Close = %d, want incremental Puts (>= 2)", store.putCount())
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	full, _ := store.object(sink.key(ref))
	if got := lineCount(full); got != n {
		t.Fatalf("object after Close has %d lines, want all %d", got, n)
	}
	if !strings.HasPrefix(string(full), string(partial)) {
		t.Error("the final object must extend the partial one (overwrite-Put of the accumulated content)")
	}
	for i := 0; i < n; i++ {
		if !strings.Contains(string(full), fmt.Sprintf("line %02d", i)) {
			t.Errorf("final object is missing %q", fmt.Sprintf("line %02d", i))
		}
	}
}

// TestObjectWriterFlushesOnInterval: a quiet task (one line, then silence) must
// not keep its only line hostage in memory until the attempt ends — the flusher
// rewrites the object on the time cadence even when the size threshold is never
// reached.
func TestObjectWriterFlushesOnInterval(t *testing.T) {
	setFlushTuning(t, 1<<20, 10*time.Millisecond)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if werr := w.WriteEvent(Event{Message: "starting"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	key := sink.key(ref)
	if !waitFor(t, 2*time.Second, func() bool { _, ok := store.object(key); return ok }) {
		t.Fatal("object never stored by the time-based flush (no Close was called)")
	}
	body, _ := store.object(key)
	if !strings.Contains(string(body), "starting") {
		t.Errorf("stored object = %q, want the line", string(body))
	}
}

// TestObjectWriterKillLeavesLastFlush models the SIGKILL: the writer is never
// Closed. Everything up to the last flush must be in the store; only the
// unflushed tail is lost.
func TestObjectWriterKillLeavesLastFlush(t *testing.T) {
	lineLen := len(EncodeLine(fixedEvent("line 00")) + "\n")
	setFlushTuning(t, 3*lineLen, time.Hour)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	const n = 10 // flushes at 3, 6, 9; the tenth line stays unflushed
	for i := 0; i < n; i++ {
		if werr := w.WriteEvent(fixedEvent(fmt.Sprintf("line %02d", i))); werr != nil {
			t.Fatalf("WriteEvent(%d) error = %v", i, werr)
		}
	}
	// No Close: the process died. Only the flusher goroutine is torn down (the
	// kill takes it too), and no final flush happens.
	w.(*objectWriter).stopFlusher()
	body, ok := store.object(sink.key(ref))
	if !ok {
		t.Fatal("kill lost the whole attempt: no object stored")
	}
	if !strings.Contains(string(body), "line 00") {
		t.Errorf("stored object %q does not start with the first line", string(body))
	}
	if got := lineCount(body); got >= n {
		t.Errorf("stored object has %d lines; the tenth line must still have been unflushed", got)
	}
}

// TestObjectWriterCloseSkipsRedundantPut: Close is the last flush, not an
// additional one — when nothing arrived since the last flush it must not re-upload
// the whole object.
func TestObjectWriterCloseSkipsRedundantPut(t *testing.T) {
	setFlushTuning(t, 8, time.Hour)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if werr := w.WriteEvent(Event{Message: "a line long enough to cross the tiny threshold"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	before := store.putCount()
	if before == 0 {
		t.Fatal("expected the size threshold to have flushed already")
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if got := store.putCount(); got != before {
		t.Errorf("Close issued %d extra Put(s) with nothing unflushed", got-before)
	}
}

// TestObjectWriterEmptyAttemptStillStoresObject preserves the pre-existing
// contract: an attempt that logged nothing still ends with a (empty) stored
// object, so a reader distinguishes "ran, silent" from "never shipped".
func TestObjectWriterEmptyAttemptStillStoresObject(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if _, ok := store.object(sink.key(ref)); !ok {
		t.Fatal("empty attempt left no object")
	}
}

// TestObjectWriterCloseFlushesAfterSinkContextCanceled: the sink is built on the
// server's lifecycle context, which is CANCELED by SIGTERM — exactly when the
// shutdown path runs the deferred Close. The final flush must not inherit that
// cancellation, or an orderly shutdown would fail every attempt's last Put.
func TestObjectWriterCloseFlushesAfterSinkContextCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	store := newMemStore()
	sink := NewObjectSink(ctx, store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if werr := w.WriteEvent(Event{Message: "before sigterm"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	cancel() // SIGTERM
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() after ctx cancel error = %v, want the final flush to succeed", cerr)
	}
	body, ok := store.object(sink.key(ref))
	if !ok || !strings.Contains(string(body), "before sigterm") {
		t.Fatalf("stored object = %q, %v; want the line flushed despite the canceled sink context", body, ok)
	}
}

// TestObjectWriterIncrementalFlushErrorIsRetriedNotFatal: a transient store
// failure on an incremental flush must not end the log stream (WriteEvent keeps
// accepting lines); the next trigger retries, and Close surfaces the outcome of
// the final flush only.
func TestObjectWriterIncrementalFlushErrorIsRetriedNotFatal(t *testing.T) {
	setFlushTuning(t, 8, time.Hour)
	store := newMemStore()
	store.setPutErr(errors.New("503 slow down"))
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if werr := w.WriteEvent(Event{Message: "first line, flush fails"}); werr != nil {
		t.Fatalf("WriteEvent() during a failing flush = %v, want nil (best-effort, retried)", werr)
	}
	if store.putCount() != 0 {
		t.Fatalf("Put count = %d, want 0 while the store fails", store.putCount())
	}
	store.setPutErr(nil)
	if werr := w.WriteEvent(Event{Message: "second line, flush succeeds"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	body, _ := store.object(sink.key(ref))
	if lineCount(body) != 2 {
		t.Fatalf("object has %d lines, want both (the failed flush's line must not be dropped): %q", lineCount(body), body)
	}
}

// TestObjectWriterCloseStopsFlusher: after Close, no flusher goroutine may keep
// rewriting the object (a leak per finished attempt, and a stale overwrite race
// against the reaper's AppendEvent).
func TestObjectWriterCloseStopsFlusher(t *testing.T) {
	setFlushTuning(t, 1<<20, 5*time.Millisecond)
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	if werr := w.WriteEvent(Event{Message: "x"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	after := store.putCount()
	time.Sleep(50 * time.Millisecond)
	if got := store.putCount(); got != after {
		t.Errorf("Put count grew from %d to %d after Close: the flusher is still running", after, got)
	}
}

// TestShouldFlushDamping pins the re-upload budget: object stores have no append,
// so every flush rewrites the whole object. Undamped, a chatty 128 MiB log flushed
// every second would re-upload gigabytes. Once the stored object is large, a
// flush waits until the unflushed tail is a fixed fraction of it, bounding total
// upload to a small multiple of the final size while small logs still flush on
// every cadence tick.
func TestShouldFlushDamping(t *testing.T) {
	setFlushTuning(t, 1<<20, 5*time.Second)
	cases := []struct {
		name               string
		unflushed, flushed int
		since              time.Duration
		want               bool
	}{
		{"nothing unflushed", 0, 0, time.Hour, false},
		{"small object, interval elapsed", 10, 0, 6 * time.Second, true},
		{"small object, interval not elapsed", 10, 100, time.Second, false},
		{"size threshold reached", 1 << 20, 0, 0, true},
		{"large object, tail below damping fraction, interval elapsed", 1 << 20, 64 << 20, 6 * time.Second, false},
		{"large object, tail below damping fraction, staleness bound reached", 1 << 20, 64 << 20, objectFlushMaxStaleness, true},
		{"large object, tail below damping fraction, just under the staleness bound", 1 << 20, 64 << 20, objectFlushMaxStaleness - time.Millisecond, false},
		{"large object, tail at damping fraction", 8 << 20, 64 << 20, 0, true},
		{"medium object, tail under fraction, interval elapsed", 100 << 10, 8 << 20, 6 * time.Second, false},
		{"medium object, tail under fraction, staleness bound reached", 100 << 10, 8 << 20, time.Hour, true},
		{"medium object, tail at fraction, time elapsed", 1 << 20, 8 << 20, 6 * time.Second, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := shouldFlush(tc.unflushed, tc.flushed, tc.since); got != tc.want {
				t.Errorf("shouldFlush(%d, %d, %s) = %v, want %v", tc.unflushed, tc.flushed, tc.since, got, tc.want)
			}
		})
	}
}

// TestObjectWriterConcurrentAttemptsBoundedAndRaceFree runs many writers with an
// aggressive flusher cadence while lines pour in, under -race: no Put body may
// ever exceed the per-attempt cap, every final object must hold exactly its
// lines in order, and the writer/flusher must not race on the buffer.
func TestObjectWriterConcurrentAttemptsBoundedAndRaceFree(t *testing.T) {
	setFlushTuning(t, 256, time.Millisecond)
	origCap := maxBufferedAttemptBytes
	maxBufferedAttemptBytes = 4 << 10
	t.Cleanup(func() { maxBufferedAttemptBytes = origCap })

	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	const attempts, perAttempt = 8, 40
	var wg sync.WaitGroup
	for a := 0; a < attempts; a++ {
		wg.Add(1)
		go func(a int) {
			defer wg.Done()
			ref := sampleRef()
			ref.TaskID = fmt.Sprintf("task%d", a)
			w, err := sink.Open(ref)
			if err != nil {
				t.Errorf("Open(%d) error = %v", a, err)
				return
			}
			for i := 0; i < perAttempt; i++ {
				if werr := w.WriteEvent(Event{Message: fmt.Sprintf("t%d l%03d", a, i)}); werr != nil {
					t.Errorf("WriteEvent(%d,%d) error = %v", a, i, werr)
					return
				}
				if i%7 == 0 {
					time.Sleep(time.Millisecond) // let the flusher interleave
				}
			}
			if cerr := w.Close(); cerr != nil {
				t.Errorf("Close(%d) error = %v", a, cerr)
			}
		}(a)
	}
	wg.Wait()

	store.mu.Lock()
	puts := append([]int(nil), store.puts...)
	store.mu.Unlock()
	if len(puts) < attempts {
		t.Fatalf("only %d Puts for %d attempts", len(puts), attempts)
	}
	for i, n := range puts {
		if n > maxBufferedAttemptBytes {
			t.Fatalf("Put #%d body = %d bytes, exceeds the %d-byte cap", i, n, maxBufferedAttemptBytes)
		}
	}
	for a := 0; a < attempts; a++ {
		ref := sampleRef()
		ref.TaskID = fmt.Sprintf("task%d", a)
		body, ok := store.object(sink.key(ref))
		if !ok {
			t.Fatalf("attempt %d has no object", a)
		}
		lines := strings.Split(strings.TrimRight(string(body), "\n"), "\n")
		if len(lines) != perAttempt {
			t.Fatalf("attempt %d object has %d lines, want %d", a, len(lines), perAttempt)
		}
		for i, l := range lines {
			if want := fmt.Sprintf("t%d l%03d", a, i); DecodeLine(l).Message != want {
				t.Fatalf("attempt %d line %d = %q, want %q (order or interleave corruption)", a, i, DecodeLine(l).Message, want)
			}
		}
	}
}

// TestDiskSinkPathUnchangedByObjectFlushTuning: Lite and the RWX-PVC path use the
// disk sink, which already flushes incrementally through bufio; the object sink's
// tuning knobs must have no effect there.
func TestDiskSinkPathUnchangedByObjectFlushTuning(t *testing.T) {
	setFlushTuning(t, 1, time.Nanosecond)
	sink, err := NewDurableSink(context.Background(), "disk", t.TempDir(), nil, "", nil)
	if err != nil {
		t.Fatalf("NewDurableSink(disk) error = %v", err)
	}
	if _, ok := sink.(*DiskSink); !ok {
		t.Fatalf("NewDurableSink(disk) = %T, want *DiskSink", sink)
	}
	w, err := sink.Open(sampleRef())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Message: "disk"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	rc, err := sink.Read(t.Context(), sampleRef())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	var b strings.Builder
	buf := make([]byte, 256)
	for {
		n, rerr := rc.Read(buf)
		b.Write(buf[:n])
		if rerr != nil {
			break
		}
	}
	if !strings.Contains(b.String(), "disk") {
		t.Errorf("disk sink read back %q, want the line", b.String())
	}
}

// TestObjectWriterFlushesStaleTailDespiteDamping: the damping bounds upload
// amplification, never staleness. A task that logs a large burst and then goes
// quiet (or trickles) has a tail under the damping fraction that would otherwise
// stay unflushed until Close — for a chatty hour-long task that is minutes of
// log at risk, not the seconds the sink promises. Past the staleness bound the
// tail is flushed regardless of its fraction; the cost is at most one extra
// rewrite per quiet bound.
func TestObjectWriterFlushesStaleTailDespiteDamping(t *testing.T) {
	setFlushTuning(t, 1<<20, time.Hour) // no size or cadence trigger in play
	origStale := objectFlushMaxStaleness
	objectFlushMaxStaleness = 20 * time.Millisecond
	t.Cleanup(func() { objectFlushMaxStaleness = origStale })
	origInterval := objectFlushInterval
	objectFlushInterval = 5 * time.Millisecond // the flusher ticks; only the staleness rule may fire
	t.Cleanup(func() { objectFlushInterval = origInterval })

	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = w.Close() })
	ow := w.(*objectWriter)

	// A large stored prefix, as after a burst: 64 MiB already flushed.
	ow.mu.Lock()
	ow.flushed = 64 << 20
	ow.lastFlush = time.Now()
	ow.mu.Unlock()
	// A tail far below the damping fraction (1/8 of 64 MiB = 8 MiB).
	if werr := w.WriteEvent(Event{Message: "the quiet tail"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	key := sink.key(ref)
	if !waitFor(t, 2*time.Second, func() bool {
		body, ok := store.object(key)
		return ok && strings.Contains(string(body), "the quiet tail")
	}) {
		t.Fatal("a tail under the damping fraction was never flushed by the staleness bound; it would sit in memory until Close")
	}
}
