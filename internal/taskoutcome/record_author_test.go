package taskoutcome

import (
	"strings"
	"testing"
)

// TestAgentReasonRecordsCarryTheAuthorMarker (#948): the constructors the agent
// uses for a classified failure stamp the record as agent-authored, and the
// marker survives an encode/decode round trip, so the reconciler can tell a
// reason the platform wrote from one a task wrote into the same file.
func TestAgentReasonRecordsCarryTheAuthorMarker(t *testing.T) {
	for name, rec := range map[string]Record{
		"FailedBecause":     FailedBecause("bootstrap refused"),
		"FailedBecauseWith": FailedBecauseWith(255, "execution_timeout: task exceeded 10s limit"),
	} {
		t.Run(name, func(t *testing.T) {
			enc, err := rec.Encode()
			if err != nil {
				t.Fatal(err)
			}
			got, ok := Decode(enc)
			if !ok {
				t.Fatalf("record must decode: %q", enc)
			}
			if !got.AgentAuthored() {
				t.Errorf("a reason the agent classified must decode as agent-authored: %q", enc)
			}
		})
	}
}

// TestRecordWithoutMarkerIsNotAgentAuthored: a record written by hand (a task
// writing its own termination message) or by an agent that predates the marker
// still decodes, so the outcome settles, but it is not agent-authored.
func TestRecordWithoutMarkerIsNotAgentAuthored(t *testing.T) {
	rec, ok := Decode(`{"v":1,"outcome":"failed","exit_code":1,"reason":"the control plane rejected this pod's token"}`)
	if !ok {
		t.Fatal("an unmarked failure record must still decode")
	}
	if rec.AgentAuthored() {
		t.Error("an unmarked record must not count as agent-authored")
	}
	if rec.Reason == "" {
		t.Error("the unmarked reason is kept on the record for the reader to label, not dropped")
	}
}

// TestRecordsWithoutReasonKeepTodaysBytes: the marker is about who wrote a
// reason, so records that carry none are byte-identical to before.
func TestRecordsWithoutReasonKeepTodaysBytes(t *testing.T) {
	for want, rec := range map[string]Record{
		`{"v":1,"outcome":"success"}`:              Succeeded(),
		`{"v":1,"outcome":"failed","exit_code":2}`: FailedWith(2),
	} {
		enc, err := rec.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if enc != want {
			t.Errorf("encoded = %s, want %s", enc, want)
		}
		if strings.Contains(enc, "author") {
			t.Errorf("a record without a reason must not carry the marker: %s", enc)
		}
	}
}
