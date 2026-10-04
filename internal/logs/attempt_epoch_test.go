package logs

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// epochRef is ref() on the given attempt epoch.
func epochRef(epoch int) Ref {
	r := ref()
	r.AttemptEpoch = epoch
	return r
}

func writeStream(t *testing.T, sink Sink, r Ref, lines ...string) {
	t.Helper()
	w, err := sink.Open(r)
	if err != nil {
		t.Fatalf("Open(epoch %d): %v", r.AttemptEpoch, err)
	}
	for _, l := range lines {
		if err := w.WriteEvent(Event{Time: time.Unix(0, 0).UTC(), Level: "info", Stream: "stdout", Message: l}); err != nil {
			t.Fatalf("WriteEvent: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

func readBody(t *testing.T, rc io.ReadCloser) string {
	t.Helper()
	b, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("reading: %v", err)
	}
	if err := rc.Close(); err != nil {
		t.Fatalf("closing: %v", err)
	}
	return string(b)
}

// streamMessages decodes a JSONL body into (stream, message) pairs, in order.
func streamMessages(body string) [][2]string {
	var out [][2]string
	for _, line := range strings.Split(strings.TrimRight(body, "\n"), "\n") {
		if line == "" {
			continue
		}
		ev := DecodeLine(line)
		out = append(out, [2]string{ev.Stream, ev.Message})
	}
	return out
}

// TestLogKeyCarriesAttemptEpoch: epoch 0 keeps today's key, so every log
// written before the upgrade is still found; any other epoch gets its own
// key, so a second execution of one try no longer overwrites the first (#863).
func TestLogKeyCarriesAttemptEpoch(t *testing.T) {
	store := newMemStore()
	sink := NewObjectSink(context.Background(), store, "logs", nil)
	writeStream(t, sink, epochRef(0), "zero")
	writeStream(t, sink, epochRef(3), "three")
	for _, key := range []string{"logs/acme/etl/run-1/extract/1.log", "logs/acme/etl/run-1/extract/1.e3.log"} {
		if _, ok := store.object(key); !ok {
			t.Errorf("object %q was not written; have %v", key, store.objs)
		}
	}

	dir := t.TempDir()
	disk := NewDiskSink(dir)
	writeStream(t, disk, epochRef(0), "zero")
	writeStream(t, disk, epochRef(3), "three")
	for _, name := range []string{"1.log", "1.e3.log"} {
		if _, err := os.Stat(filepath.Join(dir, "acme", "etl", "run-1", "extract", name)); err != nil {
			t.Errorf("file %s was not written: %v", name, err)
		}
	}
}

// TestRefRejectsNegativeAttemptEpoch: a negative epoch names no execution.
func TestRefRejectsNegativeAttemptEpoch(t *testing.T) {
	if _, err := NewDiskSink(t.TempDir()).Open(epochRef(-1)); !errors.Is(err, ErrUnsafeRef) {
		t.Errorf("Open(epoch -1) = %v, want ErrUnsafeRef", err)
	}
}

// TestReadAttemptsConcatenatesEveryExecutionOfATry: two infra attempts on one
// try produce two objects, and reading the try serves both streams in order,
// each preceded by one system line naming the execution.
func TestReadAttemptsConcatenatesEveryExecutionOfATry(t *testing.T) {
	sinks := map[string]Sink{
		"object": NewObjectSink(context.Background(), newMemStore(), "", nil),
		"disk":   NewDiskSink(t.TempDir()),
	}
	for name, sink := range sinks {
		t.Run(name, func(t *testing.T) {
			writeStream(t, sink, epochRef(1), "first-a", "first-b")
			writeStream(t, sink, epochRef(2), "second-a")
			rc, err := ReadAttempts(sink, ref(), TryEpochs{High: 2})
			if err != nil {
				t.Fatalf("ReadAttempts: %v", err)
			}
			got := streamMessages(readBody(t, rc))
			if len(got) != 5 {
				t.Fatalf("want 2 system lines and 3 task lines, got %v", got)
			}
			wantOrder := []string{"system", "stdout", "stdout", "system", "stdout"}
			for i, w := range wantOrder {
				if got[i][0] != w {
					t.Fatalf("line %d stream = %q, want %q (all: %v)", i, got[i][0], w, got)
				}
			}
			if got[1][1] != "first-a" || got[2][1] != "first-b" || got[4][1] != "second-a" {
				t.Fatalf("streams out of order: %v", got)
			}
			if !strings.Contains(got[0][1], "epoch 1") || !strings.Contains(got[3][1], "epoch 2") {
				t.Errorf("each system line must name its execution: %q, %q", got[0][1], got[3][1])
			}
		})
	}
}

// TestReadAttemptsServesALegacyLogUnchanged: an epoch-0 object written before
// the change is served byte for byte when it is the try's only stream.
func TestReadAttemptsServesALegacyLogUnchanged(t *testing.T) {
	store := newMemStore()
	legacy := `{"ts":"2026-01-01T00:00:00Z","level":"info","stream":"stdout","msg":"before the upgrade"}` + "\n"
	if err := store.Put(context.Background(), "acme/etl/run-1/extract/1.log", strings.NewReader(legacy)); err != nil {
		t.Fatal(err)
	}
	sink := NewObjectSink(context.Background(), store, "", nil)
	rc, err := ReadAttempts(sink, ref(), TryEpochs{High: 2})
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	if got := readBody(t, rc); got != legacy {
		t.Errorf("a lone legacy stream must be served unchanged, got %q", got)
	}
}

// TestReadAttemptsTerminatesAStreamWithoutANewline: a stream cut mid-line
// (a writer killed between flushes) must not glue its last line to the next
// execution's system line.
func TestReadAttemptsTerminatesAStreamWithoutANewline(t *testing.T) {
	store := newMemStore()
	_ = store.Put(context.Background(), "acme/etl/run-1/extract/1.log", strings.NewReader(`{"stream":"stdout","msg":"cut"}`))
	_ = store.Put(context.Background(), "acme/etl/run-1/extract/1.e1.log", strings.NewReader(`{"stream":"stdout","msg":"next"}`+"\n"))
	rc, err := ReadAttempts(NewObjectSink(context.Background(), store, "", nil), ref(), TryEpochs{High: 1})
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	got := streamMessages(readBody(t, rc))
	if len(got) != 4 || got[1][1] != "cut" || got[3][1] != "next" {
		t.Fatalf("want system, cut, system, next; got %v", got)
	}
}

// TestReadAttemptsNotFound: a try with no stored stream reports the sink's own
// absence, so the API still answers 404.
func TestReadAttemptsNotFound(t *testing.T) {
	_, err := ReadAttempts(NewObjectSink(context.Background(), newMemStore(), "", nil), ref(), TryEpochs{High: 4})
	if !errors.Is(err, ErrObjectNotFound) {
		t.Errorf("object sink: got %v, want ErrObjectNotFound", err)
	}
	_, err = ReadAttempts(NewDiskSink(t.TempDir()), ref(), TryEpochs{High: 4})
	if !errors.Is(err, os.ErrNotExist) {
		t.Errorf("disk sink: got %v, want os.ErrNotExist", err)
	}
}

// TestReadAttemptsPropagatesARealFailure: only absence is skipped; a store
// failure on any execution fails the read rather than serving a log with a
// silent hole.
func TestReadAttemptsPropagatesARealFailure(t *testing.T) {
	store := newMemStore()
	store.getErr = errors.New("throttled")
	if _, err := ReadAttempts(NewObjectSink(context.Background(), store, "", nil), ref(), TryEpochs{High: 1}); err == nil || errors.Is(err, ErrObjectNotFound) {
		t.Errorf("a store failure must propagate, got %v", err)
	}
}

// TestReadAttemptsSystemLineCarriesNoTimestamp: the line that introduces an
// execution marks a boundary, not an event, so it decodes with a zero time and
// the structured log view renders it without a timestamp instead of showing it
// at 1970-01-01.
func TestReadAttemptsSystemLineCarriesNoTimestamp(t *testing.T) {
	sink := NewObjectSink(context.Background(), newMemStore(), "", nil)
	writeStream(t, sink, epochRef(1), "first")
	writeStream(t, sink, epochRef(2), "second")
	rc, err := ReadAttempts(sink, ref(), TryEpochs{High: 2})
	if err != nil {
		t.Fatalf("ReadAttempts: %v", err)
	}
	first, _, _ := strings.Cut(readBody(t, rc), "\n")
	if ev := DecodeLine(first); ev.Stream != "system" || !ev.Time.IsZero() {
		t.Fatalf("system line = %+v, want stream system with a zero time", ev)
	}
}
