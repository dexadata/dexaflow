// Package logs ships task logs from the agent to durable storage and serves
// them back via the API, so logs remain available after the task pod is gone.
package logs

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// flushThreshold is the buffered-writer size; a full buffer flushes to storage,
// bounding how much is lost on a crash and how stale a live reader can be.
const flushThreshold = 1 << 20 // 1 MB

// Event is one structured log line: when it was emitted, its severity level and
// source stream (stdout/stderr), and the message text. Logs are stored as JSONL
// (one Event per line) so the UI's drill-down viewer can color by real level
// instead of guessing.
type Event struct {
	Time    time.Time `json:"ts"`
	Level   string    `json:"level"`
	Stream  string    `json:"stream"`
	Message string    `json:"msg"`
}

// DecodeLine parses a stored log line into an Event. Lines that are not JSON
// (legacy plain-text logs written before structured storage) decode as a
// stdout message with a level inferred from the text, so the reader serves both
// formats with sensible coloring.
func DecodeLine(line string) Event {
	if strings.HasPrefix(line, "{") {
		var ev Event
		if err := json.Unmarshal([]byte(line), &ev); err == nil {
			if ev.Level == "" {
				ev.Level = "info"
			}
			if ev.Stream == "" {
				ev.Stream = "stdout"
			}
			return ev
		}
	}
	return Event{Level: inferLevel(line), Stream: "stdout", Message: line}
}

// inferLevel guesses a level from a legacy plain line's text, defaulting to
// info when nothing in the text indicates a severity.
func inferLevel(line string) string { return RefineLevel(line, "info") }

// RefineLevel returns the severity indicated by a log line's own text when it
// carries a clear level token, otherwise the supplied fallback. It lets the line
// content correct a level that was derived only from the source stream — so an
// "ERROR …" line printed to stdout is colored error (not info), and an "INFO …"
// line written to stderr is colored info (not error). When the text gives no
// signal, the stream-derived fallback stands, so behavior is unchanged for plain
// output. The returned values match the Event.Level vocabulary the UI colors by
// (error/warning/info/debug); callers control the contract, this only sharpens
// the level value.
func RefineLevel(line, fallback string) string {
	if lvl, ok := levelFromContent(line); ok {
		return lvl
	}
	return fallback
}

// levelFromContent reports the severity a line's text indicates, if any. Tokens
// are matched uppercase (standard logger output: Python logging, structured
// loggers) to avoid false positives from ordinary lowercase prose. error is
// checked first so "ERROR" wins over a stray "INFO" later in the same line.
func levelFromContent(line string) (string, bool) {
	switch {
	case containsAny(line, "ERROR", "CRITICAL", "FATAL", "Traceback", "Exception"):
		return "error", true
	case containsAny(line, "WARNING", "WARN"):
		return "warning", true
	case containsAny(line, "DEBUG"):
		return "debug", true
	case containsAny(line, "INFO", "NOTICE"):
		return "info", true
	default:
		return "", false
	}
}

// containsAny reports whether s contains any of the given substrings.
func containsAny(s string, subs ...string) bool {
	for _, sub := range subs {
		if strings.Contains(s, sub) {
			return true
		}
	}
	return false
}

// EncodeLine serializes an Event to the same JSON line used for storage, so the
// live-tail channel carries the full event (level + stream + timestamp), not
// just the message text. The result is always one JSON object: a zero time is
// stamped with the current time, and so is a time JSON cannot encode (a year
// outside 0 to 9999, which time.MarshalJSON refuses). It used to hand the raw
// message back on that marshal error, and a raw message holding a newline is
// stored as two lines, so whoever controls the message text and the timestamp
// (the agent of an attempt, through its own token) could forge a stored log
// entry that way.
func EncodeLine(ev Event) string {
	if ev.Time.IsZero() || !jsonEncodableTime(ev.Time) {
		ev.Time = time.Now().UTC()
	}
	encoded, err := json.Marshal(ev)
	if err == nil {
		return string(encoded)
	}
	// Not reachable: the time is in range and the other fields are strings,
	// which encoding/json always encodes (invalid UTF-8 is replaced, not
	// refused). Kept so that no future field brings the raw message back:
	// the strings alone, still as one JSON object.
	encoded, err = json.Marshal(struct {
		Level   string `json:"level"`
		Stream  string `json:"stream"`
		Message string `json:"msg"`
	}{ev.Level, ev.Stream, ev.Message})
	if err != nil {
		// Three strings always encode; an empty object is the last resort,
		// never the raw message.
		return "{}"
	}
	return string(encoded)
}

// jsonEncodableTime reports whether encoding/json can encode t: time.MarshalJSON
// refuses a year outside 0 to 9999, the four digits RFC 3339 has for it.
func jsonEncodableTime(t time.Time) bool {
	y := t.Year()
	return y >= 0 && y <= 9999
}

