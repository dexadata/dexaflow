package logs

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// setProbeBackoff shortens the wait between probe retries for a test and
// restores it on cleanup.
func setProbeBackoff(t *testing.T, d time.Duration) {
	t.Helper()
	orig := objectProbeBackoff
	objectProbeBackoff = d
	t.Cleanup(func() { objectProbeBackoff = orig })
}

// flakyStore fails the next `failures` Gets with err, the way a store answers
// a 503 SlowDown for a moment, and behaves like memStore otherwise.
type flakyStore struct {
	*memStore
	failures int
	err      error
}

func (f *flakyStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	f.mu.Lock()
	if f.failures > 0 {
		f.failures--
		f.mu.Unlock()
		return nil, f.err
	}
	f.mu.Unlock()
	return f.memStore.Get(ctx, key)
}

// setFailures sets how many of the next Gets fail, under the lock.
func (f *flakyStore) setFailures(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = n
}

var errSlowDown = errors.New("api error SlowDown: Please reduce your request rate")

// segmentedSink builds a segmented sink over store; a nil logger means the
// process default, as in NewObjectSink.
func segmentedSink(store ObjectStore, logger *slog.Logger) *ObjectSink {
	return NewObjectSink(context.Background(), store, "p", logger, WithObjectLayout(ObjectLayoutSegmented))
}

// TestObjectSinkSegmentedReopenWithDeniedProbeKeepsFirstStream: a second
// stream for an attempt whose segment probe the store refuses (S3 answers
// AccessDenied, not NoSuchKey, to a caller without s3:ListBucket) used to start
// at segment zero over the first stream's lines. Open now refuses the stream,
// says so once, and what was stored reads back intact.
func TestObjectSinkSegmentedReopenWithDeniedProbeKeepsFirstStream(t *testing.T) {
	setProbeBackoff(t, time.Millisecond)
	store := newMemStore()
	ref := sampleRef()
	writeOne(t, segmentedSink(store, nil), ref, "first stream")

	logger, logBuf := testLogger()
	sink := segmentedSink(&deniedStore{memStore: store}, logger)
	w, err := sink.Open(ref)
	if err == nil {
		_ = w.Close()
		t.Fatal("Open() accepted a stream whose segment probe was refused")
	}
	if !errors.Is(err, errAccessDenied) {
		t.Errorf("Open() error = %v, want the store's refusal", err)
	}
	if n := strings.Count(logBuf.String(), "refusing the log stream"); n != 1 {
		t.Errorf("Open() warned %d times, want once:\n%s", n, logBuf.String())
	}
	if got := messages(readAll(t, segmentedSink(store, nil), ref)); strings.Join(got, ",") != "first stream" {
		t.Fatalf("read back %v, want the first stream intact", got)
	}
	if body, ok := store.object(sink.segmentKey(ref, 0)); !ok || !strings.Contains(string(body), "first stream") {
		t.Errorf("segment zero = %q, want the first stream's line", body)
	}
}

// TestObjectSinkSegmentedOpenRetriesTransientProbeFailure: a probe that fails
// for a moment is retried, so the second stream still lands after the first
// one's segments; one that keeps failing refuses the stream.
func TestObjectSinkSegmentedOpenRetriesTransientProbeFailure(t *testing.T) {
	setProbeBackoff(t, time.Millisecond)
	store := newMemStore()
	ref := sampleRef()
	writeOne(t, segmentedSink(store, nil), ref, "first stream")

	flaky := &flakyStore{memStore: store, failures: objectProbeAttempts - 1, err: errSlowDown}
	sink := segmentedSink(flaky, nil)
	writeOne(t, sink, ref, "second stream")
	if got := messages(readAll(t, sink, ref)); strings.Join(got, ",") != "first stream,second stream" {
		t.Fatalf("read back %v, want both streams in order", got)
	}

	flaky.setFailures(objectProbeAttempts)
	w, err := sink.Open(ref)
	if err == nil {
		_ = w.Close()
		t.Fatal("Open() accepted a stream after the probe failed on every try")
	}
	if !errors.Is(err, errSlowDown) {
		t.Errorf("Open() error = %v, want the store's failure", err)
	}
	if got := messages(readAll(t, segmentedSink(store, nil), ref)); strings.Join(got, ",") != "first stream,second stream" {
		t.Fatalf("read back %v after the refused stream, want both streams intact", got)
	}
}

