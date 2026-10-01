package storage

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// DagStats returns the home dashboard's DAG counters: the total active DAG count
// plus how many DAGs have a latest run in the failed/running/queued state.
func (r *Repository) DagStats(ctx context.Context, tenant string) (domain.DagStats, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.DagStats{}, err
	}
	active, err := r.q.CountDags(ctx, tid)
	if err != nil {
		return domain.DagStats{}, fmt.Errorf("counting active dags: %w", err)
	}
	rows, err := r.q.CountDagsByLatestRunState(ctx, tid)
	if err != nil {
		return domain.DagStats{}, fmt.Errorf("counting dags by latest run state: %w", err)
	}
	stats := domain.DagStats{Active: int(active)}
	for _, row := range rows {
		switch string(row.State) {
		case "failed":
			stats.Failed = int(row.N)
		case "running":
			stats.Running = int(row.N)
		case "queued":
			stats.Queued = int(row.N)
		}
	}
	return stats, nil
}

// HistoricalMetrics returns run- and task-instance state counts for runs whose
// logical date falls within [since, until], keyed by Leoflow state name.
func (r *Repository) HistoricalMetrics(ctx context.Context, tenant string, since, until time.Time) (domain.HistoricalMetrics, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return domain.HistoricalMetrics{}, err
	}
	from := pgtype.Timestamptz{Time: since, Valid: true}
	to := pgtype.Timestamptz{Time: until, Valid: true}
	runRows, err := r.q.CountDagRunStatesInWindow(ctx, queries.CountDagRunStatesInWindowParams{
		TenantID: tid, LogicalDate: from, LogicalDate_2: to,
	})
	if err != nil {
		return domain.HistoricalMetrics{}, fmt.Errorf("counting run states: %w", err)
	}
	tiRows, err := r.q.CountTaskInstanceStatesInWindow(ctx, queries.CountTaskInstanceStatesInWindowParams{
		TenantID: tid, LogicalDate: from, LogicalDate_2: to,
	})
	if err != nil {
		return domain.HistoricalMetrics{}, fmt.Errorf("counting task instance states: %w", err)
	}
	m := domain.HistoricalMetrics{
		RunStates: make(map[string]int, len(runRows)),
		TIStates:  make(map[string]int, len(tiRows)),
	}
	for _, row := range runRows {
		m.RunStates[string(row.State)] = int(row.N)
	}
	for _, row := range tiRows {
		m.TIStates[string(row.State)] = int(row.N)
	}
	return m, nil
}