// Ref identifies a task instance's log stream and maps to its storage location.
//
// AttemptEpoch names one execution of the try (ADR 0051 amendment): an infra
// re-place, a reschedule poke or a repeated dispatch runs the same try again,
// and each execution keeps its own stream. Epoch 0 maps to the key every log
// had before the epoch existed, so those logs are still found.
type Ref struct {
	TenantID     string
	DagID        string
	RunID        string
	TaskID       string
	TryNumber    int
	AttemptEpoch int
}

// fileName is the last segment of the ref's storage location:
// {try}.log for epoch 0 and {try}.e{epoch}.log otherwise.
func (r Ref) fileName() string {
	if r.AttemptEpoch == 0 {
		return fmt.Sprintf("%d.log", r.TryNumber)
	}
	return fmt.Sprintf("%d.e%d.log", r.TryNumber, r.AttemptEpoch)
}

// LogWriter appends structured log events for one task attempt and flushes on
// Close.
type LogWriter interface {
	WriteEvent(ev Event) error
	Close() error
}

// LineWriter is implemented by LogWriters that also accept a line already
// encoded by EncodeLine (without the trailing newline), so a caller that both
// stores and publishes a line encodes it once. DiskSink and ObjectSink writers
// implement it; a caller type-asserts and falls back to WriteEvent.
type LineWriter interface {
	WriteLine(line string) error
}

// Sink stores and retrieves task logs.
type Sink interface {
	Open(ref Ref) (LogWriter, error)
	Read(ref Ref) (io.ReadCloser, error)
}

// MarkerSink appends a single event to an attempt's existing log while
// PRESERVING prior content: O_APPEND on disk, read-modify-write on an object
// store (which has no native append, so Open+Close there would overwrite the
// whole object). A reaper uses it to add a terminal marker to a killed task's
// log without clobbering the streamed output (#861). DiskSink and ObjectSink
// both implement it; a caller holding a Sink type-asserts to reach it.
type MarkerSink interface {
	AppendEvent(ref Ref, ev Event) error
}

// DiskSink writes logs to ${root}/{tenant}/{dag}/{run}/{task}/{try}.log, or
// {try}.e{epoch}.log for an execution with a non-zero attempt epoch.
type DiskSink struct {
	root string
}

// NewDiskSink builds a DiskSink rooted at dir.
func NewDiskSink(dir string) *DiskSink { return &DiskSink{root: dir} }

// withRoot runs fn against a descriptor pinned to the sink root and hands back
// the file fn opened. The root is closed before returning — an already-opened
// file stays valid — so the descriptor does not outlive the call, and a Close
// failure is reported rather than dropped.
func (d *DiskSink) withRoot(fn func(*os.Root) (*os.File, error)) (*os.File, error) {
	root, err := os.OpenRoot(d.root)
	if err != nil {
		return nil, fmt.Errorf("opening log root: %w", err)
	}
	f, ferr := fn(root)
	cerr := root.Close()
	switch {
	case ferr != nil:
		return nil, ferr
	case cerr != nil:
		return nil, errors.Join(fmt.Errorf("closing log root: %w", cerr), f.Close())
	}
	return f, nil
}

// rel is the storage location relative to the sink root, for use with os.Root.
func (d *DiskSink) rel(ref Ref) string {
	return filepath.Join(ref.TenantID, ref.DagID, ref.RunID, ref.TaskID, ref.fileName())
}

// ErrUnsafeRef reports a Ref whose fields cannot be used as path segments.
var ErrUnsafeRef = errors.New("unsafe log reference")

// validate rejects a Ref that would not resolve inside the sink root.
//
// Every field is interpolated into the storage path, so a field carrying a
// separator or a parent reference escapes the root — and since the caller also
// controls the bytes written, that is an arbitrary file write performed by the
// control plane. The reachable field is RunID: the trigger endpoint takes
// dag_run_id verbatim from the request body, and it travels to here inside the
// agent's own signed token, so nothing downstream sees it as caller input.
//
// This is the readable-error layer, not the containment: os.Root is what makes
// an escape impossible, including the case a string check cannot see — a symlink
// inside the root whose every path component is a legal name. Both are kept
// because they fail differently. validate names the offending field, and os.Root
// holds even if this charset later turns out to be incomplete.
func (r Ref) validate() error {
	for _, f := range []struct{ name, value string }{
		{"tenant_id", r.TenantID},
		{"dag_id", r.DagID},
		{"run_id", r.RunID},
		{"task_id", r.TaskID},
	} {
		if err := safeSegment(f.value); err != nil {
			return fmt.Errorf("%w: %s: %w", ErrUnsafeRef, f.name, err)
		}
	}
	if r.AttemptEpoch < 0 {
		return fmt.Errorf("%w: attempt_epoch: is negative", ErrUnsafeRef)
	}
	return nil
}

