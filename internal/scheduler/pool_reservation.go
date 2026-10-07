package scheduler

import (
	"sort"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Starvation reservation (ADR 0066 §4). Weighted admission lets small tasks
// keep taking a pool's slots as they free, so a large task could wait forever.
// The leader remembers when it first saw each task held only by its pool, and
// once one has waited past the threshold it reserves the pool for it: the pool
// then admits nothing else until that task fits. The state is in memory only; a
// failover forgets it, which costs at most one more threshold of waiting.

// taskRef identifies a task instance across runs.
type taskRef struct{ runID, taskID string }

// SetPoolStarvationThreshold sets scheduler.pool_starvation_threshold: how
// long a task held only by its pool waits before the pool is reserved for it.
// Zero (the default for a Scheduler built without it) disables reservations.
// Call before the scheduler ticks.
func (s *Scheduler) SetPoolStarvationThreshold(d time.Duration) { s.starvationThreshold = d }

// reservationsOn reports whether the tick tracks waits and reservations.
func (s *Scheduler) reservationsOn() bool {
	return s.poolsEnabled && s.starvationThreshold > 0
}

// forgetPoolWaits drops every wait and reservation; used when leadership is lost.
func (s *Scheduler) forgetPoolWaits() {
	s.poolWaitSince, s.poolReservations, s.oversizeWarned = nil, nil, nil
}

// pruneReservations drops, before the tick plans, every reservation whose task
// is no longer scheduled (its run ended, it was skipped or canceled) or that
// the pool's budget no longer supports: the pool became unlimited, or the task
// is now larger than the whole pool. It returns the reservations the tick plans
// with, nil when none remain.
func (s *Scheduler) pruneReservations(runs []RunState, budgets map[string]int) map[string]PoolReservation {
	for pool, r := range s.poolReservations {
		budget := budgets[pool]
		slots, scheduled := reservedTaskSlots(runs, r)
		if !scheduled || budget <= 0 || slots > budget {
			delete(s.poolReservations, pool)
		}
	}
	if len(s.poolReservations) == 0 {
		return nil
	}
	return s.poolReservations
}

// reservedTaskSlots finds a reserved task among the active runs and reports
// its slots and whether it is still scheduled.
func reservedTaskSlots(runs []RunState, r PoolReservation) (slots int, scheduled bool) {
	for i := range runs {
		if runs[i].RunID != r.RunID {
			continue
		}
		if runs[i].States[r.TaskID] != domain.TaskStateScheduled {
			return 0, false
		}
		for _, t := range runs[i].Tasks {
			if t.TaskID == r.TaskID {
				return t.EffectivePoolSlots(), true
			}
		}
	}
	return 0, false
}

// recordPoolWaits updates the waits after a tick and makes the reservations
// that start on the next one. waits are this tick's tasks held only by their
// pool, keyed by run. A task not held this tick forgets its wait (it was
// admitted, or something other than the pool now holds it). A reservation
// whose task was not held this tick is dropped too: it was admitted, which
// releases the pool, or it stopped passing the other gates, and a task held by
// anything but the pool never freezes it. Then each unreserved pool goes to
// its oldest waiter past the threshold that can fit the whole pool; a waiter
// larger than the pool is logged once and never reserved for.
func (s *Scheduler) recordPoolWaits(now time.Time, waits map[string][]PoolWait, budgets map[string]int) {
	held := make(map[taskRef]PoolWait)
	for runID, ws := range waits {
		for _, w := range ws {
			held[taskRef{runID, w.TaskID}] = w
		}
	}
	since := make(map[taskRef]time.Time, len(held))
	for ref := range held {
		first, ok := s.poolWaitSince[ref]
		if !ok {
			first = now
		}
		since[ref] = first
	}
	s.poolWaitSince = since
	for pool, r := range s.poolReservations {
		if _, ok := held[taskRef{r.RunID, r.TaskID}]; !ok {
			delete(s.poolReservations, pool)
		}
	}
	for _, ref := range starvedOldestFirst(since, now, s.starvationThreshold) {
		w := held[ref]
		if _, taken := s.poolReservations[w.Pool]; taken {
			continue
		}
		if budget := budgets[w.Pool]; w.Slots > budget {
			s.warnOversize(ref, w, budget)
			continue
		}
		if s.poolReservations == nil {
			s.poolReservations = map[string]PoolReservation{}
		}
		s.poolReservations[w.Pool] = PoolReservation{RunID: ref.runID, TaskID: ref.taskID}
		s.logger.Info("pool reserved for a task waiting longer than the starvation threshold",
			"pool", w.Pool, "run", ref.runID, "task", ref.taskID, "slots", w.Slots,
			"waiting", now.Sub(since[ref]).Round(time.Second))
	}
	for ref := range s.oversizeWarned {
		if _, ok := held[ref]; !ok {
			delete(s.oversizeWarned, ref)
		}
	}
}

// warnOversize logs, once per task, a task larger than its whole pool: it can
// never be admitted and is never reserved for, since that would freeze the pool.
func (s *Scheduler) warnOversize(ref taskRef, w PoolWait, budget int) {
	if s.oversizeWarned[ref] {
		return
	}
	if s.oversizeWarned == nil {
		s.oversizeWarned = map[taskRef]bool{}
	}
	s.oversizeWarned[ref] = true
	s.logger.Warn("task is larger than its whole pool and will wait until the pool grows",
		"pool", w.Pool, "run", ref.runID, "task", ref.taskID, "slots", w.Slots, "pool_slots", budget)
}

// starvedOldestFirst returns the tasks that have waited at least threshold,
// oldest first, then by run and task ID so ties resolve the same way every
// tick. Only those are sorted, so a tick with no starved task sorts nothing.
func starvedOldestFirst(since map[taskRef]time.Time, now time.Time, threshold time.Duration) []taskRef {
	var refs []taskRef
	for ref, first := range since {
		if now.Sub(first) >= threshold {
			refs = append(refs, ref)
		}
	}
	sort.Slice(refs, func(i, j int) bool {
		a, b := refs[i], refs[j]
		if ta, tb := since[a], since[b]; !ta.Equal(tb) {
			return ta.Before(tb)
		}
		if a.runID != b.runID {
			return a.runID < b.runID
		}
		return a.taskID < b.taskID
	})
	return refs
}
