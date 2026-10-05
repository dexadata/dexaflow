package logs

import (
	"context"
	"fmt"
	"io"
	"sync"
	"testing"
)

// countingStore is an ObjectStore that discards bodies and counts what was
// uploaded, so a benchmark measures the writer, not the fake's memory.
type countingStore struct {
	mu    sync.Mutex
	puts  int
	bytes int64
}

func (c *countingStore) Put(_ context.Context, _ string, r io.Reader) error {
	n, err := io.Copy(io.Discard, r)
	c.mu.Lock()
	c.puts++
	c.bytes += n
	c.mu.Unlock()
	return err
}

func (c *countingStore) Get(context.Context, string) (io.ReadCloser, error) {
	return nil, ErrObjectNotFound
}

// benchmarkObjectWriter streams a 64 MiB attempt through one writer with the
// production flush tuning and reports the bytes uploaded per byte logged.
func benchmarkObjectWriter(b *testing.B, opts ...ObjectOption) {
	const logBytes = 64 << 20
	ev := Event{Message: fmt.Sprintf("%0200d", 0)}
	lineLen := len(EncodeLine(ev)) + 1
	lines := logBytes / lineLen
	b.SetBytes(int64(lines * lineLen))
	b.ReportAllocs()
	var uploaded, puts int64
	for i := 0; i < b.N; i++ {
		store := &countingStore{}
		sink := NewObjectSink(context.Background(), store, "", nil, opts...)
		w, err := sink.Open(sampleRef())
		if err != nil {
			b.Fatal(err)
		}
		for j := 0; j < lines; j++ {
			if werr := w.WriteEvent(ev); werr != nil {
				b.Fatal(werr)
			}
		}
		if cerr := w.Close(); cerr != nil {
			b.Fatal(cerr)
		}
		uploaded += store.bytes
		puts += int64(store.puts)
	}
	b.ReportMetric(float64(uploaded)/float64(int64(b.N)*int64(lines*lineLen)), "uploaded/logged")
	b.ReportMetric(float64(puts)/float64(b.N), "puts/op")
}

func BenchmarkObjectWriterSingle(b *testing.B) { benchmarkObjectWriter(b) }

func BenchmarkObjectWriterSegmented(b *testing.B) {
	benchmarkObjectWriter(b, WithObjectLayout(ObjectLayoutSegmented))
}