// safeSegment accepts a value usable as exactly one path segment. It bans
// separators and parent references, not punctuation: Airflow-generated run ids
// embed an RFC3339 timestamp ("scheduled__2026-07-30T12:00:00+00:00"), and
// rejecting ':' or '+' would break every scheduled run.
func safeSegment(v string) error {
	switch {
	case v == "":
		return errors.New("is empty")
	case v == "." || v == "..":
		return errors.New("is a parent or current directory reference")
	case strings.ContainsAny(v, `/\`):
		return errors.New("contains a path separator")
	case strings.ContainsRune(v, 0):
		return errors.New("contains a null byte")
	case len(v) > 255:
		return errors.New("exceeds 255 bytes")
	}
	return nil
}

// Open creates the log file (appending if it exists) and returns a buffered writer.
func (d *DiskSink) Open(ref Ref) (LogWriter, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(d.root, 0o750); err != nil {
		return nil, fmt.Errorf("creating log root: %w", err)
	}
	f, err := d.withRoot(func(root *os.Root) (*os.File, error) {
		rel := d.rel(ref)
		if merr := root.MkdirAll(filepath.Dir(rel), 0o750); merr != nil {
			return nil, fmt.Errorf("creating log directory: %w", merr)
		}
		file, oerr := root.OpenFile(rel, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o640)
		if oerr != nil {
			return nil, fmt.Errorf("opening log file: %w", oerr)
		}
		return file, nil
	})
	if err != nil {
		return nil, err
	}
	return &diskWriter{f: f, buf: bufio.NewWriterSize(f, flushThreshold)}, nil
}

// AppendEvent adds one event to the attempt's log file. The disk sink already
// opens with O_APPEND, so this is a plain open-write-close that preserves the
// streamed content — the marker lands after whatever the agent wrote (#861).
func (d *DiskSink) AppendEvent(ref Ref, ev Event) error {
	w, err := d.Open(ref)
	if err != nil {
		return err
	}
	return errors.Join(w.WriteEvent(ev), w.Close())
}

// Prune deletes log files whose last modification is older than retention,
// reclaiming space for completed runs. A missing root is not an error.
func (d *DiskSink) Prune(now time.Time, retention time.Duration) error {
	cutoff := now.Add(-retention)
	err := filepath.WalkDir(d.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(path) != ".log" {
			return nil
		}
		info, ierr := entry.Info()
		if ierr != nil {
			return ierr
		}
		if info.ModTime().Before(cutoff) {
			//nolint:gosec // G122: the log root is server-owned, not an untrusted symlink tree.
			if rerr := os.Remove(path); rerr != nil {
				return rerr
			}
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

// StoredEpochs lists the attempt epochs with a log file for ref's try, with
// one directory read (see EpochLister). A task directory that does not exist
// yet holds none.
func (d *DiskSink) StoredEpochs(ref Ref) ([]int, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	dir, err := d.withRoot(func(root *os.Root) (*os.File, error) {
		return root.Open(filepath.Dir(d.rel(ref)))
	})
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("opening log directory: %w", err)
	}
	names, rerr := dir.Readdirnames(-1)
	if cerr := dir.Close(); rerr == nil {
		rerr = cerr
	}
	if rerr != nil {
		return nil, fmt.Errorf("listing log directory: %w", rerr)
	}
	var epochs []int
	for _, name := range names {
		if e, ok := parseEpochName(name, ref.TryNumber); ok {
			epochs = append(epochs, e)
		}
	}
	return epochs, nil
}

// Read opens the log file for reading.
func (d *DiskSink) Read(ref Ref) (io.ReadCloser, error) {
	if err := ref.validate(); err != nil {
		return nil, err
	}
	f, err := d.withRoot(func(root *os.Root) (*os.File, error) {
		file, oerr := root.Open(d.rel(ref))
		if oerr != nil {
			return nil, fmt.Errorf("opening log file: %w", oerr)
		}
		return file, nil
	})
	if err != nil {
		return nil, err
	}
	return f, nil
}

// diskWriter buffers line writes and flushes them to the file.
type diskWriter struct {
	f   *os.File
	buf *bufio.Writer
}

// WriteEvent appends an event as a JSON line; the buffer flushes automatically
// at flushThreshold.
func (w *diskWriter) WriteEvent(ev Event) error {
	if ev.Time.IsZero() {
		ev.Time = time.Now().UTC()
	}
	encoded, err := json.Marshal(ev)
	if err != nil {
		return fmt.Errorf("encoding log event: %w", err)
	}
	if _, err := w.buf.Write(append(encoded, '\n')); err != nil {
		return fmt.Errorf("writing log line: %w", err)
	}
	return nil
}

// WriteLine appends a line already encoded by EncodeLine.
func (w *diskWriter) WriteLine(line string) error {
	if _, err := w.buf.WriteString(line); err != nil {
		return fmt.Errorf("writing log line: %w", err)
	}
	if err := w.buf.WriteByte('\n'); err != nil {
		return fmt.Errorf("writing log line: %w", err)
	}
	return nil
}

// Close flushes any buffered lines and closes the file.
func (w *diskWriter) Close() error {
	return errors.Join(w.buf.Flush(), w.f.Close())
}
