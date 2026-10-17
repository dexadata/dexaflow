package taskoutcome

import (
	"strings"
	"testing"
)

// TestUnexecutableRecordRoundTrips (#1594): the record the agent writes when
// the control plane never answered before the task's code ran is a failure
// with no exit code, an agent-authored reason and the infra marker, and all
// three survive an encode and decode, so the reconciler can put the attempt on
// the infra rail (ADR 0051, Unexecutable).
func TestUnexecutableRecordRoundTrips(t *testing.T) {
	enc, err := UnexecutableBecause("the control plane did not answer").WithAttemptEpoch(3).Encode()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := Decode(enc)
	if !ok {
		t.Fatalf("record must decode: %q", enc)
	}
	if got.Outcome != Failed {
		t.Errorf("outcome = %q, want %q: an older reader must still settle it as a failure", got.Outcome, Failed)
	}
	if !got.Infra {
		t.Errorf("infra marker lost in %q", enc)
	}
	if got.ExitCode != nil {
		t.Errorf("exit code = %d, want none: no user process ran", *got.ExitCode)
	}
	if !got.AgentAuthored() || got.Reason != "the control plane did not answer" {
		t.Errorf("reason = %q (agent-authored %v), want the agent's classification", got.Reason, got.AgentAuthored())
	}
	if got.AttemptEpoch == nil || *got.AttemptEpoch != 3 {
		t.Errorf("attempt epoch = %v, want 3", got.AttemptEpoch)
	}
}

// TestOtherRecordsKeepTheirBytes: the infra marker is additive like Reason and
// AttemptEpoch, so every record the agent wrote before keeps today's bytes.
func TestOtherRecordsKeepTheirBytes(t *testing.T) {
	for name, rec := range map[string]Record{
		"Succeeded":         Succeeded(),
		"FailedWith":        FailedWith(1),
		"FailedBecause":     FailedBecause("bootstrap refused"),
		"FailedBecauseWith": FailedBecauseWith(255, "execution_timeout: task exceeded 10s limit"),
	} {
		t.Run(name, func(t *testing.T) {
			enc, err := rec.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(enc, "infra") {
				t.Errorf("record carries the infra marker: %q", enc)
			}
		})
	}
}
