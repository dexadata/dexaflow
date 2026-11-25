//go:build integration

package storage_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// The branch decision's persistence (ADR 0064 D1, D6): it lands only with a
// success that passes the attempt guard, the planner reads it, a clear
// resets it and archives it, and the reconciler's recovered success sets it.

func branchDecisionOf(t *testing.T, sched *storage.SchedulerStore, runUUID, taskID string) (raw []byte, history [][]byte) {
	t.Helper()
	ctx := t.Context()
	pool := testPool(t, ctx)
	if err := pool.QueryRow(ctx, `SELECT branch_decision FROM task_instances WHERE dag_run_id = $1 AND task_id = $2`,
		runUUID, taskID).Scan(&raw); err != nil {
		t.Fatalf("reading branch_decision: %v", err)
	}
	rows, err := pool.Query(ctx, `SELECT h.branch_decision FROM task_instance_history h
		JOIN task_instances ti ON ti.id = h.task_instance_id
		WHERE ti.dag_run_id = $1 AND ti.task_id = $2 ORDER BY h.try_number`, runUUID, taskID)
	if err != nil {
		t.Fatalf("reading history: %v", err)
	}
	defer rows.Close()
	for rows.Next() {
		var h []byte
		if err := rows.Scan(&h); err != nil {
			t.Fatal(err)
		}
		history = append(history, h)
	}
	return raw, history
}

func seedBranchTask(t *testing.T, dagID string) (*storage.Repository, *storage.SchedulerStore, *storage.ExecutionStore, string, auth.AgentIdentity) {
	t.Helper()
	repo, sched, exec, ctx := openExec(t)
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "pick")
	return repo, sched, exec, runUUID, auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "pick", TryNumber: 1}
}

func TestBranchSuccessPersistsItsDecisionIntegration(t *testing.T) {
	dagID := fmt.Sprintf("branch_persist_%d", time.Now().UnixNano())
	_, sched, exec, runUUID, id := seedBranchTask(t, dagID)
	want := domain.BranchDecision{FollowTaskIDs: []string{"a"}}
	if err := exec.ReportBranchSuccess(t.Context(), id, want); err != nil {
		t.Fatal(err)
	}
	if st := taskInstanceState(t, sched, t.Context(), runUUID, "pick"); st != domain.TaskStateSuccess {
		t.Fatalf("state = %q, want success", st)
	}
	raw, _ := branchDecisionOf(t, sched, runUUID, "pick")
	var got domain.BranchDecision
	if err := json.Unmarshal(raw, &got); err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("branch_decision = %s (%v), want %+v", raw, err, want)
	}

	// The tick reads it back for the planner.
	runs, err := sched.ActiveRuns(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range runs {
		if r.RunID == runUUID {
			if d := r.BranchDecision["pick"]; d == nil || !reflect.DeepEqual(*d, want) {
				t.Fatalf("RunState.BranchDecision = %+v, want %+v", r.BranchDecision, want)
			}
			return
		}
	}
	t.Fatal("the run is not active")
}

// A report from a superseded attempt can never set a decision.
func TestStaleBranchReportSetsNoDecisionIntegration(t *testing.T) {
	dagID := fmt.Sprintf("branch_stale_%d", time.Now().UnixNano())
	_, sched, exec, runUUID, id := seedBranchTask(t, dagID)
	stale := id
	stale.TryNumber = 2
	err := exec.ReportBranchSuccess(t.Context(), stale, domain.BranchDecision{FollowTaskIDs: []string{"a"}})
	if !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Fatalf("ReportBranchSuccess = %v, want ErrStaleReport", err)
	}
	if raw, _ := branchDecisionOf(t, sched, runUUID, "pick"); raw != nil {
		t.Fatalf("branch_decision = %s, want NULL", raw)
	}
}

