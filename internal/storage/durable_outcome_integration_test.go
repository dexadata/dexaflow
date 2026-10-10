//go:build integration

// Package storage_test — the reconciler's attempt-guarded outcome settles (ADR 0052).
//
// The pod reconciler recovers a task's outcome from its durable record (or pod
// phase) and settles it: SucceedTaskInstanceIfActive, FailTaskInstanceIfActive,
// and RescheduleTaskInstanceByIDIfActive. Each is guarded by BOTH the row id and
// try_number, because try_number bumps IN PLACE on retry (same row id) — so a
// stale reconciler acting on a previous attempt's lingering pod must not match the
// live attempt. Settling SUCCESS through an unguarded path would be the worst
// shape: a live retry marked succeeded, firing downstream on incomplete work.
//
// A fake-store unit test cannot prove this — the guard lives in the UPDATE's WHERE
// clause, so only real Postgres shows the row did not move.
package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// outcomeTIID reads a task instance's UUID directly, for a TI in any state (the
// shared taskInstanceID helper only lists queued candidates).
func outcomeTIID(t *testing.T, ctx context.Context, runUUID, taskID string) string {
	t.Helper()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer pg.Close()
	var id string
	if err := pg.Pool.QueryRow(ctx,
		"SELECT id::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id=$2", runUUID, taskID).Scan(&id); err != nil {
		t.Fatalf("select ti id for %s/%s: %v", runUUID, taskID, err)
	}
	return id
}

// TestReconcilerRecoversLostSuccessIntegration is the headline of ADR 0052: the
// reconciler settles a running TI succeeded from its recovered outcome record,
// recovering a success whose report was lost.
func TestReconcilerRecoversLostSuccessIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("outcome_success_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")
	tiID := outcomeTIID(t, ctx, runUUID, "load")

	if err := exec.SucceedTask(ctx, tiID, 1, 0); err != nil {
		t.Fatalf("SucceedTask: %v", err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateSuccess {
		t.Fatalf("TI 'load' = %q, want success — the reconciler must recover the lost success", st)
	}
}

// TestReconcilerSucceedIgnoresStaleTryNumberIntegration is the crux: a success
// settle for a PREVIOUS attempt must not land on the live retry. Without the
// try_number guard it would mark a running attempt succeeded and fire downstream
// on work that never ran — strictly worse than the bug being fixed.
func TestReconcilerSucceedIgnoresStaleTryNumberIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("outcome_stale_try_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")
	tiID := outcomeTIID(t, ctx, runUUID, "load")

	// Attempt 1 fails and the scheduler retries to attempt 2 (failed → up_for_retry
	// → none → queued), bumping try_number in place on the same row.
	for _, st := range []domain.TaskState{domain.TaskStateFailed, domain.TaskStateUpForRetry} {
		if err := sched.ApplyTransition(ctx, runUUID, "load", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}
	if applied, err := sched.ResetForRetry(ctx, runUUID, "load"); err != nil || !applied {
		t.Fatalf("ResetForRetry applied=%v err=%v", applied, err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}

	// The reconciler acts on attempt 1's lingering pod — a stale success settle.
	if err := exec.SucceedTask(ctx, tiID, 1, 0); err != nil {
		t.Fatalf("SucceedTask (stale): %v", err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateQueued {
		t.Fatalf("TI 'load' = %q, want queued — a stale attempt's success must NOT clobber the live retry", st)
	}
}

// TestReconcilerSucceedDoesNotClobberTerminalIntegration: the active-state guard
// stops a recovered success from resurrecting a row a reaper already settled.
func TestReconcilerSucceedDoesNotClobberTerminalIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("outcome_terminal_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")
	tiID := outcomeTIID(t, ctx, runUUID, "load")

	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}
	if err := exec.SucceedTask(ctx, tiID, 1, 0); err != nil {
		t.Fatalf("SucceedTask: %v", err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateFailed {
		t.Fatalf("TI 'load' = %q, want failed — a recovered success must not resurrect a terminal row", st)
	}
}

// TestReconcilerFailIgnoresStaleTryNumberIntegration: the same attempt guard on
// the failure settle — a stale reconciler must not fail the live retry.
func TestReconcilerFailIgnoresStaleTryNumberIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("outcome_fail_stale_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")
	tiID := outcomeTIID(t, ctx, runUUID, "load")

	for _, st := range []domain.TaskState{domain.TaskStateFailed, domain.TaskStateUpForRetry} {
		if err := sched.ApplyTransition(ctx, runUUID, "load", st); err != nil {
			t.Fatalf("ApplyTransition to %s: %v", st, err)
		}
	}
	if applied, err := sched.ResetForRetry(ctx, runUUID, "load"); err != nil || !applied {
		t.Fatalf("ResetForRetry applied=%v err=%v", applied, err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}

	if err := exec.FailTask(ctx, tiID, 1, 0, "stale pod failure"); err != nil {
		t.Fatalf("FailTask (stale): %v", err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "load"); st != domain.TaskStateQueued {
		t.Fatalf("TI 'load' = %q, want queued — a stale attempt's failure must NOT clobber the live retry", st)
	}
}

// TestReconcilerRescheduleByIDIntegration: a recovered reschedule parks the live
// running TI in up_for_reschedule with its next-poke time, keyed by id.
func TestReconcilerRescheduleByIDIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("outcome_resched_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "sensor")
	tiID := outcomeTIID(t, ctx, runUUID, "sensor")

	at := time.Now().UTC().Add(5 * time.Minute)
	if err := exec.RescheduleTask(ctx, tiID, 1, 0, at); err != nil {
		t.Fatalf("RescheduleTask: %v", err)
	}
	if st := taskInstanceState(t, sched, ctx, runUUID, "sensor"); st != domain.TaskStateUpForReschedule {
		t.Fatalf("TI 'sensor' = %q, want up_for_reschedule", st)
	}
}
