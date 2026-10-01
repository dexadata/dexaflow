package storage

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

func uuidToString(u pgtype.UUID) string {
	if !u.Valid {
		return ""
	}
	b := u.Bytes
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func parseUUID(s string) (pgtype.UUID, error) {
	var u pgtype.UUID
	if err := u.Scan(s); err != nil {
		return pgtype.UUID{}, fmt.Errorf("parsing uuid %q: %w", s, err)
	}
	return u, nil
}

func timePtr(ts pgtype.Timestamptz) *time.Time {
	if !ts.Valid {
		return nil
	}
	t := ts.Time
	return &t
}

func timeVal(ts pgtype.Timestamptz) time.Time {
	if !ts.Valid {
		return time.Time{}
	}
	return ts.Time
}

func ptrTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *t, Valid: true}
}

func strOrEmpty(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// timeFromAny extracts a *time.Time from a scanned interface{} column (used for
// aggregate columns whose type sqlc cannot infer), returning nil when absent.
func timeFromAny(v any) *time.Time {
	if t, ok := v.(time.Time); ok {
		return &t
	}
	return nil
}

func mapDag(d queries.Dag) domain.DAG {
	return domain.DAG{
		DagID:          d.DagID,
		Description:    strOrEmpty(d.Description),
		Owner:          strOrEmpty(d.Owner),
		Tags:           d.Tags,
		Schedule:       d.Schedule,
		ScheduleTZ:     strOrEmpty(d.ScheduleTimezone),
		StartDate:      timePtr(d.StartDate),
		IsPaused:       d.IsPaused,
		IsActive:       d.IsActive,
		MaxActiveRuns:  int(d.MaxActiveRuns),
		Catchup:        d.Catchup,
		LastParsedTime: timePtr(d.UpdatedAt),
	}
}

func mapDagRun(r queries.DagRun, dagID string) domain.DagRun {
	return domain.DagRun{
		DagID:       dagID,
		RunID:       r.RunID,
		LogicalDate: timeVal(r.LogicalDate),
		State:       domain.DagRunState(r.State),
		RunType:     string(r.Trigger),
		QueuedAt:    timeVal(r.QueuedAt),
		StartedAt:   timePtr(r.StartedAt),
		EndedAt:     timePtr(r.EndedAt),
		Note:        strOrEmpty(r.Note),
		Conf:        json.RawMessage(r.Conf),
	}
}

// mapDagWithVersion is mapDag plus the current-version label the joined query
// carries. Kept separate rather than widening mapDag: the plain queries.Dag row
// has no such column, and a shared mapper would have to invent one.
func mapDagWithVersion(d queries.GetDagWithVersionRow) domain.DAG {
	out := mapDag(queries.Dag{
		DagID: d.DagID, Description: d.Description, Owner: d.Owner, Tags: d.Tags,
		Schedule: d.Schedule, ScheduleTimezone: d.ScheduleTimezone, StartDate: d.StartDate,
		IsPaused: d.IsPaused, IsActive: d.IsActive, MaxActiveRuns: d.MaxActiveRuns,
		Catchup: d.Catchup, UpdatedAt: d.UpdatedAt,
	})
	if d.CurrentVersionLabel != nil {
		out.CurrentVersion = *d.CurrentVersionLabel
	}
	return out
}

// mapDagRunWithVersion is mapDagRun plus the version label the joined query
// carries, for the same reason as mapDagWithVersion.
func mapDagRunWithVersion(r queries.GetDagRunWithVersionRow, dagID string) domain.DagRun {
	out := domain.DagRun{
		DagID:       dagID,
		RunID:       r.RunID,
		LogicalDate: timeVal(r.LogicalDate),
		State:       domain.DagRunState(r.State),
		RunType:     string(r.Trigger),
		QueuedAt:    timeVal(r.QueuedAt),
		StartedAt:   timePtr(r.StartedAt),
		EndedAt:     timePtr(r.EndedAt),
		Note:        strOrEmpty(r.Note),
		Conf:        json.RawMessage(r.Conf),
	}
	if r.DagVersionLabel != nil {
		out.Version = *r.DagVersionLabel
	}
	return out
}

func mapTaskInstance(ti queries.TaskInstance, dagID, runID string) domain.TaskInstance {
	return domain.TaskInstance{
		DagID:       dagID,
		RunID:       runID,
		TaskID:      ti.TaskID,
		MapIndex:    int(ti.MapIndex),
		TryNumber:   int(ti.TryNumber),
		MaxTries:    int(ti.MaxTries),
		State:       domain.TaskState(ti.State),
		Operator:    ti.Operator,
		ScheduledAt: timePtr(ti.ScheduledAt),
		QueuedAt:    timePtr(ti.QueuedAt),
		StartedAt:   timePtr(ti.StartedAt),
		EndedAt:     timePtr(ti.EndedAt),
		Duration:    ti.DurationSeconds,
		Hostname:    strOrEmpty(ti.Hostname),
		Note:        strOrEmpty(ti.Note),
		// error_message is where every failure path already records its cause; it
		// was written but never read back, which is what left a failed attempt with
		// no visible reason anywhere outside the control plane's own logs.
		FailureReason: strOrEmpty(ti.ErrorMessage),
	}
}

// parseTimestamptz parses an RFC3339 or date-only string into a Timestamptz,
// returning the zero (NULL) value for an empty or unparseable input.
func parseTimestamptz(s string) pgtype.Timestamptz {
	if s == "" {
		return pgtype.Timestamptz{}
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return pgtype.Timestamptz{Time: t, Valid: true}
		}
	}
	return pgtype.Timestamptz{}
}
