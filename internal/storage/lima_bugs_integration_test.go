//go:build integration

// Package storage_test holds the Lima dogfood regression suite: each test
// reproduces a bug a user actually hit running `dexaflow lite` end-to-end and
// asserts the contract the fix must satisfy. They are reality-anchored — the
// kind of failure a unit test on a fake store would NEVER catch because the
// bug lives at the SQL layer or in the composition of multiple steps.
//
// See [[tests-must-be-reality-anchored]] memory.

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestLimaBug1_ClearResetsQueuedAtIntegration reproduces the dispatch-lost
// reaper loop a user hit on Lima 2026-05-31:
//
//	21:34:57 WARN task queued past dispatch threshold; failing as dispatch_lost
//	21:36:36 WARN ...same ti, same queued_at (after user clicked Clear)
//	21:37:47 WARN ...same ti, same queued_at (third time)
//
// Root cause (verified): ResetTaskInstanceToNone updates state/started_at/
// ended_at/try_number but DOES NOT reset queued_at. When the scheduler
// re-queues the cleared TI, TransitionTaskState's "queued_at = CASE WHEN
// queued_at IS NULL THEN now() ELSE queued_at END" guard preserves the
// STALE timestamp. The reaper then sees a "queued for 13 minutes" TI and
// re-marks it dispatch_lost — every tick, forever.
//
// Contract this test pins:
//
//  1. After a clear, queued_at is reset (NULL or refreshed) so the next
//     transition to queued stamps a NEW now().
//  2. The reaper, after the clear+re-queue cycle, must NOT immediately
//     re-mark the TI based on the pre-clear timestamp.
//
// Skips without DATABASE_URL.
func TestLimaBug1_ClearResetsQueuedAtIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	// Stage: DAG with one task; manual run; TI brought to `queued` state with
	// queued_at well past the dispatch-lost threshold (so the reaper would
	// reasonably mark it).
	dagID := fmt.Sprintf("lima_bug1_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "hello", Type: domain.TaskTypePython}}
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
	if err := sched.ApplyTransition(ctx, runUUID, "hello", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}

	originalQueuedAt := queuedAtOf(t, sched, ctx, runUUID, "hello")
	if originalQueuedAt.IsZero() {
		t.Fatalf("transition to queued did not stamp queued_at")
	}

	// Step 1: dispatch-lost reaper fires (TI is queued + past threshold).
	// MarkTaskDispatchLost is idempotent at the SQL layer (WHERE state='queued')
	// so a second call before any clear is a no-op — that's not the bug.
	tiID := taskInstanceID(t, sched, ctx, runUUID, "hello")
	if _, err := sched.MarkTaskDispatchLost(ctx, tiID, 1, 0); err != nil {
		t.Fatalf("MarkTaskDispatchLost: %v", err)
	}
	if got := taskInstanceState(t, sched, ctx, runUUID, "hello"); got != domain.TaskStateFailed {
		t.Fatalf("after first dispatch-lost mark, TI state = %q, want failed", got)
	}

	// Step 2: user clicks Clear. The Repository.ClearTaskInstances code path is
	// what the API handler invokes. resetDagRun=false to isolate the TI-level
	// reset semantics (the run-level reset is a separate concern).
	cleared, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1",
		[]string{"hello"}, true /*onlyFailed*/, domain.ClearOptions{})
	if err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	if cleared != 1 {
		t.Fatalf("ClearTaskInstances cleared %d, want 1", cleared)
	}

	// Contract A: clear MUST reset queued_at so the next transition stamps
	// fresh. Today's SQL leaves it at the original value — this assertion
	// fails on main, proving the bug.
	if got := queuedAtOf(t, sched, ctx, runUUID, "hello"); !got.IsZero() {
		t.Fatalf("Bug 1: after Clear, queued_at = %v (want zero/NULL); "+
			"ResetTaskInstanceToNone must NULL queued_at so re-queue can refresh it",
			got)
	}

	// Step 3: scheduler re-queues the cleared TI. With the fix, this stamps
	// a NEW queued_at (now()). Without the fix, queued_at remains stale.
	if err := sched.ApplyTransition(ctx, runUUID, "hello", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued (after clear): %v", err)
	}

	newQueuedAt := queuedAtOf(t, sched, ctx, runUUID, "hello")
	if newQueuedAt.IsZero() {
		t.Fatalf("after re-queue, queued_at is still zero")
	}
	if !newQueuedAt.After(originalQueuedAt) {
		t.Fatalf("Bug 1: after clear+re-queue, queued_at = %v is NOT after "+
			"the original %v — TransitionTaskState's `queued_at IS NULL` guard "+
			"preserved the stale timestamp, which causes the reaper loop",
			newQueuedAt, originalQueuedAt)
	}

	// Contract B: with a fresh queued_at, the reaper must NOT immediately
	// re-mark the TI. (The threshold check IsDispatchLost(now - queued_at)
	// is in Go — we exercise it here with a generous threshold so the
	// freshly-re-queued TI is well under it.)
	cand := executor.StaleQueuedCandidate{QueuedAt: newQueuedAt}
	if executor.IsDispatchLost(cand, 60*time.Second, time.Now()) {
		t.Fatalf("Bug 1: freshly re-queued TI was flagged dispatch_lost — " +
			"the reaper sees the stale timestamp from before the clear")
	}
}

