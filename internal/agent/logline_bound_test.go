package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// setMaxLogLineBytes lowers the partial-line bound for a test.
func setMaxLogLineBytes(t *testing.T, n int) {
	t.Helper()
	orig := maxLogLineBytes
	maxLogLineBytes = n
	t.Cleanup(func() { maxLogLineBytes = orig })
}

// TestLogWriterBoundsPartialLine pins the agent's log buffer bound: output with
// no newline (a progress bar, a binary dump) used to accumulate in the writer
// without limit, and a line past the control plane's 4 MiB gRPC message limit
// ended the whole log stream. A partial line that reaches the bound is emitted
// in pieces, nothing is lost, and the buffer never holds more than the bound.
func TestLogWriterBoundsPartialLine(t *testing.T) {
	setMaxLogLineBytes(t, 16)
	sink := &capSink{}
	w := &logWriter{sink: sink, stream: "stdout"}
	input := strings.Repeat("0123456789", 5) // 50 bytes, no newline
	for _, chunk := range []string{input[:7], input[7:30], input[30:]} {
		if _, err := w.Write([]byte(chunk)); err != nil {
			t.Fatal(err)
		}
		if len(w.buf) > maxLogLineBytes {
			t.Fatalf("partial-line buffer holds %d bytes, want at most %d", len(w.buf), maxLogLineBytes)
		}
	}
	w.flush()
	for _, m := range sink.msgs {
		if len(m) > maxLogLineBytes {
			t.Errorf("emitted a %d-byte line, want at most %d", len(m), maxLogLineBytes)
		}
	}
	if got := strings.Join(sink.msgs, ""); got != input {
		t.Errorf("pieces join to %q, want the whole input %q", got, input)
	}
	if w.splits == 0 {
		t.Error("splits = 0, want the split lines counted")
	}
}

// TestLogWriterSplitKeepsUTF8 cuts a long line on a rune boundary: LogLine's
// message is a proto string, which must be valid UTF-8 to be sent at all.
func TestLogWriterSplitKeepsUTF8(t *testing.T) {
	setMaxLogLineBytes(t, 8)
	sink := &capSink{}
	w := &logWriter{sink: sink}
	line := "aaaaaaa" + strings.Repeat("é", 10) // the first rune straddles byte 8
	if _, err := w.Write([]byte(line + "\n")); err != nil {
		t.Fatal(err)
	}
	for _, m := range sink.msgs {
		if !utf8.ValidString(m) {
			t.Errorf("emitted invalid UTF-8 %q", m)
		}
	}
	if got := strings.Join(sink.msgs, ""); got != line {
		t.Errorf("pieces join to %q, want %q", got, line)
	}
}

// countSink counts sent lines without keeping them.
type countSink struct{ sends int }

func (s *countSink) Send(*agentv1.LogLine) error { s.sends++; return nil }
func (s *countSink) Close() error                { return nil }

// BenchmarkLogWriterNoNewline measures a writer fed output that never ends a
// line, the case the bound exists for.
func BenchmarkLogWriterNoNewline(b *testing.B) {
	sink := &countSink{}
	w := &logWriter{sink: sink}
	chunk := []byte(strings.Repeat("x", 32<<10))
	b.SetBytes(int64(len(chunk)))
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if _, err := w.Write(chunk); err != nil {
			b.Fatal(err)
		}
	}
	b.ReportMetric(float64(cap(w.buf)), "buffered-cap-bytes")
}
