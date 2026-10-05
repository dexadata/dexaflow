package scheduler

import (
	"hash/fnv"
	"math"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// PlannedTransition is a decided state change for a task instance within a run.
type PlannedTransition struct {
	TaskID string
	To     domain.TaskState
}

// PlanRun computes the task transitions for one dag run. It first handles
// retries — a failed task with retry budget moves to up_for_retry, and an
// up_for_retry task resets (none, try_number+1) — then plans the rest off the
// resulting effective states: none -> scheduled (or skipped / upstream_failed
// per the trigger rule) and scheduled -> queued. A failed task that can still
// recover — app-retriable, or infra-failed with re-place budget left (even while
// parked in its re-place backoff) — is treated as still active, so downstream
// tasks wait rather than seeing a failure; a downstream is condemned to
// upstream_failed only once its upstream is terminally failed. The result is
// deterministic: identical inputs yield identical output.
func PlanRun(run RunState) []PlannedTransition {
	g := run.taskGraph()
	n := len(run.Tasks)
	// Per-task rows are addressed by the graph's slot, not by task_id, so the
	// planner pays one States lookup per task instead of rebuilding three maps
	// per run. stored is the persisted state; effective folds pending retries in
	// so downstream planning sees a retriable failure as active rather than
	// terminal.
	rows := make([]domain.TaskState, 2*n)
	stored, effective := rows[:n:n], rows[n:]
	for i, t := range run.Tasks {
		if g.slot[i] == i {
			stored[i] = run.States[t.TaskID]
		}
	}
	copy(effective, stored)
	decided := make([]bool, n)
	out := make([]PlannedTransition, 0, n)

	out = planRetryTransitions(run, g, stored, effective, decided, out)

	// Admission gates (ADR 0053): a scheduled task promotes to queued only if it
	// clears BOTH the per-DAG max_active_tasks gate (Stage 1) and, on the Pro
	// path, the cross-DAG named-pool slot gate (Stage 3). headroom is the
	// remaining max_active_tasks budget this tick (math.MaxInt when unset, so that
	// gate is a no-op); promoted tracks what we spend against it. poolPromoted
	// tracks per-pool promotions this call so several ready tasks in one pool
	// cannot together overshoot the pool's free slots. Both gates only ever leave
	// a task parked (scheduled), the same "downstream waits" discipline the retry
	// and reschedule rails use.
	headroom := admissionHeadroom(run)
	promoted := 0
	var poolPromoted map[string]int
	var upstreamStates []domain.TaskState
	for i, t := range run.Tasks {
		s := g.slot[i]
		if decided[s] {
			continue
		}
		switch effective[s] {
		case domain.TaskStateNone:
			upstreamStates = g.upstreamStates(run, s, effective, upstreamStates[:0])
			if to, ok := decideStart(t, upstreamStates); ok {
				out = append(out, PlannedTransition{TaskID: t.TaskID, To: to})
			}
		case domain.TaskStateScheduled:
			// A previous dispatch may have failed; hold off re-dispatch until the
			// backoff elapses (ADR 0031 Amendment A), mirroring the reschedule gate.
			if !readyToDispatch(run, t.TaskID) {
				continue
			}
			if promoted >= headroom {
				continue // DAG at max_active_tasks — park until a sibling frees a slot.
			}
			pk := poolKeyFor(run, t)
			if !poolHasSlot(run, pk, poolPromoted) {
				continue // pool at capacity — park until a slot frees anywhere in the pool.
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateQueued})
			promoted++
			if pk != "" {
				if poolPromoted == nil {
					poolPromoted = map[string]int{}
				}
				poolPromoted[pk]++
			}
		default:
			// queued/running/terminal/up_for_retry: nothing to plan here.
		}
	}
	return out
}

// upstreamStates appends the effective state of each upstream of slot s to buf.
// An upstream outside the task list has no row, so its state comes from the
// run's stored states, which is what the map-keyed planner read for it.
func (g *TaskGraph) upstreamStates(run RunState, s int, effective, buf []domain.TaskState) []domain.TaskState {
	for j, p := range g.upstream[s] {
		if p >= 0 {
			buf = append(buf, effective[p])
		} else {
			buf = append(buf, run.States[g.upstreamIDs[s][j]])
		}
	}
	return buf
}

