//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// TestSettledRunsIntegration: SettledRuns reports exactly the asked runs that
// are settled: run in success or failed and no task instance outside success,
// failed, skipped, upstream_failed and removed (the same predicate as
// retention). A
// run marked failed while a task still runs, an active run, an unknown id, a
// malformed id and a run asked under another tenant are left out, so the
// reconciler never collects pods whose outcome may still be unrecorded.
func TestSettledRunsIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	refs := map[string]executor.RunRef{}
	for i, name := range []string{"success", "failed", "running", "queued", "failed_with_running_ti", "failed_with_removed_ti"} {
		dagID := fmt.Sprintf("settled_runs_%d_%d", time.Now().UnixNano(), i)
		registerSpec(t, repo, ctx, dagID, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		var ref executor.RunRef
		runs, err := sched.ActiveRuns(ctx)
		if err != nil {
			t.Fatal(err)
		}
		for _, r := range runs {
			if r.DagID == dagID {
				ref = executor.RunRef{Tenant: r.TenantID, Run: r.RunID}
			}
		}
		if ref.Run == "" {
			t.Fatalf("run of %s not found", dagID)
		}
		refs[name] = ref
		state := name
		if ti, ok := map[string]domain.TaskState{
			"failed_with_running_ti": domain.TaskStateRunning,
			// removed is settled: a clear marks it on a finished run's task
			// instance whose task the new version dropped (ADR 0086).
			"failed_with_removed_ti": domain.TaskStateRemoved,
		}[name]; ok {
			if err := sched.MaterializeTasks(ctx, ref.Run, tasks); err != nil {
				t.Fatal(err)
			}
			if err := sched.ApplyTransition(ctx, ref.Run, "t", ti); err != nil {
				t.Fatal(err)
			}
			state = "failed"
		}
		if err := repo.SetDagRunState(ctx, "default", dagID, "r", state); err != nil {
			t.Fatalf("set state: %v", err)
		}
	}
	otherTenant := executor.RunRef{Tenant: "00000000-0000-0000-0000-000000000001", Run: refs["success"].Run}
	got, err := exec.SettledRuns(ctx, []executor.RunRef{
		refs["success"], refs["failed"], refs["running"], refs["queued"], refs["failed_with_running_ti"], refs["failed_with_removed_ti"],
		{Tenant: refs["success"].Tenant, Run: "00000000-0000-0000-0000-000000000000"},
		{Tenant: refs["success"].Tenant, Run: "not-a-uuid"},
		otherTenant,
	})
	if err != nil {
		t.Fatalf("SettledRuns: %v", err)
	}
	if len(got) != 3 || !got[refs["success"]] || !got[refs["failed"]] || !got[refs["failed_with_removed_ti"]] {
		t.Fatalf("SettledRuns = %v, want only the success and failed runs with settled tasks", got)
	}
}