// failingKeyStore fails every Get of one key with err, the way a store answers
// a 503 for one object while the rest of the bucket is fine, and behaves like
// memStore otherwise.
type failingKeyStore struct {
	*memStore
	failKey string
	err     error
}

func (f *failingKeyStore) Get(ctx context.Context, key string) (io.ReadCloser, error) {
	if key == f.failKey {
		return nil, f.err
	}
	return f.memStore.Get(ctx, key)
}

// readUntilError reads the attempt back and returns what came before the
// error the read ended with, which the caller asserts on.
func readUntilError(t *testing.T, sink *ObjectSink, ref Ref) (string, error) {
	t.Helper()
	rc, err := sink.Read(ref)
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	b, err := io.ReadAll(rc)
	return string(b), err
}

// TestObjectSinkSegmentedReadEndsWithErrorPastLastSegment: a failure other
// than not-found while looking for the segment after the last one, or for the
// single object after the segments, used to end the log quietly, so a store
// outage in the middle of a read served a shorter log that looked complete.
// The read now ends with the store's error after the segments it served, and
// the control plane logs one warning naming how many; a probe that only fails
// once is still absorbed by the retry.
func TestObjectSinkSegmentedReadEndsWithErrorPastLastSegment(t *testing.T) {
	setProbeBackoff(t, time.Millisecond)
	lineLen := len(EncodeLine(fixedEvent("line 0")) + "\n")
	setFlushTuning(t, lineLen, time.Hour)
	setSegmentBytes(t, 2*lineLen)
	store := newMemStore()
	ref := sampleRef()
	writer := segmentedSink(store, nil)
	w, err := writer.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for i := 0; i < 5; i++ {
		if werr := w.WriteEvent(fixedEvent(fmt.Sprintf("line %d", i))); werr != nil {
			t.Fatalf("WriteEvent(%d) error = %v", i, werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if _, ok := store.object(writer.segmentKey(ref, 2)); !ok {
		t.Fatal("the attempt should span three segments")
	}
	want := "line 0,line 1,line 2,line 3,line 4"

	t.Run("segment probe", func(t *testing.T) {
		logger, logBuf := testLogger()
		reader := segmentedSink(&failingKeyStore{memStore: store, failKey: writer.segmentKey(ref, 3), err: errSlowDown}, logger)
		body, rerr := readUntilError(t, reader, ref)
		if got := messages(body); strings.Join(got, ",") != want {
			t.Fatalf("read back %v before the error, want every segment", got)
		}
		if !errors.Is(rerr, errSlowDown) {
			t.Fatalf("the read ended with %v, want the store's failure", rerr)
		}
		if n := strings.Count(logBuf.String(), "probing the next log segment failed"); n != 1 {
			t.Errorf("the read warned %d times, want once:\n%s", n, logBuf.String())
		}
	})

	if aerr := writer.AppendEvent(ref, Event{Message: "agent lost"}); aerr != nil {
		t.Fatalf("AppendEvent() error = %v", aerr)
	}
	t.Run("single object after the segments", func(t *testing.T) {
		logger, logBuf := testLogger()
		reader := segmentedSink(&failingKeyStore{memStore: store, failKey: writer.key(ref), err: errSlowDown}, logger)
		body, rerr := readUntilError(t, reader, ref)
		if got := messages(body); strings.Join(got, ",") != want {
			t.Fatalf("read back %v before the error, want every segment", got)
		}
		if !errors.Is(rerr, errSlowDown) {
			t.Fatalf("the read ended with %v, want the store's failure", rerr)
		}
		if n := strings.Count(logBuf.String(), "reading the log object after its segments failed"); n != 1 {
			t.Errorf("the read warned %d times, want once:\n%s", n, logBuf.String())
		}
	})

	t.Run("healthy store", func(t *testing.T) {
		if got := messages(readAll(t, segmentedSink(store, nil), ref)); strings.Join(got, ",") != want+",agent lost" {
			t.Fatalf("read back %v, want every line and the marker", got)
		}
	})
}

// TestObjectSinkSegmentedReadReportsProbeOutage: a probe failure that clears
// on the retry is not seen at all; one that does not, with no single object
// behind it, is a store problem the caller hears about, not an attempt that
// logged nothing.
func TestObjectSinkSegmentedReadReportsProbeOutage(t *testing.T) {
	setProbeBackoff(t, time.Millisecond)
	store := newMemStore()
	ref := sampleRef()
	writeOne(t, segmentedSink(store, nil), ref, "only")

	flaky := &flakyStore{memStore: store, failures: objectReadProbeAttempts - 1, err: errSlowDown}
	sink := segmentedSink(flaky, nil)
	if got := messages(readAll(t, sink, ref)); strings.Join(got, ",") != "only" {
		t.Fatalf("read back %v after one failed probe, want the attempt", got)
	}

	empty := &flakyStore{memStore: newMemStore(), failures: objectReadProbeAttempts, err: errSlowDown}
	_, err := segmentedSink(empty, nil).Read(ref)
	switch {
	case err == nil:
		t.Fatal("Read() during an outage returned a log")
	case errors.Is(err, ErrObjectNotFound):
		t.Fatalf("Read() during an outage = %v, want the store's failure, not an absent log", err)
	case !errors.Is(err, errSlowDown):
		t.Fatalf("Read() during an outage = %v, want the store's failure", err)
	}
}

// TestNewDurableSinkSegmentedChecksMissingKeyAnswer: the boot-time constructor
// refuses the segmented layout on a store that answers a missing key with
// something other than not-found, naming the permission to grant, and accepts
// one that answers not-found. The check reads a key that cannot exist, retries
// a transient failure, writes nothing, and leaves the single layout alone.
func TestNewDurableSinkSegmentedChecksMissingKeyAnswer(t *testing.T) {
	setProbeBackoff(t, time.Millisecond)
	denied := &deniedStore{memStore: newMemStore()}
	_, err := NewDurableSink(context.Background(), "s3", "", denied, "p", nil, WithObjectLayout(ObjectLayoutSegmented))
	if err == nil {
		t.Fatal("NewDurableSink accepted the segmented layout on a store that refuses missing keys")
	}
	if !errors.Is(err, errAccessDenied) {
		t.Errorf("error = %v, want the store's refusal as its cause", err)
	}
	for _, want := range []string{"s3:ListBucket", ObjectLayoutSegmented, "p/.probe-"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not mention %q", err, want)
		}
	}
	denied.mu.Lock()
	gets := denied.gets
	denied.mu.Unlock()
	if gets != objectProbeAttempts {
		t.Errorf("the check issued %d GETs, want %d", gets, objectProbeAttempts)
	}
	if n := denied.putCount(); n != 0 {
		t.Errorf("the check wrote %d objects, want none", n)
	}

	store := newMemStore()
	sink, err := NewDurableSink(context.Background(), "s3", "", store, "p", nil, WithObjectLayout(ObjectLayoutSegmented))
	if err != nil {
		t.Fatalf("NewDurableSink(segmented) over a store answering not-found: %v", err)
	}
	if _, ok := sink.(*ObjectSink); !ok {
		t.Fatalf("NewDurableSink(segmented) = %T, want *ObjectSink", sink)
	}
	if n := store.putCount(); n != 0 {
		t.Errorf("the check wrote %d objects, want none", n)
	}

	flaky := &flakyStore{memStore: newMemStore(), failures: objectProbeAttempts - 1, err: errSlowDown}
	if _, err := NewDurableSink(context.Background(), "s3", "", flaky, "p", nil, WithObjectLayout(ObjectLayoutSegmented)); err != nil {
		t.Errorf("a transient failure refused the boot: %v", err)
	}

	single := &deniedStore{memStore: newMemStore()}
	if _, err := NewDurableSink(context.Background(), "s3", "", single, "p", nil); err != nil {
		t.Errorf("the single layout was refused on a store that refuses missing keys: %v", err)
	}
	single.mu.Lock()
	gets = single.gets
	single.mu.Unlock()
	if gets != 0 {
		t.Errorf("the single layout probed the store %d times, want none", gets)
	}
}