// defaultPoolName is the implicit pool a task with no declared pool draws from,
// so the pool gate is always well-defined (ADR 0053: "a task with no pool uses
// an implicit default pool"). Mirrors domain.DefaultPoolName.
const defaultPoolName = "default_pool"

// PoolKey composes the cross-DAG admission-budget key for a (tenant, pool) pair.
// Pools are tenant-scoped, so a pool name is only meaningful within its tenant;
// the key namespaces the pool budget and occupancy maps by tenant. The NUL
// separator cannot occur in a tenant UUID or an Airflow pool name, so the join is
// unambiguous. The scheduler store builds its budget map with the same key.
func PoolKey(tenant, pool string) string {
	return tenant + "\x00" + pool
}

// resolvePool maps an unset task pool to the implicit default pool.
func resolvePool(pool string) string {
	if pool == "" {
		return defaultPoolName
	}
	return pool
}

// poolKeyFor returns the admission-budget key for a task's pool, or "" when the
// named-pool gate is disabled (Lite / non-Pro). Returning "" makes poolHasSlot a
// no-op, so planning on the Lite path is byte-identical to the
// max_active_tasks-only path.
func poolKeyFor(run RunState, t domain.TaskSpec) string {
	if !run.PoolsEnabled {
		return ""
	}
	return PoolKey(run.TenantID, resolvePool(t.Pool))
}

// poolHasSlot reports whether the task's pool has a free slot this tick: the
// pool's cap minus its cross-DAG active occupancy (PoolActive) minus what this
// run already promoted into the pool this call (promotedByPool). A disabled gate
// (key ""), or a pool with a non-positive or absent budget (unset/undefined),
// is unlimited — fail open, never deadlock a DAG on a misconfigured pool.
func poolHasSlot(run RunState, poolKey string, promotedByPool map[string]int) bool {
	if poolKey == "" {
		return true
	}
	budget := run.PoolBudgets[poolKey]
	if budget <= 0 {
		return true
	}
	return run.PoolActive[poolKey]+promotedByPool[poolKey] < budget
}

// admissionHeadroom returns how many more of this DAG's scheduled tasks PlanRun
// may promote to queued this tick under the per-DAG max_active_tasks cap (ADR
// 0053 Stage 1). A non-positive cap means unlimited — the gate is a no-op, so an
// unset DAG (and all of Lite, which never sets the field) plans byte-identically
// to today; math.MaxInt is the "unbounded" sentinel the promotion loop compares
// against. Otherwise it is the cap minus the DAG's already-active (queued+
// running) task instances, floored at zero (never negative, so a DAG over its
// cap simply admits nothing rather than wrapping).
func admissionHeadroom(run RunState) int {
	if run.MaxActiveTasks <= 0 {
		return math.MaxInt
	}
	if headroom := run.MaxActiveTasks - run.ActiveTaskCount; headroom > 0 {
		return headroom
	}
	return 0
}

