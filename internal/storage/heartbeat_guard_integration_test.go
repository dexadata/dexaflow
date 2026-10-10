//go:build integration

// Package storage_test — the write-side guard on the agent's heartbeat.
//
// RecordTaskHeartbeat stamps last_heartbeat_at only while the (dag_run_id,
// task_id, try_number) tuple is still active (queued/running). #474 makes the
// zero-rows outcome observable: when the heartbeating attempt no longer matches
// the live row — a reaper settled it terminal, or a retry moved past it —
// RecordHeartbeat returns ErrStaleReport so the agent RPC can answer
// should_terminate and stop a reaped-but-alive pod. A live, matching attempt
// stamps a row and gets no error, so a live execution is never told to stop.
//
// A fake-store unit test cannot prove this: the predicate lives in the UPDATE's
// WHERE clause, so only real Postgres shows that the row did not move.
package storage_test

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/agentrpc"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRecordHeartbeatLiveAttemptStampsNoError: the ordinary path — a running,
// matching attempt heartbeats, stamps its row, and gets no error (the invariant
// that a live execution is never told to terminate).
func TestRecordHeartbeatLiveAttemptStampsNoError(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("hb_live_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	id := auth.AgentIdentity{TenantID: "default", DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	if err := exec.RecordHeartbeat(ctx, id); err != nil {
		t.Fatalf("RecordHeartbeat on a live attempt = %v, want nil (must never signal terminate)", err)
	}
}

// TestRecordHeartbeatTerminalRowSignalsStale: once a reaper settles the row
// terminal, a heartbeat from the abandoned-but-alive agent no longer applies —
// RecordHeartbeat returns ErrStaleReport so the RPC answers should_terminate.
func TestRecordHeartbeatTerminalRowSignalsStale(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("hb_terminal_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	id := auth.AgentIdentity{TenantID: "default", DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	if err := exec.RecordHeartbeat(ctx, id); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Fatalf("RecordHeartbeat after a reap = %v, want ErrStaleReport (the terminate signal source)", err)
	}
}

// TestRecordHeartbeatStaleTryNumberSignalsStale: a heartbeat from an attempt the
// scheduler already retried past must not stamp the current attempt; it reports
// ErrStaleReport so only the superseded pod is told to terminate.
func TestRecordHeartbeatStaleTryNumberSignalsStale(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("hb_stale_try_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	// Attempt 1 fails and is retried along the real rail failed → up_for_retry →
	// none: try_number becomes 2, row back to none → queued → running for
	// attempt 2. ResetForRetry is guarded to up_for_retry, so the intermediate
	// transition is required (a direct failed → reset would no-op).
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
	for _, st := range []domain.TaskState{domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "load", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}

	// Attempt 1's straggler heartbeats — it is behind, so it is told to stop.
	stale := auth.AgentIdentity{TenantID: "default", DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	if err := exec.RecordHeartbeat(ctx, stale); !errors.Is(err, agentrpc.ErrStaleReport) {
		t.Fatalf("RecordHeartbeat (stale attempt) = %v, want ErrStaleReport", err)
	}

	// Attempt 2 (the live one) heartbeats and applies cleanly — never terminated.
	live := auth.AgentIdentity{TenantID: "default", DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 2}
	if err := exec.RecordHeartbeat(ctx, live); err != nil {
		t.Fatalf("RecordHeartbeat (live attempt 2) = %v, want nil", err)
	}
}
