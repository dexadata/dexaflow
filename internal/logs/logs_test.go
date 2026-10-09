package logs

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func ref() Ref {
	return Ref{TenantID: "acme", DagID: "etl", RunID: "run-1", TaskID: "extract", TryNumber: 1}
}

func TestRefChannel(t *testing.T) {
	if got := ref().Channel(); got != "log_tail:acme:etl:run-1:extract:1" {
		t.Errorf("Channel() = %q", got)
	}
}

// TestDiskSinkAppendEventPreservesExisting: the marker seam appends after the
// agent's streamed lines on disk (O_APPEND), never truncating them (#861).
func TestDiskSinkAppendEventPreservesExisting(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, err := sink.Open(ref())
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	if werr := w.WriteEvent(Event{Level: "info", Message: "Running with dbt=1.12.3"}); werr != nil {
		t.Fatalf("WriteEvent() error = %v", werr)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if aerr := sink.AppendEvent(ref(), Event{Level: "error", Stream: "system", Message: "killed: agent_lost"}); aerr != nil {
		t.Fatalf("AppendEvent() error = %v", aerr)
	}
	rc, err := sink.Read(ref())
	if err != nil {
		t.Fatalf("Read() error = %v", err)
	}
	defer func() { _ = rc.Close() }()
	raw, _ := io.ReadAll(rc)
	lines := strings.Split(strings.TrimRight(string(raw), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("want 2 lines (streamed + marker), got %d: %q", len(lines), string(raw))
	}
	if !strings.Contains(DecodeLine(lines[1]).Message, "agent_lost") {
		t.Errorf("last line = %q, want appended marker", lines[1])
	}
}

func TestDiskSinkWriteThenReadBack(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)

	w, err := sink.Open(ref())
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	for _, line := range []string{"first", "second"} {
		if werr := w.WriteEvent(Event{Level: "info", Stream: "stdout", Message: line}); werr != nil {
			t.Fatalf("WriteEvent: %v", werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close: %v", cerr)
	}

	// The file persists after the writer is closed (i.e. past pod termination)
	// and is stored as JSONL; decoding each line recovers the messages.
	rc, err := sink.Read(ref())
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	rawLines := strings.Split(strings.TrimSpace(string(data)), "\n")
	msgs := make([]string, 0, len(rawLines))
	for _, raw := range rawLines {
		msgs = append(msgs, DecodeLine(raw).Message)
	}
	if len(msgs) != 2 || msgs[0] != "first" || msgs[1] != "second" {
		t.Errorf("read back %q -> messages %v, want [first second]", data, msgs)
	}
}

func TestDecodeLineRoundTripAndLegacy(t *testing.T) {
	// JSONL round-trip preserves the real level/stream.
	dir := t.TempDir()
	w, err := NewDiskSink(dir).Open(ref())
	if err != nil {
		t.Fatal(err)
	}
	if werr := w.WriteEvent(Event{Level: "error", Stream: "stderr", Message: "boom"}); werr != nil {
		t.Fatal(werr)
	}
	_ = w.Close()
	rc, _ := NewDiskSink(dir).Read(ref())
	defer rc.Close()
	data, _ := io.ReadAll(rc)
	ev := DecodeLine(strings.TrimSpace(string(data)))
	if ev.Level != "error" || ev.Stream != "stderr" || ev.Message != "boom" {
		t.Errorf("round-trip lost fields: %+v", ev)
	}

	// Legacy plain lines decode as stdout with an inferred level.
	legacy := DecodeLine("ERROR something failed")
	if legacy.Level != "error" || legacy.Message != "ERROR something failed" {
		t.Errorf("legacy decode = %+v", legacy)
	}
	if DecodeLine("just a line").Level != "info" {
		t.Errorf("plain line should infer info")
	}
}

func TestDiskSinkPathLayout(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, err := sink.Open(ref())
	if err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	want := filepath.Join(dir, "acme", "etl", "run-1", "extract", "1.log")
	if _, err := os.Stat(want); err != nil {
		t.Errorf("expected log at %s: %v", want, err)
	}
}

func TestDiskSinkReadMissing(t *testing.T) {
	if _, err := NewDiskSink(t.TempDir()).Read(ref()); err == nil {
		t.Error("reading a non-existent log should error")
	}
}

func TestDiskSinkPruneDeletesOldLogs(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, err := sink.Open(ref())
	if err != nil {
		t.Fatal(err)
	}
	_ = w.WriteEvent(Event{Message: "x"})
	_ = w.Close()

	now := time.Now()
	logPath := filepath.Join(dir, "acme", "etl", "run-1", "extract", "1.log")
	old := now.Add(-40 * 24 * time.Hour)
	if cerr := os.Chtimes(logPath, old, old); cerr != nil {
		t.Fatal(cerr)
	}

	if perr := sink.Prune(now, 30*24*time.Hour); perr != nil {
		t.Fatalf("Prune: %v", perr)
	}
	if _, serr := os.Stat(logPath); !os.IsNotExist(serr) {
		t.Errorf("log older than retention should be pruned (stat err = %v)", serr)
	}
}

func TestDiskSinkPruneKeepsRecentAndMissingRoot(t *testing.T) {
	dir := t.TempDir()
	sink := NewDiskSink(dir)
	w, _ := sink.Open(ref())
	_ = w.Close()
	if err := sink.Prune(time.Now(), 30*24*time.Hour); err != nil {
		t.Fatalf("Prune recent: %v", err)
	}
	if _, err := sink.Read(ref()); err != nil {
		t.Errorf("recent log should be kept: %v", err)
	}
	// A never-written root must not error.
	if err := NewDiskSink(filepath.Join(dir, "nope")).Prune(time.Now(), time.Hour); err != nil {
		t.Errorf("pruning a missing root should be a no-op, got %v", err)
	}
}

func TestRefineLevel(t *testing.T) {
	cases := []struct {
		msg, fallback, want string
	}{
		// Content with a clear token wins over the stream-derived fallback.
		{"ERROR: connection refused", "info", "error"}, // error printed to stdout
		{"Traceback (most recent call last):", "info", "error"},
		{"CRITICAL boom", "info", "error"},
		{"INFO: started run", "error", "info"}, // info written to stderr
		{"WARNING: deprecated API", "error", "warning"},
		{"DEBUG cache hit", "info", "debug"},
		// No recognizable token -> the stream-derived fallback stands.
		{"processed 42 rows", "info", "info"},
		{"raw stderr noise", "error", "error"},
		{"", "info", "info"},
	}
	for _, c := range cases {
		if got := RefineLevel(c.msg, c.fallback); got != c.want {
			t.Errorf("RefineLevel(%q, %q) = %q, want %q", c.msg, c.fallback, got, c.want)
		}
	}
}

func TestInferLevelDefaultsToInfo(t *testing.T) {
	if inferLevel("nothing notable here") != "info" {
		t.Error("a line with no severity token should infer info")
	}
	if inferLevel("WARNING: heads up") != "warning" {
		t.Error("a WARNING line should infer warning")
	}
}

func TestEncodeLineRoundTrip(t *testing.T) {
	ev := Event{Level: "error", Stream: "stderr", Message: `boom: "quoted"`, Time: time.Now().UTC().Truncate(time.Second)}
	got := DecodeLine(EncodeLine(ev))
	if got.Level != "error" || got.Stream != "stderr" || got.Message != `boom: "quoted"` {
		t.Errorf("EncodeLine->DecodeLine lost fields: %+v", got)
	}
	if !got.Time.Equal(ev.Time) {
		t.Errorf("timestamp lost: got %v want %v", got.Time, ev.Time)
	}
}

// TestEncodeLineNeverReturnsRawMessage: a time JSON cannot encode (a year past
// 9999, or before 0) used to make EncodeLine hand the message back unencoded,
// so a message holding a newline was stored as two lines, the second one a log
// entry composed by whoever sent the message. The line is always one JSON
// object, and such a time is replaced by the current one.
func TestEncodeLineNeverReturnsRawMessage(t *testing.T) {
	forged := "real\n{\"ts\":\"2026-01-01T00:00:00Z\",\"level\":\"error\",\"stream\":\"stderr\",\"msg\":\"FORGED\"}"
	for name, ts := range map[string]time.Time{
		"year 10000": time.Date(10000, 1, 1, 0, 0, 0, 0, time.UTC),
		"year -1":    time.Date(-1, 1, 1, 0, 0, 0, 0, time.UTC),
		"zero":       {},
	} {
		t.Run(name, func(t *testing.T) {
			before := time.Now().UTC().Add(-time.Second)
			line := EncodeLine(Event{Time: ts, Level: "info", Stream: "stdout", Message: forged})
			if strings.Contains(line, "\n") {
				t.Fatalf("EncodeLine() = %q: more than one line", line)
			}
			if !strings.HasPrefix(line, "{") {
				t.Fatalf("EncodeLine() = %q, want a JSON object", line)
			}
			got := DecodeLine(line)
			if got.Message != forged || got.Level != "info" || got.Stream != "stdout" {
				t.Errorf("EncodeLine()->DecodeLine() = %+v, want the message escaped inside the event", got)
			}
			if got.Time.Before(before) || got.Time.After(time.Now().Add(time.Second)) {
				t.Errorf("time = %v, want the current time in place of %v", got.Time, ts)
			}
		})
	}
}

// A Ref component is a path segment. Any component carrying a separator or a
// parent reference escapes the log root, which turns a task's own log stream
// into an arbitrary file write by the control-plane process. run_id is the
// reachable one: the API takes it verbatim from the caller's JSON body.
func TestDiskSinkRejectsPathEscapingComponents(t *testing.T) {
	for _, tc := range []struct {
		name string
		ref  Ref
	}{
		{"run id parent traversal", Ref{TenantID: "acme", DagID: "etl", RunID: "../../../../pwned", TaskID: "extract", TryNumber: 1}},
		{"run id nested separator", Ref{TenantID: "acme", DagID: "etl", RunID: "a/../../b", TaskID: "extract", TryNumber: 1}},
		{"run id absolute", Ref{TenantID: "acme", DagID: "etl", RunID: "/etc/cron.d", TaskID: "extract", TryNumber: 1}},
		{"tenant traversal", Ref{TenantID: "..", DagID: "etl", RunID: "r", TaskID: "extract", TryNumber: 1}},
		{"dag traversal", Ref{TenantID: "acme", DagID: "../..", RunID: "r", TaskID: "extract", TryNumber: 1}},
		{"task traversal", Ref{TenantID: "acme", DagID: "etl", RunID: "r", TaskID: "../../x", TryNumber: 1}},
		{"empty component", Ref{TenantID: "acme", DagID: "", RunID: "r", TaskID: "extract", TryNumber: 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := filepath.Join(t.TempDir(), "logs")
			sink := NewDiskSink(root)

			w, err := sink.Open(tc.ref)
			if err == nil {
				_ = w.Close()
				t.Fatalf("Open accepted an escaping ref; wrote outside %s", root)
			}
			if _, rerr := sink.Read(tc.ref); rerr == nil {
				t.Error("Read accepted an escaping ref")
			}
			// Nothing may exist above the root, whether or not Open errored.
			if entries, derr := os.ReadDir(filepath.Dir(root)); derr == nil {
				for _, e := range entries {
					if e.Name() != "logs" {
						t.Errorf("created %s outside the log root", e.Name())
					}
				}
			}
		})
	}
}

// Airflow-generated run ids carry RFC3339 timestamps, so colons and plus signs
// must keep working; the guard rejects separators, not punctuation.
func TestDiskSinkAcceptsAirflowStyleRunID(t *testing.T) {
	root := t.TempDir()
	r := Ref{TenantID: "acme", DagID: "etl", RunID: "scheduled__2026-07-30T12:00:00+00:00", TaskID: "extract", TryNumber: 1}
	w, err := NewDiskSink(root).Open(r)
	if err != nil {
		t.Fatalf("rejected a legitimate Airflow run id: %v", err)
	}
	_ = w.Close()
}

// Segment validation cannot see a symlink: every component is a legal name, and
// the escape happens in the kernel when the path is resolved. os.Root refuses to
// traverse a link that leaves the root, which is why the sink uses it rather
// than relying on the string check alone.
func TestDiskSinkRefusesSymlinkEscape(t *testing.T) {
	base := t.TempDir()
	root := filepath.Join(base, "logs")
	outside := filepath.Join(base, "outside")
	if err := os.MkdirAll(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(outside, 0o750); err != nil {
		t.Fatal(err)
	}
	// A tenant directory that is really a link out of the root.
	if err := os.Symlink(outside, filepath.Join(root, "acme")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	sink := NewDiskSink(root)
	ref := Ref{TenantID: "acme", DagID: "etl", RunID: "r1", TaskID: "extract", TryNumber: 1}
	if w, err := sink.Open(ref); err == nil {
		_ = w.Close()
		t.Fatal("Open followed a symlink out of the log root")
	}
	if _, err := os.Stat(filepath.Join(outside, "etl")); err == nil {
		t.Error("wrote through the symlink into the outside directory")
	}
}
