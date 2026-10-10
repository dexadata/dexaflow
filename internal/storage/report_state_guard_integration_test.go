//go:build integration

// Package storage_test — the write-side guard on agent-reported task state.
//
// Two writers touch task_instances: the scheduler tick and the agent's gRPC
// report. ReportTaskResult carried neither a source-state predicate nor a
// try_number predicate, so a report that arrives late — after a reaper already
// settled the row, or after a retry already moved on to the next attempt —
// overwrote whatever was there.
//
// A fake-store unit test cannot prove this: the guard lives in the UPDATE's
// WHERE clause, so only real Postgres can show that the row did not move.
// Both sibling writes on this table (FailTaskInstanceIfActive and
// RescheduleTaskInstance) already carry the state predicate; these tests pin
// the same invariant for the third.
package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// seedRunningTask brings a fresh DAG up to a single materialized task instance
// in `running`, the state an agent reports from, and returns the run UUID.
func seedRunningTask(t *testing.T, repo *storage.Repository, sched *storage.SchedulerStore, ctx context.Context, dagID, taskID string) string {
	t.Helper()
	tasks := []domain.TaskSpec{{TaskID: taskID, Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual",
		LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, taskID, st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}
	return runUUID
}

// TestReportStateDoesNotClobberTerminalIntegration: once a reaper has settled a
// task instance as failed, a late report from the agent it gave up on must not
// resurrect it as success.
//
// The sequence is ordinary in a cluster: the agent stops heartbeating during a
// network partition, the heartbeat reaper fails the TI, the partition heals,
// and the agent — which finished its work — reports success. Without the guard
// the run reports green on work the system already abandoned, and downstream
// tasks fire on it. Nothing errors and nothing alerts, which is what makes this
// the worst failure shape an orchestrator has.
func TestReportStateDoesNotClobberTerminalIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("guard_terminal_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// The reaper gives up on the agent and settles the row.
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	// The partition heals and the abandoned agent reports success.
	id := auth.AgentIdentity{TenantID: "default", DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	err := exec.ReportState(ctx, id, domain.TaskStateSuccess, 0, "")
	if !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Fatalf("ReportState = %v, want ErrStaleReport — the caller must be able to tell a rejected late report from a successful write", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateFailed {
		t.Fatalf("TI 'load' = %q, want failed — a late report must never resurrect a row a reaper already settled", st)
	}
}

// TestReportStateIgnoresStaleTryNumberIntegration: a report from an attempt the
// scheduler has already retried past must not land on the current attempt.
//
// ResetTaskInstanceToNone bumps try_number in place rather than inserting a new
// row, so without a try_number predicate the UPDATE matches the live row no
// matter which attempt is reporting. The agent token already carries TryNumber,
// so the value needed to tell them apart is present at the call site.
func TestReportStateIgnoresStaleTryNumberIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("guard_stale_try_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// Attempt 1 fails and the scheduler retries. The real retry rail is
	// failed → up_for_retry → none: the planner parks a retriable failure in
	// up_for_retry, then ResetForRetry (guarded to up_for_retry) releases it to
	// none with try_number bumped to 2. Going straight from failed would not
	// match production and the guarded reset would (correctly) no-op.
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateUpForRetry); err != nil {
		t.Fatalf("ApplyTransition to up_for_retry: %v", err)
	}
	if applied, err := sched.ResetForRetry(ctx, runUUID, "load"); err != nil {
		t.Fatalf("ResetForRetry: %v", err)
	} else if !applied {
		t.Fatalf("ResetForRetry on up_for_retry must apply")
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}

	// The straggler from attempt 1 finally reports success.
	stale := auth.AgentIdentity{TenantID: "default", DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	if err := exec.ReportState(ctx, stale, domain.TaskStateSuccess, 0, ""); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Fatalf("ReportState (stale attempt) = %v, want ErrStaleReport", err)
	}

	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateQueued {
		t.Fatalf("TI 'load' = %q, want queued — attempt 1's report must not land on attempt 2", st)
	}
}
