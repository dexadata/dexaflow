package storage

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// TestTaskInstanceRowsCarryPoolSlots pins the batched COPY rows MaterializeTasks
// writes: each task instance carries its task's EffectivePoolSlots (#1499), so
// PoolSlotUsage sums the same weights the admission gate charges (ADR 0066). An
// unset or non-positive size is one slot, never zero.
func TestTaskInstanceRowsCarryPoolSlots(t *testing.T) {
	tenant := pgtype.UUID{Bytes: [16]byte{1}, Valid: true}
	run := pgtype.UUID{Bytes: [16]byte{2}, Valid: true}
	two := 2
	tasks := []domain.TaskSpec{
		{TaskID: "big", Type: domain.TaskTypePython, Pool: "gpu", PoolSlots: 4, Retries: &two},
		{TaskID: "unset", Type: domain.TaskTypeBash},
		{TaskID: "zero", Type: domain.TaskTypePython, PoolSlots: 0},
	}
	rows := taskInstanceRows(tenant, run, tasks)
	if len(rows) != len(tasks) {
		t.Fatalf("rows = %d, want %d", len(rows), len(tasks))
	}
	gpu := "gpu"
	want := []queries.CreateTaskInstancesParams{
		{TenantID: tenant, DagRunID: run, TaskID: "big", Operator: "python", MaxTries: 3,
			State: queries.TaskStateNone, Pool: &gpu, TryNumber: 1, PoolSlots: 4},
		{TenantID: tenant, DagRunID: run, TaskID: "unset", Operator: "bash", MaxTries: 1,
			State: queries.TaskStateNone, TryNumber: 1, PoolSlots: 1},
		{TenantID: tenant, DagRunID: run, TaskID: "zero", Operator: "python", MaxTries: 1,
			State: queries.TaskStateNone, TryNumber: 1, PoolSlots: 1},
	}
	for i := range want {
		got := rows[i]
		if (got.Pool == nil) != (want[i].Pool == nil) || (got.Pool != nil && *got.Pool != *want[i].Pool) {
			t.Errorf("row %d pool = %v, want %v", i, got.Pool, want[i].Pool)
		}
		got.Pool, want[i].Pool = nil, nil
		if got != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got, want[i])
		}
	}
}
