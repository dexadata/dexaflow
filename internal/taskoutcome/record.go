// Package taskoutcome defines the durable task-outcome record the agent writes to
// its container termination message before delivering the report, and the
// reconciler reads back as the source of truth over pod phase (ADR 0052).
//
// The record decouples a task's true result from the delivery of that result: a
// pod killed mid-report still leaves the outcome behind, so a task that succeeded
// but lost its report is not misread as a failure. The format is a compact,
// versioned JSON document kept far under the Kubernetes termination-message cap;
// it carries the outcome only, never logs or return values.
package taskoutcome

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"
)

// Version is the record schema version. The reader rejects any other version so
// the format can evolve without a stale reader misinterpreting a newer record.
const Version = 1

// maxRecordBytes bounds an encoded record. The real Kubernetes cap is ~4 KiB; a
// record is a few dozen bytes, so a value far above any real record but well under
// the cap catches a programming error (a giant field) without false positives.
const maxRecordBytes = 1024

// Outcome is a task's true result, independent of report delivery.
type Outcome string

const (
	// Success means the user task completed successfully.
	Success Outcome = "success"
	// Failed means the user task failed; ExitCode carries the process exit code.
	Failed Outcome = "failed"
	// Reschedule means a reschedule-mode sensor poked not-ready; RescheduleAt
	// carries the next-poke time. Never settled as a failure by the reconciler.
	Reschedule Outcome = "reschedule"
)

// MaxReasonLen bounds a Reason so an unexpectedly long classification can never
// push a record past the termination-message cap. A classification is a short
// sentence; this ceiling is far above any real one and well under the cap.
const MaxReasonLen = 240

// Record is the durable outcome document. Only the fields relevant to an outcome
// are populated: ExitCode for Failed, RescheduleAt for Reschedule, Reason for a
// failure the agent classified itself.
type Record struct {
	V            int     `json:"v"`
	Outcome      Outcome `json:"outcome"`
	ExitCode     *int32  `json:"exit_code,omitempty"`
	RescheduleAt string  `json:"reschedule_at,omitempty"`
	// Reason is a short, human-readable classification of a failure the agent
	// observed before it could report anything — the pre-registration blind spot
	// where the control plane sees only a dead pod. It is optional and additive:
	// a reader that does not know the field still decodes the record as a plain
	// failure, and a writer that never sets it produces today's bytes exactly.
	//
	// It carries a CLASSIFICATION, never a raw error. The agent maps the failure
	// either to one of a closed set of operator-facing constants (the bootstrap
	// and environment-build classifiers in internal/agent) or to a fixed template
	// whose only variable is the timeout the task itself declared; a failure its
	// classifiers recognize nothing in records NO reason at all rather than
	// falling back to the error's text. So nothing derived from a credential or an
	// internal error path can reach this durable, end-user-visible field. A reader
	// still bounds it: the record can also arrive from a task that wrote its own
	// termination message.
	Reason string `json:"reason,omitempty"`
	// AttemptEpoch names the execution of the try that wrote the record (ADR
	// 0052 amendment), as the task spec gave it to the agent. It is optional
	// and additive like Reason: v stays 1, an old reader ignores it, and a
	// record without it (an older agent, or epoch 0) is read against the pod's
	// label alone. A reader that finds an epoch different from the pod's label
	// treats the record as absent.
	AttemptEpoch *int64 `json:"attempt_epoch,omitempty"`
}

// WithAttemptEpoch returns the record stamped with the execution that wrote
// it. Epoch 0 is left out, so the record keeps today's bytes: an agent served
// by a replica that predates the field sees 0 for every execution, and the
// pod's label is the safer answer then.
func (r Record) WithAttemptEpoch(epoch int64) Record {
	if epoch > 0 {
		r.AttemptEpoch = &epoch
	}
	return r
}

// Succeeded returns a success record.
func Succeeded() Record { return Record{V: Version, Outcome: Success} }

