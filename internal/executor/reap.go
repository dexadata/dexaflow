package executor

import (
	"context"
	"log/slog"
	"runtime/debug"
	"time"
)

// ReapCandidate is one running dag run the reaper is considering, with the
// timestamp of its most recent observable activity (max of the run's started_at
// and its task instances' started_at / ended_at). The reaper compares the gap
// from this stamp to "now" against a stall threshold; a non-zero gap larger
// than the threshold means the run is orphaned and should be failed.
type ReapCandidate struct {
	RunID        string
	DagID        string
	LastActivity time.Time
}

// IsOrphaned reports whether a running run has been quiet long enough to be
// declared orphaned. A zero LastActivity (no progress signal at all) counts as
// orphaned: a running run with no recorded activity since at least its
// started_at is, by definition, a run nothing is touching. Future timestamps
// (clock skew) are treated as fresh — the reaper is a backstop, not a clock
// arbiter, so it errs on the side of leaving recent-looking runs alone.
func IsOrphaned(c ReapCandidate, threshold time.Duration, now time.Time) bool {
	if c.LastActivity.IsZero() {
		return true
	}
	return now.Sub(c.LastActivity) >= threshold
}

// ReapStore is the slice of scheduler.Store the reaper needs. The full
// scheduler.Store embeds this interface so production wires through one type;
// the unit tests fake just this surface.
type ReapStore interface {
	// ListReapCandidates returns every dag_run currently in 'running' state
	// alongside its last-activity timestamp. The query is the authority on what
	// "running" means and how to compute the timestamp; the reaper only decides
	// whether each one has been quiet for too long.
	ListReapCandidates(ctx context.Context) ([]ReapCandidate, error)
	// ReapRun transitions a run to 'failed' with an "orphaned" note and fails
	// any still-active task instances, but only if the run is still orphaned
	// at that moment: the store re-checks the orphan predicate atomically,
	// with quietBefore as the last-activity cutoff, because the list is only a
	// snapshot. It reports false, with no error, when the run is no longer an
	// orphan and nothing was touched. It is idempotent: a second call on the
	// same run reports false.
	ReapRun(ctx context.Context, runID string, quietBefore time.Time) (bool, error)
}

// orphanReaper is the scheduler-internal worker that fails dag runs whose
// last observed activity is older than threshold. It is invoked once per
// scheduler tick, only on the leader.
type orphanReaper struct {
	store     ReapStore
	logger    *slog.Logger
	threshold time.Duration
	recorder  DecisionRecorder
	// pods tears down the reaped run's pods after the DB transition (#474).
	// Nil in Lite (no pods); the delete is skipped.
	pods PodManager
	// gate is re-checked before every destructive call (see destructiveGate).
	gate destructiveGate
}

func newOrphanReaper(store ReapStore, logger *slog.Logger, threshold time.Duration, rec DecisionRecorder) *orphanReaper {
	return &orphanReaper{store: store, logger: logger, threshold: threshold, recorder: rec}
}

// run lists every candidate, reaps the stale ones, and returns any infra-level
// list error so the caller can log it. Per-run reap failures are isolated:
// they are logged and metered but never abort the loop, so one poison run
// never blocks the others (same pattern as advanceSafely).
func (r *orphanReaper) run(ctx context.Context) error {
	defer func() {
		if rec := recover(); rec != nil {
			r.logger.Error("orphan reaper panic recovered", "panic", rec, "stack", string(debug.Stack()))
			r.record("orphan_panic")
		}
	}()
	candidates, err := r.store.ListReapCandidates(ctx)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	quietBefore := now.Add(-r.threshold)
	for _, c := range candidates {
		if !IsOrphaned(c, r.threshold, now) {
			continue
		}
		r.reapOne(ctx, c, quietBefore)
	}
	return nil
}

// reapOne fails one orphaned run and tears down its pods, re-checking the
// destructive gate immediately before each write. quietBefore is the
// last-activity cutoff the store re-checks atomically; when the run turns out to
// be no longer orphaned the reap is a no-op and the pods are left alone.
func (r *orphanReaper) reapOne(ctx context.Context, c ReapCandidate, quietBefore time.Time) {
	if !gateOpen(r.gate, ctx) {
		r.record("orphan_gate_skip")
		return
	}
	reaped, rerr := r.store.ReapRun(ctx, c.RunID, quietBefore)
	if rerr != nil {
		r.logger.Error("reaping orphan run", "run", c.RunID, "dag", c.DagID, "error", rerr)
		r.record("orphan_reap_error")
		return
	}
	if !reaped {
		// The run resumed (or was finalized) between the list and the reap. It
		// is live again, so its pods are doing its work: no teardown.
		r.logger.Info("orphan candidate no longer orphaned at reap time", "run", c.RunID, "dag", c.DagID, "last_activity", c.LastActivity)
		r.record("orphan_reap_noop")
		return
	}
	r.logger.Warn("reaped orphan run", "run", c.RunID, "dag", c.DagID, "last_activity", c.LastActivity)
	r.record("orphan_reaped")
	// The run is now durably failed; tear down its pods so any still-running
	// task containers stop instead of finishing abandoned work (#474). The
	// run-id is unique, so this can only ever hit this run's pods. Best-effort:
	// a delete failure is logged but does not undo the DB reap — the pod's own
	// ActiveDeadline and the reconciler's GC remain backstops.
	//
	// "Its pods" is narrower than every pod of the run: this reaper reads no pod
	// presence, so DeleteRunPods applies the terminal-phase skip per pod (#928).
	// A finished task's pod keeps the outcome record the reconciler settles from;
	// only the run's still-live containers are stopped.
	if r.pods == nil {
		return
	}
	if !gateOpen(r.gate, ctx) {
		r.record("orphan_teardown_gate_skip")
		return
	}
	if derr := r.pods.DeleteRunPods(ctx, c.RunID); derr != nil {
		r.logger.Error("deleting orphaned run pods", "run", c.RunID, "dag", c.DagID, "error", derr)
		r.record("orphan_pod_delete_error")
	}
}

func (r *orphanReaper) record(decision string) {
	if r.recorder != nil {
		r.recorder.RecordSchedulerDecision(decision)
	}
}
