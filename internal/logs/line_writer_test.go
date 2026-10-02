package logs

import (
	"context"
	"io"
	"testing"
	"time"
)

// TestSinkWritersTakeEncodedLines: both durable writers accept a line encoded
// once by the caller (who also publishes it for the live tail) and store exactly
// what WriteEvent would have stored.
func TestSinkWritersTakeEncodedLines(t *testing.T) {
	ev := Event{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Level: "info", Stream: "stdout", Message: "hello"}
	for name, sink := range map[string]Sink{
		"disk":   NewDiskSink(t.TempDir()),
		"object": NewObjectSink(context.Background(), newMemStore(), "", nil),
	} {
		t.Run(name, func(t *testing.T) {
			w, err := sink.Open(sampleRef())
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			lw, ok := w.(LineWriter)
			if !ok {
				t.Fatalf("%T does not implement LineWriter", w)
			}
			if werr := lw.WriteLine(EncodeLine(ev)); werr != nil {
				t.Fatalf("WriteLine() error = %v", werr)
			}
			if cerr := w.Close(); cerr != nil {
				t.Fatalf("Close() error = %v", cerr)
			}
			rc, err := sink.Read(sampleRef())
			if err != nil {
				t.Fatalf("Read() error = %v", err)
			}
			defer func() { _ = rc.Close() }()
			b, _ := io.ReadAll(rc)
			if want := EncodeLine(ev) + "\n"; string(b) != want {
				t.Errorf("stored %q, want %q", b, want)
			}
		})
	}
}

// TestMemoryTailerHasSubscribers reports a live subscription and its end.
func TestMemoryTailerHasSubscribers(t *testing.T) {
	tailer := NewMemoryTailer()
	ref := sampleRef()
	if has, err := tailer.HasSubscribers(context.Background(), ref); err != nil || has {
		t.Fatalf("HasSubscribers() = %v, %v before any subscription, want false", has, err)
	}
	_, cancel := tailer.Subscribe(context.Background(), ref)
	if has, _ := tailer.HasSubscribers(context.Background(), ref); !has {
		t.Fatal("HasSubscribers() = false with a subscription open")
	}
	other := ref
	other.TryNumber++
	if has, _ := tailer.HasSubscribers(context.Background(), other); has {
		t.Error("HasSubscribers() = true for another attempt")
	}
	cancel()
	if has, _ := tailer.HasSubscribers(context.Background(), ref); has {
		t.Error("HasSubscribers() = true after the subscription ended")
	}
}
