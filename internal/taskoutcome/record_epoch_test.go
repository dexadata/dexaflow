package taskoutcome

import (
	"strings"
	"testing"
)

// TestRecordCarriesAttemptEpoch: a record names the execution of the try that
// wrote it (ADR 0052 amendment), so the reconciler can refuse a record that
// belongs to another execution than the pod's label says.
func TestRecordCarriesAttemptEpoch(t *testing.T) {
	enc, err := Succeeded().WithAttemptEpoch(3).Encode()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(enc, `"attempt_epoch":3`) {
		t.Fatalf("encoded record %s must carry attempt_epoch", enc)
	}
	rec, ok := Decode(enc)
	if !ok || rec.AttemptEpoch == nil || *rec.AttemptEpoch != 3 {
		t.Fatalf("decoded %+v ok=%v, want attempt epoch 3", rec, ok)
	}
}

// TestRecordWithoutEpochKeepsTodaysBytes: epoch 0 is not stamped (an agent
// served by an older replica sees 0 for every execution), so its record is
// byte for byte today's and the reader uses the pod's label.
func TestRecordWithoutEpochKeepsTodaysBytes(t *testing.T) {
	for _, rec := range []Record{Succeeded(), Succeeded().WithAttemptEpoch(0)} {
		enc, err := rec.Encode()
		if err != nil {
			t.Fatal(err)
		}
		if enc != `{"v":1,"outcome":"success"}` {
			t.Errorf("encoded = %s, want today's bytes", enc)
		}
	}
}

// TestOldRecordStillDecodes: a record written before the field existed decodes
// with no epoch, and v stays 1, so an old reader also decodes a new record.
func TestOldRecordStillDecodes(t *testing.T) {
	rec, ok := Decode(`{"v":1,"outcome":"success"}`)
	if !ok || rec.AttemptEpoch != nil {
		t.Fatalf("an old record must decode with no epoch: %+v ok=%v", rec, ok)
	}
	enc, _ := FailedWith(2).WithAttemptEpoch(5).Encode()
	if !strings.Contains(enc, `"v":1`) {
		t.Errorf("the version must stay 1, got %s", enc)
	}
}
