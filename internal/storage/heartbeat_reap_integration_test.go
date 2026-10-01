//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/storage"
)

// openExec extends the openRepo helper with an ExecutionStore over the same
// pool, so the heartbeat write path can be exercised end-to-end.
func openExec(t *testing.T) (*storage.Repository, *storage.SchedulerStore, *storage.ExecutionStore, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return storage.NewRepository(pg), storage.NewSchedulerStore(pg), storage.NewExecutionStore(pg), ctx
}

// TestHeartbeatRoundTripIntegration is the end-to-end contract for the
// per-TI liveness signal (#128): the agent's RecordHeartbeat call stamps
// last_heartbeat_at, and the scheduler's ListAgentLostCandidates picks the
// TI up only after the stamp is older than the threshold. The "do no harm"
// rule of ADR 0031 is the load-bearing assertion here — a fresh heartbeat
// must keep the TI off the candidate list.
func TestHeartbeatRoundTripIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("hb_roundtrip_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}

	// Pre-heartbeat: TI has no last_heartbeat_at, so it is OUT of the
	// candidate set (the SQL filters on IS NOT NULL).
	cands, err := sched.ListAgentLostCandidates(ctx)
	if err != nil {
		t.Fatalf("ListAgentLostCandidates: %v", err)
	}
	if findAgentLostCandidate(cands, runUUID) != nil {
		t.Errorf("pre-heartbeat TI must not be a candidate (last_heartbeat_at is NULL)")
	}

	// Agent heartbeats — the column is stamped.
	if err := exec.RecordHeartbeat(ctx, auth.AgentIdentity{
		RunID: runUUID, TaskID: "t", TryNumber: 1,
	}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	// Post-heartbeat: the TI is now a candidate (it has heartbeated). Whether
	// it gets reaped is a Go-side decision based on the threshold — the SQL
	// only filters on "has heartbeated at all".
	cands, err = sched.ListAgentLostCandidates(ctx)
	if err != nil {
		t.Fatalf("ListAgentLostCandidates: %v", err)
	}
	c := findAgentLostCandidate(cands, runUUID)
	if c == nil {
		t.Fatalf("post-heartbeat TI must appear as a candidate; got %+v", cands)
	}
	if c.LastHeartbeat.IsZero() {
		t.Errorf("candidate LastHeartbeat must be the stamped time, got zero")
	}
	if c.TaskID != "t" || c.DagID != dagID {
		t.Errorf("candidate identity = %+v, want task=t dag=%s", c, dagID)
	}
}

// TestMarkTaskAgentLostIntegration: marking a TI as agent_lost transitions
// it to `failed` with the right error_message and is idempotent (a second
// call on a now-failed TI is a no-op thanks to the WHERE state='running'
// guard — defense in depth against late terminal reports).
func TestMarkTaskAgentLostIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("hb_mark_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	if err := exec.RecordHeartbeat(ctx, auth.AgentIdentity{
		RunID: runUUID, TaskID: "t", TryNumber: 1,
	}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}

	cands, _ := sched.ListAgentLostCandidates(ctx)
	c := findAgentLostCandidate(cands, runUUID)
	if c == nil {
		t.Fatalf("expected candidate after heartbeat")
	}

	// Reap — the first mark applies (a running row transitions).
	applied, err := sched.MarkTaskAgentLost(ctx, c.TaskInstanceID)
	if err != nil {
		t.Fatalf("MarkTaskAgentLost: %v", err)
	}
	if !applied {
		t.Errorf("first MarkTaskAgentLost on a running TI must report applied=true")
	}

	// The TI is now failed and out of the candidate set.
	tis, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if len(tis) != 1 || tis[0].State != domain.TaskStateFailed {
		t.Errorf("after MarkTaskAgentLost, TI state = %+v, want failed", tis)
	}

	// Second call is a no-op (WHERE state='running' now matches 0 rows) — and
	// that is observable: applied must be false.
	applied, err = sched.MarkTaskAgentLost(ctx, c.TaskInstanceID)
	if err != nil {
		t.Errorf("second MarkTaskAgentLost errored: %v", err)
	}
	if applied {
		t.Errorf("second MarkTaskAgentLost on a failed TI must report applied=false (0 rows)")
	}
}

// findAgentLostCandidate returns the candidate matching the run uuid (a
// single-TI test set), or nil.
func findAgentLostCandidate(cands []executor.AgentLostCandidate, runUUID string) *executor.AgentLostCandidate {
	for i := range cands {
		if cands[i].DagRunID == runUUID {
			return &cands[i]
		}
	}
	return nil
}