// planRetryTransitions handles the retry/reschedule rail: a failed task with
// budget moves to up_for_retry; an up_for_retry or up_for_reschedule task resets
// to none once its cooldown/poke time elapses. It records the effective state and
// marks each handled task decided so the main loop leaves it alone, and appends
// the transitions to emit to out. Rows are addressed by the graph's slot.
func planRetryTransitions(run RunState, g *TaskGraph, stored, effective []domain.TaskState, decided []bool, out []PlannedTransition) []PlannedTransition {
	for i, t := range run.Tasks {
		s := g.slot[i]
		switch stored[s] {
		case domain.TaskStateFailed:
			switch {
			case run.InfraFailed[t.TaskID]:
				// Infra fault (agent/pod/dispatch lost): re-place the task WITHOUT
				// consuming its retry budget — an infrastructure failure is not the
				// user's task failing (ADR 0051 Phase 1). Bounded by a separate
				// infra-attempt limit so a poison placement can't loop forever;
				// exhausted → terminal (no fallback to the app-retry budget). The
				// store bumps infra_attempts (not try_number) when applying failed→none.
				//
				// The effective state downstream planning sees is decoupled from the
				// transition emitted this tick. A re-placeable task is ACTIVE for its
				// downstream from the moment it is re-placeable — not only once its
				// backoff elapses — mirroring the app-retry branch below. Otherwise a
				// downstream would be persisted upstream_failed (terminal; nothing
				// reverts it) during the backoff, condemning the run even though the
				// upstream goes on to re-run and succeed. A downstream may only see
				// `failed` once the upstream is terminally failed (budget exhausted).
				if infraReplaceable(run, t.TaskID) {
					effective[s] = domain.TaskStateUpForRetry
					if readyToInfraReplace(run, t.TaskID) {
						out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
						effective[s] = domain.TaskStateNone
					}
				}
				decided[s] = true
			case retriable(run, t.TaskID):
				out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateUpForRetry})
				effective[s] = domain.TaskStateUpForRetry
				decided[s] = true
			}
		case domain.TaskStateUpForRetry:
			if !readyToRetry(run, t.TaskID) {
				decided[s] = true
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[s] = domain.TaskStateNone
			decided[s] = true
		case domain.TaskStateUpForReschedule:
			// Re-dispatch once reschedule_at passes, WITHOUT consuming retry budget
			// (reschedule is not a failure); until then keep it parked so downstream
			// waits. Mirrors the up_for_retry rail, gated on reschedule_at (#380).
			if !readyToReschedule(run, t.TaskID) {
				decided[s] = true
				continue
			}
			out = append(out, PlannedTransition{TaskID: t.TaskID, To: domain.TaskStateNone})
			effective[s] = domain.TaskStateNone
			decided[s] = true
		default:
			// none/scheduled/queued/running/terminal: no retry decision here.
		}
	}
	return out
}

// retriable reports whether a failed task still has retry budget (the current
// try number is below its max). Absent budget data it is false, so tasks fail
// terminally by default.
func retriable(run RunState, taskID string) bool {
	return run.Tries[taskID] < run.MaxTries[taskID]
}

// readyToRetry reports whether the cooldown window from the user's declared
// retry_delay_seconds has elapsed since the task ended. The check honors the
// "absent data falls back to immediate retry" convention so legacy callers
// (tests, in-flight DAGs predating issue #201) keep working unchanged.
//
// Returns true when:
//   - delay is 0 (no cooldown declared), OR
//   - the task's EndedAt is not recorded (can't compute, retry immediately), OR
//   - run.Now is zero (no clock provided — test seam preserves old behavior), OR
//   - run.Now >= EndedAt + delay (cooldown has elapsed)
func readyToRetry(run RunState, taskID string) bool {
	delay := run.RetryDelaySeconds[taskID]
	if delay <= 0 {
		return true
	}
	ended := run.EndedAt[taskID]
	if ended == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	return !run.Now.Before(ended.Add(time.Duration(delay) * time.Second))
}

// readyToDispatch reports whether a `scheduled` task may be dispatched now: true
// unless a prior synchronous dispatch failure set next_dispatch_at in the future.
// Honors the "absent data / zero clock falls back to immediate" convention
// (mirroring readyToReschedule), so the common case (never failed) and tests that
// do not populate NextDispatchAt/Now dispatch immediately.
func readyToDispatch(run RunState, taskID string) bool {
	at := run.NextDispatchAt[taskID]
	if at == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	return !run.Now.Before(*at)
}

// readyToReschedule reports whether a task parked in up_for_reschedule may be
// re-dispatched: true when reschedule_at has passed. It honors the "absent data /
// zero clock falls back to immediate" convention (mirroring readyToRetry) so tests
// and callers that don't populate RescheduleAt/Now keep the simplest behavior.
//
// Returns true when:
//   - the task has no recorded reschedule_at (re-dispatch now), OR
//   - run.Now is zero (no clock provided — test seam), OR
//   - run.Now >= reschedule_at.
func readyToReschedule(run RunState, taskID string) bool {
	at := run.RescheduleAt[taskID]
	if at == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	return !run.Now.Before(*at)
}

