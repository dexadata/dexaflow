package domain

import "time"

// UsageCursor is a position in the order the service API serves usage events
// (ADR 0096 point 4): the writing transaction's id, then the event id. The
// zero value is before every event.
type UsageCursor struct {
	Xid uint64
	ID  int64
}

// Before reports whether c sorts before o.
func (c UsageCursor) Before(o UsageCursor) bool {
	return c.Xid < o.Xid || (c.Xid == o.Xid && c.ID < o.ID)
}

// UsageEvent is one finished task attempt, or a later revision of it, as the
// service API serves it. An attempt is (TaskInstanceID, TryNumber,
// AttemptEpoch); with Revision it is the key a consumer dedupes on.
type UsageEvent struct {
	Cursor UsageCursor
	// Tenant, DagID and RunID are resolved when the event is served; they are
	// empty once the tenant or the run is deleted.
	Tenant         string
	DagID, RunID   string
	TaskInstanceID string
	TaskID         string
	MapIndex       int
	TryNumber      int
	AttemptEpoch   int
	Revision       int
	State          TaskState
	PreviousState  TaskState
	PoolSlots      int
	// StartedAt and EndedAt are nil for a task that never started (skipped,
	// upstream failed). EndedAt is the task's end time, or the time of the
	// change that ended the attempt when the task has none.
	StartedAt, EndedAt *time.Time
	LastFailureKind    string
	RecordedAt         time.Time
}

// UsagePage is one read of the usage events after a cursor.
type UsagePage struct {
	// Enabled is false when recording is off; nothing else is set then.
	Enabled bool
	// Gone reports that the retention janitor deleted events after the
	// cursor; Oldest is then the first cursor whose following events are
	// complete, and Events is empty.
	Gone   bool
	Oldest UsageCursor
	Events []UsageEvent
}
