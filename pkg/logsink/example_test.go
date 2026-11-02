package logsink_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"sync"
	"time"

	"github.com/dexadata/dexaflow/pkg/logsink"
)

// memorySink keeps every attempt's log in memory, as JSON lines.
type memorySink struct {
	mu   sync.Mutex
	logs map[logsink.Ref][]byte
}

func (m *memorySink) Open(ref logsink.Ref) (logsink.LogWriter, error) {
	return &memoryWriter{sink: m, ref: ref}, nil
}

func (m *memorySink) Read(_ context.Context, ref logsink.Ref) (io.ReadCloser, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.logs[ref]
	if !ok {
		// The API answers 404 for an error wrapping fs.ErrNotExist.
		return nil, fmt.Errorf("no log for %s/%s try %d: %w", ref.DagID, ref.TaskID, ref.TryNumber, fs.ErrNotExist)
	}
	return io.NopCloser(bytes.NewReader(b)), nil
}

// memoryWriter buffers one attempt and stores it on Close.
type memoryWriter struct {
	sink *memorySink
	ref  logsink.Ref
	buf  bytes.Buffer
}

func (w *memoryWriter) WriteEvent(ev logsink.Event) error {
	line, err := json.Marshal(ev)
	if err != nil {
		return err
	}
	w.buf.Write(line)
	w.buf.WriteByte('\n')
	return nil
}

func (w *memoryWriter) Close() error {
	w.sink.mu.Lock()
	defer w.sink.mu.Unlock()
	w.sink.logs[w.ref] = w.buf.Bytes()
	return nil
}

// A sink that stores logs in memory: the server opens a writer per attempt,
// writes its events and closes it, and reads it back for the API.
func ExampleSink() {
	var sink logsink.Sink = &memorySink{logs: map[logsink.Ref][]byte{}}
	ref := logsink.Ref{TenantID: "t-1", DagID: "etl", RunID: "r-1", TaskID: "extract", TryNumber: 1}

	w, _ := sink.Open(ref)
	_ = w.WriteEvent(logsink.Event{
		Time:    time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC),
		Level:   "info",
		Stream:  "stdout",
		Message: "extracted 42 rows",
	})
	_ = w.Close()

	rc, _ := sink.Read(context.Background(), ref)
	defer rc.Close()
	b, _ := io.ReadAll(rc)
	fmt.Print(string(b))

	_, err := sink.Read(context.Background(), logsink.Ref{DagID: "etl", TaskID: "load", TryNumber: 1})
	fmt.Println(err)
	// Output:
	// {"ts":"2026-11-02T09:00:00Z","level":"info","stream":"stdout","msg":"extracted 42 rows"}
	// no log for etl/load try 1: file does not exist
}
