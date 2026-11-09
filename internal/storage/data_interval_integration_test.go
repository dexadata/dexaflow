//go:build integration

// The data interval reaches the task (#2446). Before the fix no insert wrote
// dag_runs.data_interval_start/end, so every task saw data_interval_start and
// data_interval_end as None while the REST API showed the logical date. A run
// now stores the logical date at both ends, Airflow 3's default, and a row
// written before the fix falls back to it when the agent fetches the spec.
// Skips without DATABASE_URL.

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

func TestTaskSpecCarriesTheDataIntervalIntegration(t *testing.T) {
	pg, repo, sched, exec, ctx := openExecPG(t)
	logical := time.Date(2026, 3, 4, 6, 0, 0, 0, time.UTC)
	want := logical.Format(time.RFC3339)
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}

	specOf := func(t *testing.T, dagID string) (string, string) {
		t.Helper()
		runUUID := resolveRunUUID(t, sched, ctx, dagID)
		if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
			t.Fatalf("MaterializeTasks: %v", err)
		}
		id := auth.AgentIdentity{TenantID: defaultTenantID(), RunID: runUUID, TaskID: "load", TryNumber: 1}
		spec, err := exec.TaskSpec(ctx, id)
		if err != nil {
			t.Fatalf("TaskSpec: %v", err)
		}
		return spec.DataIntervalStart, spec.DataIntervalEnd
	}

	t.Run("manual run", func(t *testing.T) {
		dagID := fmt.Sprintf("interval_manual_%d", time.Now().UnixNano())
		registerSpec(t, repo, ctx, dagID, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "m1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: logical,
		}); err != nil {
			t.Fatalf("CreateDagRun: %v", err)
		}
		if start, end := specOf(t, dagID); start != want || end != want {
			t.Fatalf("data interval = [%q, %q], want [%q, %q]", start, end, want, want)
		}
	})

	t.Run("scheduled run", func(t *testing.T) {
		dagID := fmt.Sprintf("interval_sched_%d", time.Now().UnixNano())
		registerSpec(t, repo, ctx, dagID, tasks)
		if err := sched.CreateScheduledRun(ctx, defaultTenantID(), dagID, logical); err != nil {
			t.Fatalf("CreateScheduledRun: %v", err)
		}
		if start, end := specOf(t, dagID); start != want || end != want {
			t.Fatalf("data interval = [%q, %q], want [%q, %q]", start, end, want, want)
		}
	})

	t.Run("run stored before the fix", func(t *testing.T) {
		dagID := fmt.Sprintf("interval_legacy_%d", time.Now().UnixNano())
		registerSpec(t, repo, ctx, dagID, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "l1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: logical,
		}); err != nil {
			t.Fatalf("CreateDagRun: %v", err)
		}
		if _, err := pg.Pool.Exec(ctx,
			`UPDATE dag_runs SET data_interval_start = NULL, data_interval_end = NULL
			 WHERE run_id = 'l1' AND dag_id = (SELECT id FROM dags WHERE dag_id = $1)`, dagID); err != nil {
			t.Fatalf("clearing the interval: %v", err)
		}
		if start, end := specOf(t, dagID); start != want || end != want {
			t.Fatalf("data interval = [%q, %q], want the logical date %q at both ends", start, end, want)
		}
	})
}