// A plain success leaves the column NULL.
func TestPlainSuccessSetsNoDecisionIntegration(t *testing.T) {
	dagID := fmt.Sprintf("branch_plain_%d", time.Now().UnixNano())
	_, sched, exec, runUUID, id := seedBranchTask(t, dagID)
	if err := exec.ReportState(t.Context(), id, domain.TaskStateSuccess, 0, ""); err != nil {
		t.Fatal(err)
	}
	if raw, _ := branchDecisionOf(t, sched, runUUID, "pick"); raw != nil {
		t.Fatalf("branch_decision = %s, want NULL", raw)
	}
}

// A clear resets the decision and archives it with the attempt.
func TestClearResetsAndArchivesTheDecisionIntegration(t *testing.T) {
	dagID := fmt.Sprintf("branch_clear_%d", time.Now().UnixNano())
	repo, sched, exec, runUUID, id := seedBranchTask(t, dagID)
	if err := exec.ReportBranchSuccess(t.Context(), id, domain.BranchDecision{FollowTaskIDs: []string{"a"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.ClearTaskInstances(t.Context(), "default", dagID, "r1", []string{"pick"}, false, domain.ClearOptions{}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	raw, history := branchDecisionOf(t, sched, runUUID, "pick")
	if raw != nil {
		t.Errorf("branch_decision after the clear = %s, want NULL", raw)
	}
	if len(history) != 1 || !json.Valid(history[0]) {
		t.Fatalf("history = %q, want the archived decision", history)
	}
}

// The reconciler's recovered success sets the decision, guarded like any
// recovered success.
func TestRecoveredBranchSuccessSetsItsDecisionIntegration(t *testing.T) {
	dagID := fmt.Sprintf("branch_recover_%d", time.Now().UnixNano())
	_, sched, exec, runUUID, _ := seedBranchTask(t, dagID)
	var tiID string
	if err := testPool(t, t.Context()).QueryRow(t.Context(),
		`SELECT id::text FROM task_instances WHERE dag_run_id = $1 AND task_id = 'pick'`, runUUID).Scan(&tiID); err != nil {
		t.Fatal(err)
	}
	if ok, err := exec.SucceedBranchTaskIfActive(t.Context(), tiID, 2, 0, domain.BranchDecision{}); err != nil || ok {
		t.Fatalf("a superseded try settled: ok=%v err=%v", ok, err)
	}
	if ok, err := exec.SucceedBranchTaskIfActive(t.Context(), tiID, 1, 0, domain.BranchDecision{ShortCircuitFalsy: true}); err != nil || !ok {
		t.Fatalf("SucceedBranchTaskIfActive: ok=%v err=%v", ok, err)
	}
	raw, _ := branchDecisionOf(t, sched, runUUID, "pick")
	if string(raw) != `{"short_circuit_falsy": true}` {
		t.Fatalf("branch_decision = %s", raw)
	}
}

// TaskSpec carries the branch fields for a branch task only.
func TestTaskSpecCarriesTheBranchFieldsIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("branch_spec_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "pick", Type: domain.TaskTypePython, Branch: &domain.BranchSpec{Kind: domain.BranchKindBranch}},
		{TaskID: "a", Type: domain.TaskTypePython, DependsOn: []string{"pick"}},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"pick"}},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "pick", TryNumber: 1}
	spec, err := exec.TaskSpec(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if spec.Branch == nil || !reflect.DeepEqual(spec.DagTaskIDs, []string{"pick", "a", "b"}) || !reflect.DeepEqual(spec.DownstreamTaskIDs, []string{"a", "b"}) {
		t.Fatalf("spec = %+v / %v / %v", spec.Branch, spec.DagTaskIDs, spec.DownstreamTaskIDs)
	}
	id.TaskID = "a"
	if spec, err = exec.TaskSpec(ctx, id); err != nil || spec.Branch != nil || spec.DagTaskIDs != nil {
		t.Fatalf("plain task spec = %+v, %v", spec, err)
	}
}