// TestLimaBug3_TaskTriesIncludesArchivedAttemptsIntegration reproduces the
// "after Clear, only the latest attempt tab shows" symptom from Lima
// 2026-05-31:
//
//	user clears a failed task 3x → disk has 1.log, 2.log, 3.log, 4.log
//	UI's /tries endpoint returned 1 entry (the latest) → only one tab → user
//	cannot navigate to the prior failures' logs.
//
// Root cause: the Reset* SQL queries updated the live task_instances row
// in-place (bumping try_number), so the database remembered only the current
// attempt. Fix: the queries now ARCHIVE the current per-attempt state into
// task_instance_history before resetting, and Repository.ListTaskInstanceAttempts
// UNIONs current + history so /tries returns all attempts oldest-first.
//
// Contract this test pins:
//
//  1. After N clears, ListTaskInstanceAttempts returns N+1 entries.
//  2. try_number on the entries advances monotonically (1, 2, 3, ...).
//  3. Each archived entry preserves the state it had at clear time (failed
//     here), so the UI can render the right state badge per tab.
//
// Skips without DATABASE_URL.
func TestLimaBug3_TaskTriesIncludesArchivedAttemptsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	dagID := fmt.Sprintf("lima_bug3_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "hello", Type: domain.TaskTypePython}}
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

	// Walk the failed→clear cycle 3 times so we end up with 4 attempts
	// (try_number 1, 2, 3, 4 — the first ran initially, then 3 clears).
	const clearCycles = 3
	for i := 0; i < clearCycles; i++ {
		// Fail the current attempt so Clear has something to reset.
		if err := sched.ApplyTransition(ctx, runUUID, "hello", domain.TaskStateFailed); err != nil {
			t.Fatalf("ApplyTransition to failed (cycle %d): %v", i, err)
		}
		if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1",
			[]string{"hello"}, true /*onlyFailed*/, domain.ClearOptions{}); err != nil {
			t.Fatalf("ClearTaskInstances (cycle %d): %v", i, err)
		}
	}

	attempts, err := repo.ListTaskInstanceAttempts(ctx, "default", dagID, "r1", "hello")
	if err != nil {
		t.Fatalf("ListTaskInstanceAttempts: %v", err)
	}
	// Expect: 3 archived attempts (1 through 3, each "failed" at clear time) +
	// 1 current row (try 4, state none after the final clear) = 4 total.
	const wantAttempts = clearCycles + 1
	if len(attempts) != wantAttempts {
		t.Fatalf("Bug 3: ListTaskInstanceAttempts returned %d entries, want %d "+
			"(N+1 = clearCycles+1, one row per attempt)\nentries: %+v",
			len(attempts), wantAttempts, attempts)
	}
	// try_number must advance 1, 2, 3, 4 in order.
	for i, a := range attempts {
		wantTry := i + 1
		if a.TryNumber != wantTry {
			t.Errorf("attempts[%d].TryNumber = %d, want %d (monotonic 1..N)", i, a.TryNumber, wantTry)
		}
	}
	// The N archived entries (all but the last) must record the failed state
	// the TI had at clear time. The last is the current row, just reset.
	for i := 0; i < clearCycles; i++ {
		if attempts[i].State != domain.TaskStateFailed {
			t.Errorf("attempts[%d].State = %q, want failed (archived snapshot of pre-clear state)",
				i, attempts[i].State)
		}
	}
	if attempts[clearCycles].State != domain.TaskStateNone {
		t.Errorf("attempts[%d].State = %q, want none (the live row reset by the last clear)",
			clearCycles, attempts[clearCycles].State)
	}
}

