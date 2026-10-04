package scheduler

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Two tenants may each own a DAG with the same dag_id. Every per-DAG decision
// the scheduler makes (which tenant a scheduled run belongs to, the
// max_active_runs cap, the max_active_tasks cap) must be scoped to the
// (tenant, dag_id) pair, never to the dag_id alone (#209).

const (
	tenantA = "00000000-0000-0000-0000-00000000000a"
	tenantB = "00000000-0000-0000-0000-00000000000b"
)

// TestStepCreatesScheduledRunInTheDAGsOwnTenant: a due slot is created for the
// tenant that owns the scheduled DAG, so a DAG registered outside the default
// tenant fires, and a same-named DAG elsewhere does not receive its runs.
func TestStepCreatesScheduledRunInTheDAGsOwnTenant(t *testing.T) {
	store := newFakeStore()
	store.scheduled = []ScheduledDAG{
		{TenantID: tenantA, DagID: "etl", Schedule: "@hourly"},
		{TenantID: tenantB, DagID: "etl", Schedule: "@hourly"},
	}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	want := []string{tenantA, tenantB}
	if len(store.createdTenants) != len(want) {
		t.Fatalf("created runs in tenants %v, want %v", store.createdTenants, want)
	}
	for i := range want {
		if store.createdTenants[i] != want[i] {
			t.Errorf("run %d created in tenant %q, want %q", i, store.createdTenants[i], want[i])
		}
	}
}

// TestStepMaxActiveRunsIsPerTenant: tenant A's etl sits at max_active_runs=1;
// tenant B's etl, with no active run, still gets its due run.
func TestStepMaxActiveRunsIsPerTenant(t *testing.T) {
	last := time.Now().UTC().Add(-2 * time.Hour).Truncate(time.Hour)
	store := newFakeStore(RunState{
		RunID: "r-a", DagID: "etl", TenantID: tenantA, State: domain.DagRunStateRunning,
		Tasks:  linearTasks(),
		States: map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateNone},
	})
	store.scheduled = []ScheduledDAG{
		{TenantID: tenantA, DagID: "etl", Schedule: "@hourly", LastLogical: &last, MaxActiveRuns: 1},
		{TenantID: tenantB, DagID: "etl", Schedule: "@hourly", LastLogical: &last, MaxActiveRuns: 1},
	}
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(store.createdTenants) != 1 || store.createdTenants[0] != tenantB {
		t.Errorf("only tenant B's etl has headroom, created in %v", store.createdTenants)
	}
}

// TestActiveTaskCountsArePerTenant: the max_active_tasks occupancy snapshot
// counts each tenant's DAG separately.
func TestActiveTaskCountsArePerTenant(t *testing.T) {
	runs := []RunState{
		{DagID: "etl", TenantID: tenantA, States: map[string]domain.TaskState{"a": domain.TaskStateRunning, "b": domain.TaskStateQueued}},
		{DagID: "etl", TenantID: tenantB, States: map[string]domain.TaskState{"a": domain.TaskStateRunning}},
	}
	counts := activeTaskCounts(runs)
	if got := counts[dagKey(tenantA, "etl")]; got != 2 {
		t.Errorf("tenant A etl active tasks = %d, want 2", got)
	}
	if got := counts[dagKey(tenantB, "etl")]; got != 1 {
		t.Errorf("tenant B etl active tasks = %d, want 1", got)
	}
}

// TestStepMaxActiveTasksIsPerTenant: tenant A's etl fills its max_active_tasks
// cap of 1; tenant B's etl run is still admitted to queued on the same tick.
func TestStepMaxActiveTasksIsPerTenant(t *testing.T) {
	tasks := []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}}
	store := newFakeStore(
		RunState{
			RunID: "r-a", DagID: "etl", TenantID: tenantA, State: domain.DagRunStateRunning,
			Tasks: tasks, States: map[string]domain.TaskState{"a": domain.TaskStateRunning},
			MaxActiveTasks: 1,
		},
		RunState{
			RunID: "r-b", DagID: "etl", TenantID: tenantB, State: domain.DagRunStateRunning,
			Tasks: tasks, States: scheduledStates(tasks),
			MaxActiveTasks: 1,
		},
	)
	if err := newScheduler(store).Step(context.Background()); err != nil {
		t.Fatal(err)
	}
	// The fake has no executor, so an admitted task is failed fast as
	// undispatchable; a task held back by the cap gets no transition at all.
	for _, tr := range store.transitions {
		if tr.runID == "r-b" && tr.taskID == "a" {
			return
		}
	}
	t.Errorf("tenant B's task must be admitted despite tenant A's full cap, transitions=%v", store.transitions)
}
