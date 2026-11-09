// Package logsink is the contract of the task log sink (ADR 0099): where the
// control plane stores the log of each task attempt and where it reads it back
// from for the API, the UI and the CLI.
//
// A [Sink] opens a [LogWriter] for one attempt, named by a [Ref], and the
// server writes the attempt's [Event] values to it as the agent streams them.
// The Read method of a [Sink] returns the stored log as JSON lines, one event
// per line, in the order they were written.
//
// Three optional interfaces let the server do more with a sink that supports
// them, and it checks for each with a type assertion. A [LogWriter] that is
// also a [LineWriter] takes a line the server already encoded. A [Sink] that
// is also a [Resumer] continues a stream after the agent's connection dropped
// mid-task, instead of refusing the reconnect. A [Sink] that is also a
// [MarkerSink] can add one event to a log that was already closed, which the
// server uses to record why it stopped a task. A sink that implements none of
// them still works: the server falls back to the WriteEvent method of
// [LogWriter], the agent stops reconnecting the stream, and the marker is
// skipped.
//
// Everything exported here follows the pkg/ compatibility rule of ADR 0099:
// no breaking change in a patch release.
package logsink

import (
	"context"
	"io"
	"time"
)

// Event is one line of a task log. Stored logs are JSON lines whose keys are
// the struct tags below.
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

// Ref identifies the log stream of one execution of one task try.
//
// AttemptEpoch names one execution of the try: an infrastructure re-place, a
// reschedule poke or a repeated dispatch runs the same try again, and each
// execution keeps its own stream. Epoch 0 is the stream every log had before
// the epoch existed, so a sink should store it where it stored logs before.
//
// TenantID and RunID are the server's ids of the tenant and of the DAG run,
// as UUID strings; DagID and TaskID are the ids the DAG declares. A sink that
// builds a path or an object key from them must refuse a value that would
// leave its own namespace, such as one holding a path separator or "..".
type Ref struct {
	TenantID     string
	DagID        string
	RunID        string
	TaskID       string
	TryNumber    int
	AttemptEpoch int
}

// LogWriter appends the events of one task attempt. The server calls Close
// once, when the attempt's stream ends, and a sink flushes there: a log is
// complete only after Close returned nil.
type LogWriter interface {
	WriteEvent(ev Event) error
	Close() error
}

// LineWriter is an optional interface of a [LogWriter] that also accepts a
// line the server already encoded as JSON, without the trailing newline, so a
// line that is both stored and published to the live tail is encoded once.
type LineWriter interface {
	WriteLine(line string) error
}

// Sink stores and retrieves task logs.
//
// Open starts the stream of ref and may replace what was stored for it. Read
// returns the stored log of ref, and an error wrapping [io/fs.ErrNotExist]
// when nothing was stored for it, which the API reports as a log that is not
// there yet.
type Sink interface {
	Open(ref Ref) (LogWriter, error)
	Read(ctx context.Context, ref Ref) (io.ReadCloser, error)
}

// MarkerSink is an optional interface of a [Sink] that can append one event
// to an attempt's log while keeping what it already holds. The server uses it
// to add a terminal marker to the log of a task it killed.
type MarkerSink interface {
	AppendEvent(ref Ref, ev Event) error
}

// Resumer is an optional interface of a [Sink] that can continue the stored
// stream of ref: the writer Resume returns keeps what is already stored and
// appends after it. The server calls it when an agent reopens the log stream
// of an attempt that is still running.
type Resumer interface {
	Resume(ref Ref) (LogWriter, error)
}
