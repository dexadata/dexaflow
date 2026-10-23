package logs

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

// writeOneStream opens a writer with open, writes one event per message and
// closes it, as one StreamLogs stream does.
func writeOneStream(t *testing.T, open func(Ref) (LogWriter, error), ref Ref, msgs ...string) {
	t.Helper()
	w, err := open(ref)
	if err != nil {
		t.Fatalf("opening the writer: %v", err)
	}
	for _, msg := range msgs {
		if werr := w.WriteEvent(Event{Message: msg}); werr != nil {
			t.Fatalf("WriteEvent(%q): %v", msg, werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}
}

// TestObjectSinkResumeKeepsStoredPrefix covers #912 in the single layout: a
// stream the agent re-opens after the control plane closed the first one must
// continue the stored object, not overwrite it with only the lines that came
// after the re-open.
func TestObjectSinkResumeKeepsStoredPrefix(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	writeOneStream(t, sink.Open, ref, "before close 1", "before close 2")
	writeOneStream(t, sink.Resume, ref, "after reopen")
	if got := strings.Join(messages(readAll(t, sink, ref)), ","); got != "before close 1,before close 2,after reopen" {
		t.Fatalf("read back %q, want the stored prefix followed by the re-opened stream", got)
	}
}

// TestObjectSinkResumeWithNothingStored: a resume for an attempt with no
// stored object (the first stream was refused or never flushed) starts empty.
func TestObjectSinkResumeWithNothingStored(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	writeOneStream(t, sink.Resume, ref, "only line")
	if got := strings.Join(messages(readAll(t, sink, ref)), ","); got != "only line" {
		t.Fatalf("read back %q, want the resumed stream alone", got)
	}
}

// TestObjectSinkResumeRefusesWhenTheStoredObjectCannotBeRead: when the store
// keeps failing the read of the stored object, Resume refuses the stream: an
// unseeded writer would overwrite the stored prefix on its first flush.
func TestObjectSinkResumeRefusesWhenTheStoredObjectCannotBeRead(t *testing.T) {
	defer func(b time.Duration) { objectProbeBackoff = b }(objectProbeBackoff)
	objectProbeBackoff = 0
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil)
	ref := sampleRef()
	writeOneStream(t, sink.Open, ref, "stored")
	store.mu.Lock()
	store.getErr = errors.New("503 SlowDown")
	store.mu.Unlock()
	if w, err := sink.Resume(ref); err == nil {
		_ = w.Close()
		t.Fatal("Resume must refuse when the stored object cannot be read")
	}
	store.mu.Lock()
	store.getErr = nil
	store.mu.Unlock()
	if got := strings.Join(messages(readAll(t, sink, ref)), ","); got != "stored" {
		t.Fatalf("read back %q, want the stored object untouched", got)
	}
}

// TestObjectSinkSegmentedResumeAppends: the segmented layout already continues
// after the stored segments, and Resume keeps that.
func TestObjectSinkSegmentedResumeAppends(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "", nil, WithObjectLayout(ObjectLayoutSegmented))
	ref := sampleRef()
	writeOneStream(t, sink.Open, ref, "first stream")
	writeOneStream(t, sink.Resume, ref, "second stream")
	if got := strings.Join(messages(readAll(t, sink, ref)), ","); got != "first stream,second stream" {
		t.Fatalf("read back %q, want both streams in order", got)
	}
}

// TestDiskSinkResumeAppends: the disk sink appends on every open, so Resume
// keeps the stored prefix too.
func TestDiskSinkResumeAppends(t *testing.T) {
	sink := NewDiskSink(t.TempDir())
	ref := sampleRef()
	writeOneStream(t, sink.Open, ref, "first stream")
	writeOneStream(t, sink.Resume, ref, "second stream")
	rc, err := sink.Read(t.Context(), ref)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("ReadAll: %v", err)
	}
	if got := strings.Join(messages(string(raw)), ","); got != "first stream,second stream" {
		t.Fatalf("read back %q, want both streams in order", got)
	}
}
