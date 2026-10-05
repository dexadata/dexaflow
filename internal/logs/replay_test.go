package logs

import (
	"testing"
	"time"
)

// TestMarkReplayRoundTrip: the replay flag rides on the published JSON line,
// is invisible to DecodeLine (so a follower that predates it shows the line
// unchanged), and SplitReplay restores the exact stored encoding.
func TestMarkReplayRoundTrip(t *testing.T) {
	line := EncodeLine(Event{Time: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Level: "info", Stream: "stdout", Message: `a "quoted" }`})
	marked := MarkReplay(line)
	if marked == line {
		t.Fatal("MarkReplay left the line unchanged")
	}
	if got := DecodeLine(marked); got != DecodeLine(line) {
		t.Errorf("DecodeLine(marked) = %+v, want %+v", got, DecodeLine(line))
	}
	raw, replay := SplitReplay(marked)
	if !replay || raw != line {
		t.Errorf("SplitReplay(marked) = %q, %v; want %q, true", raw, replay, line)
	}
	if raw, replay := SplitReplay(line); replay || raw != line {
		t.Errorf("SplitReplay(unmarked) = %q, %v; want the line, false", raw, replay)
	}
	if got := MarkReplay("plain text"); got != "plain text" {
		t.Errorf("MarkReplay(plain) = %q, want a legacy plain line left alone", got)
	}
}
