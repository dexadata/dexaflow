package scheduler

import (
	"context"
	"maps"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// maxSameTickPasses bounds how many times eager promotion re-plans one run
// within a tick. Each pass is one PlanRun over the run, so a tick stays O(passes
// x tasks) however deep a skip cascade goes; what the bound leaves undecided is
// picked up by the next tick, exactly as without the gate.
const maxSameTickPasses = 8

// wakeMinGap is the shortest spacing between a woken tick and the tick before
// it. Completion reports can arrive faster than a tick runs; the gap keeps a
// burst of them from turning the loop into a busy loop against the database,
// while still cutting the wait from a full interval to about this long. See
// wakeGap for how it grows with the tick's own duration.
const wakeMinGap = 100 * time.Millisecond

// wakeGap is the spacing, measured from the start of the previous tick, that a
// woken tick waits for: wakeMinGap, or twice the previous tick's duration when
// that is longer, so ticks pulled forward by wakes keep the loop busy at most
// half the time however slow a tick gets on a large installation. It is capped
// at the loop interval, which stays the upper bound between ticks.
func wakeGap(lastTick, interval time.Duration) time.Duration {
	return min(max(wakeMinGap, 2*lastTick), interval)
}

// EnableEagerPromotion turns on scheduler.eager_promotion: same-tick re-planning
// of a run after its own state changes, and early ticks on Wake. Off by default
// (ADR 0062), in which case the tick promotes one step per interval and Wake is
// inert. Call once before the scheduler starts running.
func (s *Scheduler) EnableEagerPromotion() { s.eagerPromotion = true }

// Wake asks the loop to tick now instead of at the next interval, typically
// because a task just settled and its downstreams may be ready. It never blocks:
// wakes coalesce into at most one pending tick. With eager promotion off the
// loop does not listen and Wake does nothing. A woken follower runs its usual
// empty tick, so leader-only semantics are unchanged.
func (s *Scheduler) Wake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

// waitWakeGap holds a woken tick back until wakeGap has passed since the start
// of the previous tick, which took lastTick. It reports false when ctx ends
// first.
func (s *Scheduler) waitWakeGap(ctx context.Context, last time.Time, lastTick time.Duration) bool {
	wait := time.Until(last.Add(wakeGap(lastTick, s.interval)))
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// admit charges one queued promotion of taskID to the run's result: one
// max_active_tasks admission and, when the pool gate is on (poolOf non-nil), the
// task's slots in its pool (ADR 0066).
func (res *advanced) admit(poolOf map[string]poolCharge, taskID string) {
	res.admitted++
	if poolOf == nil {
		return
	}
	if res.byPool == nil {
		res.byPool = map[string]int{}
	}
	c := poolOf[taskID]
	res.byPool[c.key] += c.slots
}

// promoteSameTick re-plans a run after a pass persisted plain transitions, so
// what those transitions unlock happens in this tick rather than the next: a
// task the pass scheduled is queued and dispatched, and a skip or upstream
// failure cascades to its downstreams. It returns the run with every persisted
// plain transition folded into its states, which FinalizeRun then reads.
// planned is the first pass's plan, whose queued promotions are folded too.
//
// Each re-plan is restricted to what the previous pass changed. Retry, reschedule
// and infra re-place decisions belong to the first pass only, and a task that
// was already scheduled when the tick began was offered once by that pass, so a
// dispatch that failed or a gate that parked it is not retried until the next
// tick. Admission gates see every promotion made by earlier passes exactly once:
// a promoted task is folded as queued, so the planner does not offer it again,
// and its charge reaches the gates through ActiveTaskCount and PoolActive,
// weighted by its slots (ADR 0066). The pool waits of each re-plan join the
// first pass's, so the starvation clock of a task a re-plan scheduled starts in
// this tick (ADR 0066 §4). The loop stops when a pass changes nothing, or after
// maxSameTickPasses re-plans.
func (s *Scheduler) promoteSameTick(ctx context.Context, run RunState, planned []PlannedTransition, settled *transitionBatch, poolOf map[string]poolCharge, res *advanced) (RunState, error) {
	baseActive, basePool := run.ActiveTaskCount, run.PoolActive
	reported := make(map[string]bool, len(res.waits))
	for _, w := range res.waits {
		reported[w.TaskID] = true
	}
	owned := false
	for range maxSameTickPasses {
		fresh := settled.foldable()
		if len(fresh) == 0 {
			break
		}
		if !owned {
			// The store's snapshot is not ours to change; fold into a copy.
			run.States = maps.Clone(run.States)
			owned = true
		}
		for _, t := range planned {
			if t.To == domain.TaskStateQueued {
				run.States[t.TaskID] = domain.TaskStateQueued
			}
		}
		scheduled := make(map[string]bool, len(fresh))
		for taskID, to := range fresh {
			run.States[taskID] = to
			if to == domain.TaskStateScheduled {
				scheduled[taskID] = true
			}
		}
		run.ActiveTaskCount = baseActive + res.admitted
		run.PoolActive = withAdmitted(basePool, res.byPool)
		replanned, waits := planRun(run)
		for _, w := range waits {
			if !reported[w.TaskID] {
				reported[w.TaskID] = true
				res.waits = append(res.waits, w)
			}
		}
		plan := sameTickFollowUps(replanned, scheduled)
		if len(plan) == 0 {
			break
		}
		var err error
		if settled, err = s.applyPlan(ctx, run, plan, poolOf, res); err != nil {
			return run, err
		}
		planned = plan
	}
	return run, nil
}

// foldable returns the batch's persisted transitions that a re-plan should
// see: the planner's start decisions (scheduled, skipped, upstream_failed).
// up_for_retry is left out on purpose, so a re-plan derives a pending retry
// exactly as the first pass did instead of advancing it. A row the flush's
// source-state guard left alone (#1605) is left out too, so a re-plan never
// dispatches a task a concurrent writer moved.
func (b *transitionBatch) foldable() map[string]domain.TaskState {
	out := map[string]domain.TaskState{}
	for _, k := range b.order {
		switch k.to {
		case domain.TaskStateScheduled, domain.TaskStateSkipped, domain.TaskStateUpstreamFailed:
			for _, taskID := range b.applied[k] {
				out[taskID] = k.to
			}
		default:
		}
	}
	return out
}

// sameTickFollowUps keeps the transitions a re-plan may apply: start decisions
// for tasks still in none (their upstreams changed in the previous pass), and
// queued promotions of tasks that pass newly scheduled. Everything else was
// already decided by the first pass of this tick.
func sameTickFollowUps(plan []PlannedTransition, newlyScheduled map[string]bool) []PlannedTransition {
	out := plan[:0]
	for _, t := range plan {
		switch t.To {
		case domain.TaskStateQueued:
			if newlyScheduled[t.TaskID] {
				out = append(out, t)
			}
		case domain.TaskStateScheduled, domain.TaskStateSkipped, domain.TaskStateUpstreamFailed:
			out = append(out, t)
		default:
		}
	}
	return out
}

// withAdmitted returns the pool occupancy a re-plan must see: the tick's
// occupancy plus what this run already admitted. It copies, because the tick's
// map is shared by every run and the Step loop folds this run's admissions into
// it on its own once the run is done.
func withAdmitted(occupied, admitted map[string]int) map[string]int {
	if len(admitted) == 0 {
		return occupied
	}
	out := make(map[string]int, len(occupied)+len(admitted))
	maps.Copy(out, occupied)
	for k, n := range admitted {
		out[k] += n
	}
	return out
}