// FailedWith returns a failure record carrying the user process exit code.
func FailedWith(exitCode int32) Record {
	return Record{V: Version, Outcome: Failed, ExitCode: &exitCode}
}

// FailedBecause returns a failure record carrying a classified reason, for a
// failure the agent detected before any state could be reported. The reason is
// truncated to MaxReasonLen so an over-long classification degrades to a shorter
// message rather than losing the whole record to the size cap.
func FailedBecause(reason string) Record {
	return Record{V: Version, Outcome: Failed, Reason: TruncateReason(reason, MaxReasonLen)}
}

// FailedBecauseWith returns a failure record carrying BOTH the user process exit
// code and a classified reason. It is the shape a diagnosed failure of a process
// that did run needs (#930): the reason names the cause the control plane cannot
// otherwise derive — an execution_timeout the agent enforced itself — while the
// exit code keeps the raw signal the operator still wants to see. FailedBecause
// remains the constructor for a failure where no user process ever ran, and so
// has no exit code to report.
func FailedBecauseWith(exitCode int32, reason string) Record {
	return Record{
		V:        Version,
		Outcome:  Failed,
		ExitCode: &exitCode,
		Reason:   TruncateReason(reason, MaxReasonLen),
	}
}

// TruncateReason clamps a failure reason to limit bytes, cutting on a rune boundary
// so a truncated multi-byte character never produces invalid UTF-8.
//
// It is exported because every layer that carries a reason has to bound it, and
// they must all bound it the same way: the reason's sources include kubelet
// fields and a task's own reported error text, which are unbounded input for a
// value that is persisted and served to end users. The cap differs per layer
// (the termination message is far tighter than an HTTP response), so the limit is
// the caller's; only the cutting rule is shared.
func TruncateReason(reason string, limit int) string {
	if len(reason) <= limit {
		return reason
	}
	cut := reason[:limit]
	for cut != "" && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// RescheduledAt returns a reschedule record carrying the next-poke time. The time
// is stored in RFC3339Nano (UTC) so the reconciler can settle up_for_reschedule
// with the real poke time rather than inventing one.
func RescheduledAt(when time.Time) Record {
	return Record{V: Version, Outcome: Reschedule, RescheduleAt: when.UTC().Format(time.RFC3339Nano)}
}

// Encode renders the record as the string to write to the termination message.
// It errors on an unexpectedly large result, which would signal a programming
// error rather than a real outcome.
func (r Record) Encode() (string, error) {
	b, err := json.Marshal(r)
	if err != nil {
		return "", err
	}
	if len(b) > maxRecordBytes {
		return "", errors.New("taskoutcome: encoded record exceeds size cap")
	}
	return string(b), nil
}

// At parses the reschedule time; ok is false unless the record is a reschedule
// carrying a parseable RFC3339 time.
func (r Record) At() (time.Time, bool) {
	if r.Outcome != Reschedule || r.RescheduleAt == "" {
		return time.Time{}, false
	}
	when, err := time.Parse(time.RFC3339Nano, r.RescheduleAt)
	if err != nil {
		return time.Time{}, false
	}
	return when, true
}

// Decode parses a termination message into a Record. ok is false for anything
// that is not a valid, current-version record with a known outcome — an empty
// message, arbitrary text, a log tail, an unknown version, or a reschedule with no
// parseable time — so the caller falls back to pod phase. Decode never errors; a
// malformed message is simply "no record".
func Decode(msg string) (Record, bool) {
	msg = strings.TrimSpace(msg)
	if msg == "" || msg[0] != '{' {
		return Record{}, false
	}
	var r Record
	if err := json.Unmarshal([]byte(msg), &r); err != nil {
		return Record{}, false
	}
	if r.V != Version {
		return Record{}, false
	}
	switch r.Outcome {
	case Success, Failed:
		return r, true
	case Reschedule:
		if _, ok := r.At(); !ok {
			return Record{}, false
		}
		return r, true
	default:
		return Record{}, false
	}
}