func decideStart(t domain.TaskSpec, upstreamStates []domain.TaskState) (domain.TaskState, bool) {
	switch EvaluateTriggerRule(triggerRuleOf(t), upstreamStates) {
	case DecisionSchedule:
		return domain.TaskStateScheduled, true
	case DecisionSkip:
		return domain.TaskStateSkipped, true
	case DecisionUpstreamFailed:
		return domain.TaskStateUpstreamFailed, true
	default:
		return "", false
	}
}

func triggerRuleOf(t domain.TaskSpec) domain.TriggerRule {
	if t.TriggerRule == "" {
		return domain.TriggerRuleAllSuccess
	}
	return t.TriggerRule
}

// infraReplaceable reports whether a failed task is an infra fault (agent/pod/
// dispatch lost) still within its re-place budget — one the scheduler will
// return to 'none' rather than leave terminal (ADR 0051 Phase 1). Such a task is
// NOT terminal for run finalization, mirroring how a retriable failed task keeps
// the run active until the retry resolves.
func infraReplaceable(run RunState, taskID string) bool {
	return run.InfraFailed[taskID] && run.InfraAttempts[taskID] < infraMaxAttempts
}

// infraReplaceJitterWindow spreads sibling infra re-placements across this window
// so N tasks reaped in one tick (a control-plane restart marking the whole run
// agent_lost) do not all re-dispatch simultaneously. 2× the heartbeat interval —
// enough to de-synchronize the herd without materially delaying recovery.
const infraReplaceJitterWindow = 30 * time.Second

// readyToInfraReplace gates the infra re-placement (failed→none) behind the same
// exponential backoff as a synchronous dispatch failure, keyed on the
// infra-attempt count and measured from the reap's ended_at, plus a deterministic
// per-task jitter (#859). Without it, a mass infra fault re-dispatches every
// sibling on the next tick — a thundering herd of pod create/delete against the
// kube-apiserver from the just-recovered scheduler, which re-throttles it. Honors
// the "absent ended_at / zero clock → immediate" convention (mirroring
// readyToRetry) so the never-failed common case and tests are unaffected.
func readyToInfraReplace(run RunState, taskID string) bool {
	ended := run.EndedAt[taskID]
	if ended == nil {
		return true
	}
	if run.Now.IsZero() {
		return true
	}
	delay := dispatchBackoff(run.InfraAttempts[taskID]+1) + infraReplaceJitter(run.RunID, taskID)
	return !run.Now.Before(ended.Add(delay))
}

// infraReplaceJitter returns a stable per-(run,task) offset in
// [0, infraReplaceJitterWindow). Deterministic (FNV-1a over the key, no rand) so
// the planner stays reproducible and unit-testable; distinct across sibling
// task_ids so they spread rather than fire together.
func infraReplaceJitter(runID, taskID string) time.Duration {
	h := fnv.New64a()
	_, _ = h.Write([]byte(runID + "\x00" + taskID))
	return time.Duration(h.Sum64() % uint64(infraReplaceJitterWindow))
}

// FinalizeRun reports the terminal dag-run state once every task is terminal.
// A failed task that still has retry budget (or an infra re-place budget) counts
// as non-terminal, so the run keeps running until it resolves. The boolean is
// false while any task is still non-terminal.
func FinalizeRun(run RunState) (domain.DagRunState, bool) {
	anyFailed := false
	for _, t := range run.Tasks {
		st := run.States[t.TaskID]
		if st == domain.TaskStateFailed {
			// Infra faults route EXCLUSIVELY through the infra budget, matching
			// planRetryTransitions. They preserve try_number, so `retriable` would
			// wrongly keep the run alive after the infra budget is spent — the
			// planner never app-retries an InfraFailed task, so the run would hang.
			if run.InfraFailed[t.TaskID] {
				if infraReplaceable(run, t.TaskID) {
					return "", false
				}
			} else if retriable(run, t.TaskID) {
				return "", false
			}
		}
		if !st.IsTerminal() {
			return "", false
		}
		if st == domain.TaskStateFailed || st == domain.TaskStateUpstreamFailed {
			anyFailed = true
		}
	}
	if anyFailed {
		return domain.DagRunStateFailed, true
	}
	return domain.DagRunStateSuccess, true
}
