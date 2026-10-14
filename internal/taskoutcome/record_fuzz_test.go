package taskoutcome

import (
	"reflect"
	"testing"
	"time"
	"unicode/utf8"
)

// FuzzDecode feeds arbitrary termination messages to Decode. The message is
// written from inside the task container, so a task can put anything there
// (ADR 0052, "The trust caveat"). Decode must never panic, and a record it
// accepts must survive an Encode/Decode round trip unchanged.
func FuzzDecode(f *testing.F) {
	for _, r := range []Record{
		Succeeded(),
		FailedWith(137),
		FailedBecause("image pull failed"),
		FailedBecauseWith(1, "execution_timeout of 30s exceeded"),
		RescheduledAt(time.Date(2026, 10, 12, 8, 30, 0, 0, time.UTC)),
		Succeeded().WithAttemptEpoch(3),
	} {
		msg, err := r.Encode()
		if err != nil {
			f.Fatal(err)
		}
		f.Add(msg)
	}
	f.Add("")
	f.Add("Traceback (most recent call last):")
	f.Add(`{"v":2,"outcome":"success"}`)
	f.Add(`{"v":1,"outcome":"reschedule","reschedule_at":"not a time"}`)
	f.Add(`{"v":1,"outcome":"failed","exit_code":-2147483648,"reason":"\xff"}`)

	f.Fuzz(func(t *testing.T, msg string) {
		r, ok := Decode(msg)
		if !ok {
			if !reflect.DeepEqual(r, Record{}) {
				t.Fatalf("rejected message returned a non-empty record %+v", r)
			}
			return
		}
		if r.V != Version {
			t.Fatalf("accepted record has version %d", r.V)
		}
		encoded, err := r.Encode()
		if err != nil {
			// Decode does not bound the size of what it reads (the
			// termination message itself is capped by the kubelet), so a
			// large accepted record may not re-encode under the writer's cap.
			return
		}
		again, ok := Decode(encoded)
		if !ok {
			t.Fatalf("re-encoded record %q is rejected", encoded)
		}
		if !reflect.DeepEqual(again, r) {
			t.Fatalf("round trip changed the record:\n got  %+v\n want %+v", again, r)
		}
	})
}

// FuzzTruncateReason checks the cutting rule every layer applies to a failure
// reason: the result is a prefix of the input, fits the limit, and is valid
// UTF-8 whenever the input was.
func FuzzTruncateReason(f *testing.F) {
	f.Add("exit code 1", 240)
	f.Add("café 日本語", 5)
	f.Add("\xff\xfe", 1)
	f.Add("", 0)
	f.Fuzz(func(t *testing.T, reason string, limit int) {
		if limit < 0 || limit > 4096 {
			t.Skip("callers pass a fixed, positive limit")
		}
		got := TruncateReason(reason, limit)
		if len(got) > limit {
			t.Fatalf("TruncateReason(%q, %d) = %q, longer than the limit", reason, limit, got)
		}
		if reason[:len(got)] != got {
			t.Fatalf("TruncateReason(%q, %d) = %q, not a prefix", reason, limit, got)
		}
		if utf8.ValidString(reason) && !utf8.ValidString(got) {
			t.Fatalf("TruncateReason(%q, %d) = %q, invalid UTF-8", reason, limit, got)
		}
	})
}
