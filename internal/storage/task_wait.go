package storage

import (
	"context"

	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// WaitingAge is the age of the oldest task instance of a tenant that waits in
// State (scheduled or queued).
type WaitingAge struct {
	Tenant     string
	State      string
	AgeSeconds float64
}

// TaskWaitStore reads the waiting task ages behind
// dexaflow_task_oldest_waiting_seconds (#2300).
type TaskWaitStore struct {
	q *queries.Queries
}

// NewTaskWaitStore builds a TaskWaitStore over pg.
func NewTaskWaitStore(pg *Postgres) *TaskWaitStore {
	return &TaskWaitStore{q: pg.Queries}
}

// OldestWaiting returns one age per tenant and waiting state that has at least
// one task waiting.
func (s *TaskWaitStore) OldestWaiting(ctx context.Context) ([]WaitingAge, error) {
	rows, err := s.q.OldestWaitingTaskAges(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]WaitingAge, 0, len(rows))
	for _, r := range rows {
		out = append(out, WaitingAge{Tenant: r.Tenant, State: r.State, AgeSeconds: r.AgeSeconds})
	}
	return out, nil
}
