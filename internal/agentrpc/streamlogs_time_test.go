package agentrpc

import (
	"io"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/logs"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// TestWriteLineRefusesInvalidTimestamp: a line whose timestamp lies outside
// what a Timestamp can hold (here the first second of year 10000) is refused
// as the peer's error and nothing of it is stored or published. The sink's
// encoder used to hand such a line back raw, so a newline in its message
// became a second stored line composed by the sender; it now stamps the line
// with the current time instead, and the stored log keeps one JSON object per
// line either way. A line with no timestamp is still taken.
func TestWriteLineRefusesInvalidTimestamp(t *testing.T) {
	sink := logs.NewDiskSink(t.TempDir())
	forged := "real\n{\"ts\":\"2026-01-01T00:00:00Z\",\"level\":\"error\",\"stream\":\"stderr\",\"msg\":\"FORGED\"}"
	stored := func(ref logs.Ref) string {
		t.Helper()
		rc, err := sink.Read(ref)
		if err != nil {
			t.Fatalf("Read() error = %v", err)
		}
		defer func() { _ = rc.Close() }()
		b, err := io.ReadAll(rc)
		if err != nil {
			t.Fatalf("reading the log back: %v", err)
		}
		return string(b)
	}

	ref := logs.Ref{TenantID: "tn", DagID: "d", RunID: "r", TaskID: "t", TryNumber: 1}
	w, err := sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	var published []string
	werr := writeLine(w, &agentv1.LogLine{Time: &timestamppb.Timestamp{Seconds: 253402300800}, Message: forged},
		func(l string) { published = append(published, l) }, nil)
	if status.Code(werr) != codes.InvalidArgument {
		t.Fatalf("writeLine() error = %v, want InvalidArgument", werr)
	}
	if len(published) != 0 {
		t.Errorf("published %q, want nothing", published)
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	if got := stored(ref); got != "" {
		t.Errorf("stored %q, want nothing", got)
	}

	ref.TryNumber = 2
	w, err = sink.Open(ref)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	for _, line := range []*agentv1.LogLine{
		{Time: timestamppb.Now(), Message: forged, Stream: "stdout"},
		{Message: "no stamp", Stream: "stdout"},
	} {
		if werr := writeLine(w, line, func(string) {}, nil); werr != nil {
			t.Fatalf("writeLine(%q) error = %v", line.GetMessage(), werr)
		}
	}
	if cerr := w.Close(); cerr != nil {
		t.Fatalf("Close() error = %v", cerr)
	}
	lines := strings.Split(strings.TrimRight(stored(ref), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("stored %d lines, want 2: %q", len(lines), lines)
	}
	if got := logs.DecodeLine(lines[0]).Message; got != forged {
		t.Errorf("first stored line decodes to %q, want the message with its newline escaped", got)
	}
	if got := logs.DecodeLine(lines[1]).Message; got != "no stamp" {
		t.Errorf("second stored line decodes to %q, want %q", got, "no stamp")
	}
}
