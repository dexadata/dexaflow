//go:build integration

// The run's start, due time and state reach the task (#2474). Before the fix the
// runtime's dag_run had no start_date or run_after, so provider operators that
// read them (BigQueryInsertJobOperator reads start_date, #2473) failed before
// doing any work. Skips without DATABASE_URL.

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

func TestTaskSpecCarriesTheRunStartRunAfterAndStateIntegration(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	logical := time.Date(2026, 3, 4, 6, 0, 0, 0, time.UTC)
	started := time.Date(2026, 3, 4, 6, 0, 7, 0, time.UTC)
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	dagID := fmt.Sprintf("run_timing_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "m1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: logical,
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	id := auth.AgentIdentity{TenantID: defaultTenantID(), RunID: runUUID, TaskID: "load", TryNumber: 1}

	t.Run("not started yet", func(t *testing.T) {
		if _, err := pg.Pool.Exec(ctx,
			`UPDATE dag_runs SET started_at = NULL WHERE id = $1`, runUUID); err != nil {
			t.Fatalf("clearing started_at: %v", err)
		}
		spec, err := exec.TaskSpec(ctx, id)
		if err != nil {
			t.Fatalf("TaskSpec: %v", err)
		}
		if spec.RunStartDate != "" {
			t.Errorf("RunStartDate = %q, want empty for a run with no start", spec.RunStartDate)
		}
	})

	t.Run("started", func(t *testing.T) {
		if _, err := pg.Pool.Exec(ctx,
			`UPDATE dag_runs SET started_at = $2 WHERE id = $1`, runUUID, started); err != nil {
			t.Fatalf("setting started_at: %v", err)
		}
		spec, err := exec.TaskSpec(ctx, id)
		if err != nil {
			t.Fatalf("TaskSpec: %v", err)
		}
		if want := started.Format(time.RFC3339); spec.RunStartDate != want {
			t.Errorf("RunStartDate = %q, want %q", spec.RunStartDate, want)
		}
		if want := logical.Format(time.RFC3339); spec.RunAfter != want {
			t.Errorf("RunAfter = %q, want the data interval end %q", spec.RunAfter, want)
		}
		if spec.RunState != "running" {
			t.Errorf("RunState = %q, want running", spec.RunState)
		}
	})
}
