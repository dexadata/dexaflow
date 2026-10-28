package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// UsageStore reads and writes the task attempt usage events of ADR 0096. The
// events themselves are written by a trigger on task_instances (migration
// 055); this store drives the switch that trigger reads and serves them.
type UsageStore struct {
	q    *queries.Queries
	pool *pgxpool.Pool
}

// NewUsageStore builds a UsageStore over the given Postgres connection.
func NewUsageStore(pg *Postgres) *UsageStore {
	return &UsageStore{q: pg.Queries, pool: pg.Pool}
}

// SetEventsEnabled turns the recording of usage events on or off for the
// whole database. It reports whether the stored value changed.
func (s *UsageStore) SetEventsEnabled(ctx context.Context, enabled bool) (bool, error) {
	n, err := s.q.SetUsageEventsEnabled(ctx, enabled)
	return n > 0, err
}

// ReadEvents returns up to limit usage events after the cursor, in serving
// order. The switch, the pruned mark and the events are read in one
// repeatable-read snapshot, and the janitor moves the mark in the statement
// that deletes, so a page that is not Gone never skips a deleted event. The
// zero cursor reads from the oldest event kept and is never Gone.
func (s *UsageStore) ReadEvents(ctx context.Context, after domain.UsageCursor, limit int) (domain.UsagePage, error) {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return domain.UsagePage{}, fmt.Errorf("beginning usage read: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // read only; nothing to keep
	q := s.q.WithTx(tx)
	state, err := q.GetUsageEventsState(ctx)
	if err != nil {
		return domain.UsagePage{}, fmt.Errorf("reading the usage events state: %w", err)
	}
	if !state.Enabled {
		return domain.UsagePage{}, nil
	}
	page := domain.UsagePage{Enabled: true}
	if state.PrunedXid.Valid && state.PrunedID != nil && after != (domain.UsageCursor{}) {
		mark := domain.UsageCursor{Xid: state.PrunedXid.Uint64, ID: *state.PrunedID}
		if after.Before(mark) {
			page.Gone, page.Oldest = true, mark
			return page, nil
		}
	}
	rows, err := q.ListUsageEvents(ctx, queries.ListUsageEventsParams{
		AfterXid: pgtype.Uint64{Uint64: after.Xid, Valid: true}, AfterID: after.ID, RowLimit: int32(limit), //nolint:gosec // bounded by the route
	})
	if err != nil {
		return domain.UsagePage{}, fmt.Errorf("listing usage events: %w", err)
	}
	page.Events = make([]domain.UsageEvent, len(rows))
	for i, r := range rows {
		page.Events[i] = usageEventFromRow(r)
	}
	return page, nil
}

func usageEventFromRow(r queries.ListUsageEventsRow) domain.UsageEvent {
	e := domain.UsageEvent{
		Cursor: domain.UsageCursor{Xid: r.Xid.Uint64, ID: r.ID},
		Tenant: r.Tenant, DagID: r.DagID, RunID: r.RunID,
		TaskInstanceID: uuidToString(r.TaskInstanceID), TaskID: r.TaskID,
		MapIndex: int(r.MapIndex), TryNumber: int(r.TryNumber), AttemptEpoch: int(r.AttemptEpoch),
		Revision: int(r.Revision), PoolSlots: int(r.PoolSlots),
		State: domain.TaskState(r.State), PreviousState: domain.TaskState(r.PreviousState),
		RecordedAt: r.RecordedAt.Time,
	}
	if r.StartedAt.Valid {
		t := r.StartedAt.Time
		e.StartedAt = &t
	}
	if r.EndedAt.Valid {
		t := r.EndedAt.Time
		e.EndedAt = &t
	}
	if r.LastFailureKind != nil {
		e.LastFailureKind = *r.LastFailureKind
	}
	return e
}

// EventsLag is the age of the oldest usage event the route cannot serve yet
// because a transaction older than it is still open, or 0 when there is none.
func (s *UsageStore) EventsLag(ctx context.Context) (time.Duration, error) {
	secs, err := s.q.UsageEventsLagSeconds(ctx)
	if err != nil {
		return 0, fmt.Errorf("reading the usage events lag: %w", err)
	}
	return time.Duration(secs * float64(time.Second)), nil
}
