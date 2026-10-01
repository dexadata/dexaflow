package storage

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

func TestUUIDRoundTrip(t *testing.T) {
	const s = "11111111-2222-3333-4444-555555555555"
	u, err := parseUUID(s)
	if err != nil {
		t.Fatal(err)
	}
	if got := uuidToString(u); got != s {
		t.Errorf("uuid round trip = %q, want %q", got, s)
	}
	if uuidToString(pgtype.UUID{}) != "" {
		t.Error("invalid uuid should map to empty string")
	}
	if _, err := parseUUID("not-a-uuid"); err == nil {
		t.Error("invalid uuid string should error")
	}
}

func TestTimestamptzHelpers(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	if timePtr(pgtype.Timestamptz{}) != nil {
		t.Error("invalid timestamptz should map to nil pointer")
	}
	if got := timePtr(pgtype.Timestamptz{Time: now, Valid: true}); got == nil || !got.Equal(now) {
		t.Errorf("timePtr = %v, want %v", got, now)
	}
	if !timeVal(pgtype.Timestamptz{}).IsZero() {
		t.Error("invalid timestamptz should map to zero time")
	}
	if !ptrTimestamptz(nil).Valid == false {
		t.Error("nil time should map to invalid timestamptz")
	}
	if ts := ptrTimestamptz(&now); !ts.Valid || !ts.Time.Equal(now) {
		t.Errorf("ptrTimestamptz = %+v", ts)
	}
}

func TestStrHelpers(t *testing.T) {
	if strOrEmpty(nil) != "" {
		t.Error("nil -> empty")
	}
	v := "x"
	if strOrEmpty(&v) != "x" {
		t.Error("ptr -> value")
	}
	if strPtr("") != nil {
		t.Error("empty -> nil")
	}
	if got := strPtr("y"); got == nil || *got != "y" {
		t.Error("value -> ptr")
	}
}

func TestMapDag(t *testing.T) {
	sched := "0 5 * * *"
	owner := "data"
	d := mapDag(queries.Dag{
		DagID: "etl", Owner: &owner, Tags: []string{"a"},
		Schedule: &sched, IsPaused: true, IsActive: true, MaxActiveRuns: 16, Catchup: true,
	})
	if d.DagID != "etl" || d.Owner != "data" || !d.IsPaused || !d.Catchup || d.MaxActiveRuns != 16 {
		t.Errorf("unexpected dag: %+v", d)
	}
	if d.Schedule == nil || *d.Schedule != sched {
		t.Errorf("schedule = %v", d.Schedule)
	}
}

func TestMapDagRun(t *testing.T) {
	now := time.Now().UTC()
	r := mapDagRun(queries.DagRun{
		RunID: "r1", State: queries.DagRunStateRunning, Trigger: queries.DagRunTriggerManual,
		LogicalDate: pgtype.Timestamptz{Time: now, Valid: true},
		QueuedAt:    pgtype.Timestamptz{Time: now, Valid: true},
	}, "etl")
	if r.DagID != "etl" || r.RunID != "r1" || r.State != domain.DagRunStateRunning || r.RunType != "manual" {
		t.Errorf("unexpected run: %+v", r)
	}
	if r.StartedAt != nil {
		t.Error("unset started_at should be nil")
	}
}

func TestMapTaskInstance(t *testing.T) {
	ti := mapTaskInstance(queries.TaskInstance{
		TaskID: "extract", State: queries.TaskStateQueued, Operator: "python",
		TryNumber: 1, MaxTries: 3,
	}, "etl", "r1")
	if ti.TaskID != "extract" || ti.State != domain.TaskStateQueued || ti.Operator != "python" || ti.MaxTries != 3 {
		t.Errorf("unexpected ti: %+v", ti)
	}
}

func TestTimeFromAny(t *testing.T) {
	now := time.Now().UTC()
	if p := timeFromAny(now); p == nil || !p.Equal(now) {
		t.Errorf("timeFromAny(time.Time) should return it, got %v", p)
	}
	// Anything that is not a time.Time (wrong type, nil) must yield nil, not panic.
	for _, v := range []any{nil, "2026-01-02", 12345, []byte("x"), (*time.Time)(nil)} {
		if p := timeFromAny(v); p != nil {
			t.Errorf("timeFromAny(%T) should be nil, got %v", v, p)
		}
	}
}

func TestParseTimestamptz(t *testing.T) {
	for _, ok := range []string{
		"2026-01-02T03:04:05Z",      // RFC3339
		"2026-01-02T03:04:05+02:00", // RFC3339 with offset
		"2026-01-02T03:04:05",       // no zone
		"2026-01-02",                // date only
	} {
		if ts := parseTimestamptz(ok); !ts.Valid {
			t.Errorf("parseTimestamptz(%q) should be valid", ok)
		}
	}
	// Empty and unparseable inputs yield NULL (invalid), never a panic.
	for _, bad := range []string{"", "nope", "2026-13-99", "01/02/2026", "now"} {
		if ts := parseTimestamptz(bad); ts.Valid {
			t.Errorf("parseTimestamptz(%q) should be invalid (NULL)", bad)
		}
	}
}

// TestMapTaskInstanceCarriesFailureReason closes the gap that left operators
// blind: the reapers and the reconciler already write a cause to
// task_instances.error_message, but the mapper dropped it on the floor, so it
// never reached the domain model, the API, or the UI.
func TestMapTaskInstanceCarriesFailureReason(t *testing.T) {
	reason := "the control plane rejected this pod's projected ServiceAccount token."
	ti := mapTaskInstance(queries.TaskInstance{
		TaskID: "extract", State: queries.TaskStateFailed, ErrorMessage: &reason,
	}, "etl", "r1")
	if ti.FailureReason != reason {
		t.Errorf("FailureReason = %q, want %q", ti.FailureReason, reason)
	}
}

// TestMapTaskInstanceNoFailureReasonIsEmpty keeps a healthy instance clean: a
// NULL error_message must map to the empty string, never a spurious reason.
func TestMapTaskInstanceNoFailureReasonIsEmpty(t *testing.T) {
	ti := mapTaskInstance(queries.TaskInstance{
		TaskID: "extract", State: queries.TaskStateSuccess,
	}, "etl", "r1")
	if ti.FailureReason != "" {
		t.Errorf("FailureReason = %q, want empty for a successful instance", ti.FailureReason)
	}
}
