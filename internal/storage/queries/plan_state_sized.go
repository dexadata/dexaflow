package queries

import (
	"context"

	"github.com/jackc/pgx/v5/pgtype"
)

// ListTaskInstancePlanStateByRunsSized is ListTaskInstancePlanStateByRuns with
// the result slice allocated for sizeHint rows before the first one is read.
// The scheduler tick reads every task instance of every active run each
// second, and growing a slice of 100k rows by appends allocates several times
// the final size (#2325); the caller passes the previous tick's row count.
// A hint that is too small only means growth past it, as without one.
//
// Written by hand next to the generated code so it shares the query text and
// the row type sqlc checks against the schema.
func (q *Queries) ListTaskInstancePlanStateByRunsSized(ctx context.Context, dagRunIds []pgtype.UUID, sizeHint int) ([]ListTaskInstancePlanStateByRunsRow, error) {
	rows, err := q.db.Query(ctx, listTaskInstancePlanStateByRuns, dagRunIds)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]ListTaskInstancePlanStateByRunsRow, 0, max(sizeHint, 0))
	// One scan target and one destination list for every row: building them
	// per row put two heap allocations on each of the tick's rows.
	var i ListTaskInstancePlanStateByRunsRow
	dest := []any{
		&i.DagRunID,
		&i.TaskID,
		&i.State,
		&i.TryNumber,
		&i.MaxTries,
		&i.EndedAt,
		&i.RescheduleAt,
		&i.DispatchAttempts,
		&i.NextDispatchAt,
		&i.LastFailureKind,
		&i.InfraConfirmedAt,
		&i.InfraAttempts,
	}
	for rows.Next() {
		// Scan writes every column, NULLs included (a NULL leaves a
		// Timestamptz invalid and a *string nil), so nothing of the previous
		// row survives into this one.
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		items = append(items, i)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return items, nil
}
