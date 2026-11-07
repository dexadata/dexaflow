---
title: "pkg/logsink"
linkTitle: "pkg/logsink"
weight: 17
---

```go
import "github.com/dexadata/dexaflow/pkg/logsink"
```

Package logsink is the contract of the task log sink \(ADR 0099\): where the control plane stores the log of each task attempt and where it reads it back from for the API, the UI and the CLI.

A [Sink](<#Sink>) opens a [LogWriter](<#LogWriter>) for one attempt, named by a [Ref](<#Ref>), and the server writes the attempt's [Event](<#Event>) values to it as the agent streams them. The Read method of a [Sink](<#Sink>) returns the stored log as JSON lines, one event per line, in the order they were written.

Three optional interfaces let the server do more with a sink that supports them, and it checks for each with a type assertion. A [LogWriter](<#LogWriter>) that is also a [LineWriter](<#LineWriter>) takes a line the server already encoded. A [Sink](<#Sink>) that is also a [Resumer](<#Resumer>) continues a stream after the agent's connection dropped mid\-task, instead of refusing the reconnect. A [Sink](<#Sink>) that is also a [MarkerSink](<#MarkerSink>) can add one event to a log that was already closed, which the server uses to record why it stopped a task. A sink that implements none of them still works: the server falls back to the WriteEvent method of [LogWriter](<#LogWriter>), the agent stops reconnecting the stream, and the marker is skipped.

Everything exported here follows the pkg/ compatibility rule of ADR 0099: no breaking change in a patch release.

## Index

- [type Event](<#Event>)
- [type LineWriter](<#LineWriter>)
- [type LogWriter](<#LogWriter>)
- [type MarkerSink](<#MarkerSink>)
- [type Ref](<#Ref>)
- [type Resumer](<#Resumer>)
- [type Sink](<#Sink>)


<a name="Event"></a>
## type [Event](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L33-L43>)

Event is one line of a task log. Stored logs are JSON lines whose keys are the struct tags below.

```go
type Event struct {
    // Time is when the line was produced.
    Time time.Time `json:"ts"`
    // Level is the severity: debug, info, warning or error.
    Level string `json:"level"`
    // Stream is where the line came from: stdout, stderr, or system for a
    // line the server or the agent added.
    Stream string `json:"stream"`
    // Message is the text of the line, without a trailing newline.
    Message string `json:"msg"`
}
```

<a name="LineWriter"></a>
## type [LineWriter](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L76-L78>)

LineWriter is an optional interface of a [LogWriter](<#LogWriter>) that also accepts a line the server already encoded as JSON, without the trailing newline, so a line that is both stored and published to the live tail is encoded once.

```go
type LineWriter interface {
    WriteLine(line string) error
}
```

<a name="LogWriter"></a>
## type [LogWriter](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L68-L71>)

LogWriter appends the events of one task attempt. The server calls Close once, when the attempt's stream ends, and a sink flushes there: a log is complete only after Close returned nil.

```go
type LogWriter interface {
    WriteEvent(ev Event) error
    Close() error
}
```

<a name="MarkerSink"></a>
## type [MarkerSink](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L94-L96>)

MarkerSink is an optional interface of a [Sink](<#Sink>) that can append one event to an attempt's log while keeping what it already holds. The server uses it to add a terminal marker to the log of a task it killed.

```go
type MarkerSink interface {
    AppendEvent(ref Ref, ev Event) error
}
```

<a name="Ref"></a>
## type [Ref](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L56-L63>)

Ref identifies the log stream of one execution of one task try.

AttemptEpoch names one execution of the try: an infrastructure re\-place, a reschedule poke or a repeated dispatch runs the same try again, and each execution keeps its own stream. Epoch 0 is the stream every log had before the epoch existed, so a sink should store it where it stored logs before.

TenantID and RunID are the server's ids of the tenant and of the DAG run, as UUID strings; DagID and TaskID are the ids the DAG declares. A sink that builds a path or an object key from them must refuse a value that would leave its own namespace, such as one holding a path separator or "..".

```go
type Ref struct {
    TenantID     string
    DagID        string
    RunID        string
    TaskID       string
    TryNumber    int
    AttemptEpoch int
}
```

<a name="Resumer"></a>
## type [Resumer](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L102-L104>)

Resumer is an optional interface of a [Sink](<#Sink>) that can continue the stored stream of ref: the writer Resume returns keeps what is already stored and appends after it. The server calls it when an agent reopens the log stream of an attempt that is still running.

```go
type Resumer interface {
    Resume(ref Ref) (LogWriter, error)
}
```

<a name="Sink"></a>
## type [Sink](<https://github.com/dexadata/dexaflow/blob/main/pkg/logsink/logsink.go#L86-L89>)

Sink stores and retrieves task logs.

Open starts the stream of ref and may replace what was stored for it. Read returns the stored log of ref, and an error wrapping [io/fs.ErrNotExist](<https://pkg.go.dev/io/fs/#ErrNotExist>) when nothing was stored for it, which the API reports as a log that is not there yet.

```go
type Sink interface {
    Open(ref Ref) (LogWriter, error)
    Read(ctx context.Context, ref Ref) (io.ReadCloser, error)
}
```

<details><summary>Example</summary>
<p>

A sink that stores logs in memory: the server opens a writer per attempt, writes its events and closes it, and reads it back for the API.

```go
package main

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
func main() {
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
}
```

#### Output

```
{"ts":"2026-11-02T09:00:00Z","level":"info","stream":"stdout","msg":"extracted 42 rows"}
no log for etl/load try 1: file does not exist
```

</p>
</details>

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