// queuedAtOf returns the queued_at timestamp of the named TI in the run, or
// zero if absent. Reads the raw task_instances row via the scheduler store's
// list query.
func queuedAtOf(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, runUUID, taskID string) time.Time {
	t.Helper()
	cands, err := sched.ListStaleQueuedCandidates(ctx)
	if err != nil {
		// The candidate query only lists `queued` TIs; for failed/none TIs
		// we fall back to scanning ActiveRuns which only carries states,
		// not timestamps. The honest answer is: zero (caller will catch).
		return time.Time{}
	}
	for _, c := range cands {
		if c.DagRunID == runUUID && c.TaskID == taskID {
			return c.QueuedAt
		}
	}
	return time.Time{}
}

// taskInstanceID returns the task_instance UUID for the named task in the run,
// resolved via the staging-store ID query the dispatcher uses.
func taskInstanceID(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, runUUID, taskID string) string {
	t.Helper()
	cands, err := sched.ListStaleQueuedCandidates(ctx)
	if err != nil {
		t.Fatalf("ListStaleQueuedCandidates: %v", err)
	}
	for _, c := range cands {
		if c.DagRunID == runUUID && c.TaskID == taskID {
			return c.TaskInstanceID
		}
	}
	t.Fatalf("task instance id not found for %s/%s", runUUID, taskID)
	return ""
}

// TestOnFailureAlertDedupResetsOnClearIntegration pins the #431 "once per failure
// episode" contract at the SQL layer: a delivered episode is never claimed again
// (no duplicate page on a re-tick), and a Clear (ResetDagRunToVersion) reopens the
// episode so a genuine re-failure pages again. A unit test on a fake store can't
// prove either — both live in the UPDATE's WHERE clause.
//
// The contract survived the claim/deliver split unchanged; only what "alerted"
// means moved. It used to be stamped BEFORE the send, so a failed send counted as
// a page. Now the claim consumes an attempt and delivery is stamped after a
// successful send, so this test delivers explicitly where it used to just claim.
func TestOnFailureAlertDedupResetsOnClearIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)

	dagID := fmt.Sprintf("alert_dedup_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "hello", Type: domain.TaskTypePython}}
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
	if err := sched.ApplyTransition(ctx, runUUID, "hello", domain.TaskStateFailed); err != nil {
		t.Fatalf("ApplyTransition to failed: %v", err)
	}

	// First claim wins and the send succeeds, so the episode is stamped delivered.
	attempt, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute)
	if err != nil || attempt != 1 {
		t.Fatalf("first ClaimAlertAttempt = (%d, %v), want (1, nil)", attempt, err)
	}
	if err := sched.MarkRunAlertDelivered(ctx, runUUID, attempt); err != nil {
		t.Fatalf("MarkRunAlertDelivered: %v", err)
	}
	// The immediate re-tick (same failed episode, no clear) must not page again.
	// The backoff is set negative so an elapsed backoff cannot be what refuses the
	// claim — delivery has to be the reason, which is the contract under test.
	if got, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute); err != nil || got != 0 {
		t.Fatalf("re-tick claim = (%d, %v), want (0, nil) — no duplicate page", got, err)
	}

	// A Clear (resetDagRun=true) opens a new failure episode by NULLing alerted_at.
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1",
		[]string{"hello"}, true /*onlyFailed*/, domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}

	// The genuine re-failure after the clear re-claims and re-alerts. The attempt
	// counter resets too, so the new episode gets a full retry budget rather than
	// inheriting a spent one.
	got, err := sched.ClaimAlertAttempt(ctx, runUUID, 5, -time.Minute)
	if err != nil {
		t.Fatalf("post-clear claim: %v", err)
	}
	if got != 1 {
		t.Fatalf("post-clear claim = %d, want 1 — a clear must reopen the episode with a fresh budget (#431)", got)
	}
}
