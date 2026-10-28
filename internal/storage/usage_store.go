package storage

import (
	"context"

	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// UsageStore reads and writes the task attempt usage events of ADR 0096. The
// events themselves are written by a trigger on task_instances (migration
// 055); this store only drives the switch that trigger reads.
type UsageStore struct {
	q *queries.Queries
}

// NewUsageStore builds a UsageStore over the given Postgres connection.
func NewUsageStore(pg *Postgres) *UsageStore {
	return &UsageStore{q: pg.Queries}
}

// SetEventsEnabled turns the recording of usage events on or off for the
// whole database. It reports whether the stored value changed.
func (s *UsageStore) SetEventsEnabled(ctx context.Context, enabled bool) (bool, error) {
	n, err := s.q.SetUsageEventsEnabled(ctx, enabled)
	return n > 0, err
}
