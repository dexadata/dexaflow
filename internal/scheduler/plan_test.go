package scheduler

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

func linear() []domain.TaskSpec {
	return []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}
}

func planMap(run RunState) map[string]domain.TaskState {
	out := make(map[string]domain.TaskState)
	for _, p := range PlanRun(run) {
		out[p.TaskID] = p.To
	}
	return out
}

func st(tasks []domain.TaskSpec, states map[string]domain.TaskState) RunState {
	return RunState{Tasks: tasks, States: states}
}

func TestPlanRunSchedulesRootTask(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateNone, "b": domain.TaskStateNone}))
	if got["a"] != domain.TaskStateScheduled {
		t.Errorf("root a = %q, want scheduled", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("b should wait on a, got %q", got["b"])
	}
}

func TestPlanRunPromotesScheduledToQueued(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateScheduled}))
	if got["a"] != domain.TaskStateQueued {
		t.Errorf("scheduled a = %q, want queued", got["a"])
	}
}

func TestPlanRunSchedulesDownstreamAfterSuccess(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone}))
	if got["b"] != domain.TaskStateScheduled {
		t.Errorf("b = %q, want scheduled after a success", got["b"])
	}
}

func TestPlanRunPropagatesUpstreamFailure(t *testing.T) {
	got := planMap(st(linear(), map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone}))
	if got["b"] != domain.TaskStateUpstreamFailed {
		t.Errorf("b = %q, want upstream_failed", got["b"])
	}
}

func TestPlanRunAllDoneSchedulesAfterFailure(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "cleanup", Type: domain.TaskTypePython, DependsOn: []string{"a"}, TriggerRule: domain.TriggerRuleAllDone},
	}
	got := planMap(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateFailed, "cleanup": domain.TaskStateNone}))
	if got["cleanup"] != domain.TaskStateScheduled {
		t.Errorf("all_done cleanup = %q, want scheduled", got["cleanup"])
	}
}

func TestPlanRunRetriesFailedTaskWithBudget(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 1},
		MaxTries: map[string]int{"a": 3},
	}
	got := planMap(run)
	if got["a"] != domain.TaskStateUpForRetry {
		t.Errorf("retriable a = %q, want up_for_retry", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("b must wait while a is retriable, got %q", got["b"])
	}
}

func TestPlanRunNoRetryWhenExhausted(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 3},
		MaxTries: map[string]int{"a": 3},
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("exhausted a must not retry, got %q", got["a"])
	}
	if got["b"] != domain.TaskStateUpstreamFailed {
		t.Errorf("b = %q, want upstream_failed once a is terminally failed", got["b"])
	}
}

// TestPlanRunInfraFailureDoesNotConsumeRetryBudget pins the Phase-1 fix (ADR 0051):
// a task whose failure was infra-caused (agent/pod/dispatch lost) re-places WITHOUT
// consuming the task's retry budget. Here the app-retry budget is exhausted, yet the
// task still re-places — proof it routes to the infra rail, not `retriable()`.
func TestPlanRunInfraFailureDoesNotConsumeRetryBudget(t *testing.T) {
	run := RunState{
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 3}, // app-retry budget EXHAUSTED
		MaxTries:      map[string]int{"a": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": 0},
	}
	got := planMap(run)
	if got["a"] != domain.TaskStateNone {
		t.Errorf("infra-failed task should re-place (none) without app budget, got %q", got["a"])
	}
}

// TestPlanRunInfraBudgetExhaustedIsTerminal: the infra re-place is bounded — once the
// separate infra-attempt budget is spent, a poison placement fails terminally rather
// than looping forever, and it does NOT fall back to the app-retry budget.
func TestPlanRunInfraBudgetExhaustedIsTerminal(t *testing.T) {
	run := RunState{
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 0}, // app budget available…
		MaxTries:      map[string]int{"a": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": infraMaxAttempts}, // …but infra budget spent
	}
	if _, ok := planMap(run)["a"]; ok {
		t.Error("infra-exhausted task should be terminal (no transition), not app-retried")
	}
}

// TestPlanRunInfraReplaceRespectsBackoff: an infra-failed task is NOT re-placed
// immediately — it waits out an exponential backoff from its ended_at (#859), so
// a mass infra fault (a control-plane restart marking many TIs agent_lost at
// once) cannot re-dispatch every sibling on the same tick and stampede the
// just-recovered kube-apiserver. Within the backoff the task stays parked in
// failed (no transition); past it, it re-places to none.
func TestPlanRunInfraReplaceRespectsBackoff(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-2 * time.Second) // < 5s base backoff
	run := RunState{
		RunID:         "run-1",
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 0},
		MaxTries:      map[string]int{"a": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": 0},
		EndedAt:       map[string]*time.Time{"a": &ended},
		Now:           now,
	}
	if _, ok := planMap(run)["a"]; ok {
		t.Error("infra-failed task within backoff must stay parked in failed (no transition)")
	}
	// Past the base backoff plus the whole jitter window, it must re-place.
	run.Now = ended.Add(dispatchBackoffBase + infraReplaceJitterWindow)
	if got := planMap(run)["a"]; got != domain.TaskStateNone {
		t.Errorf("infra-failed task past backoff must re-place (none), got %q", got)
	}
}

// TestReadyToInfraReplace pins the gate's edges: nil ended / zero clock fall back
// to immediate (test seam + never-failed), and the delay grows with attempts.
func TestReadyToInfraReplace(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-3 * time.Second)
	run := RunState{
		RunID:         "run-1",
		EndedAt:       map[string]*time.Time{"a": &ended},
		InfraAttempts: map[string]int{"a": 0},
		Now:           now,
	}
	if readyToInfraReplace(run, "a") {
		t.Error("3s elapsed < 5s base backoff: must not be ready")
	}
	run.Now = ended.Add(dispatchBackoffBase + infraReplaceJitterWindow)
	if !readyToInfraReplace(run, "a") {
		t.Error("past base backoff + jitter window: must be ready")
	}
	run.Now = time.Time{}
	if !readyToInfraReplace(run, "a") {
		t.Error("zero clock is the immediate test seam")
	}
	empty := RunState{RunID: "r", EndedAt: map[string]*time.Time{}, InfraAttempts: map[string]int{}, Now: now}
	if !readyToInfraReplace(empty, "a") {
		t.Error("nil ended must fall back to immediate")
	}
}

// TestInfraReplaceJitterSpreadsSiblings: the jitter is deterministic per (run,
// task) and spreads siblings across the window so N tasks reaped together do not
// re-dispatch simultaneously.
func TestInfraReplaceJitterSpreadsSiblings(t *testing.T) {
	seen := map[time.Duration]bool{}
	for _, task := range []string{"a", "b", "c", "d", "e", "f"} {
		j := infraReplaceJitter("run-1", task)
		if j < 0 || j >= infraReplaceJitterWindow {
			t.Errorf("jitter %v out of [0,%v)", j, infraReplaceJitterWindow)
		}
		seen[j] = true
	}
	if len(seen) < 3 {
		t.Errorf("expected the jitter to spread siblings, got %d distinct values", len(seen))
	}
	j1, j2 := infraReplaceJitter("run-1", "a"), infraReplaceJitter("run-1", "a")
	if j1 != j2 {
		t.Errorf("jitter must be deterministic for a given (run, task): %v != %v", j1, j2)
	}
}

// TestPlanRunInfraReplaceInBackoffDoesNotCondemnDownstream pins the lazy
// upstream_failed invariant: a downstream must see its upstream as failed ONLY
// once the upstream is terminally failed. An infra-failed upstream that is still
// re-placeable but parked in its re-place backoff is an ACTIVE upstream — the
// downstream waits, exactly as it does for an app-retriable upstream. Before the
// fix the infra branch left the effective state as `failed` during the backoff,
// so the downstream was persisted `upstream_failed` (a terminal state nothing
// ever reverts) while its upstream went on to re-run and succeed — condemning
// the run on a control-plane restart that the upstream itself survived.
func TestPlanRunInfraReplaceInBackoffDoesNotCondemnDownstream(t *testing.T) {
	now := time.Now().UTC()
	ended := now.Add(-2 * time.Second) // < 5s base backoff: replaceable, not yet ready
	run := RunState{
		RunID:         "run-1",
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 0},
		MaxTries:      map[string]int{"a": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": 0},
		EndedAt:       map[string]*time.Time{"a": &ended},
		Now:           now,
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("a within the infra backoff must stay parked (no transition), got %q", got["a"])
	}
	if to, ok := got["b"]; ok {
		t.Errorf("b must WAIT while its upstream is still infra-replaceable, got planned to %q", to)
	}

	// Past the backoff the upstream re-places to none; the downstream still waits
	// (none is an active state) rather than being condemned or scheduled.
	run.Now = ended.Add(dispatchBackoffBase + infraReplaceJitterWindow)
	got = planMap(run)
	if got["a"] != domain.TaskStateNone {
		t.Errorf("a past the backoff must re-place (none), got %q", got["a"])
	}
	if to, ok := got["b"]; ok {
		t.Errorf("b must still wait while a re-places, got planned to %q", to)
	}
}

// TestPlanRunDownstreamSchedulesAfterInfraReplaceSucceeds: once the re-placed
// upstream runs again and succeeds, the downstream — which was never condemned —
// plans none→scheduled. The InfraFailed marker of a task that has since
// succeeded is irrelevant to downstream planning.
func TestPlanRunDownstreamSchedulesAfterInfraReplaceSucceeds(t *testing.T) {
	run := RunState{
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateNone},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": 1},
	}
	if got := planMap(run); got["b"] != domain.TaskStateScheduled {
		t.Errorf("b = %q, want scheduled once the re-placed upstream succeeded", got["b"])
	}
}

// TestPlanRunInfraExhaustedCondemnsDownstream is the regression guard for the
// lazy marking: once the upstream is TERMINALLY failed — infra budget spent, and
// infra faults never fall back to the app-retry budget — the downstream DOES get
// upstream_failed. Lazy must not mean never.
func TestPlanRunInfraExhaustedCondemnsDownstream(t *testing.T) {
	run := RunState{
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateNone},
		Tries:         map[string]int{"a": 0}, // app budget available, but infra routes exclusively
		MaxTries:      map[string]int{"a": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": infraMaxAttempts},
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("infra-exhausted a must be terminal (no transition), got %q", got["a"])
	}
	if got["b"] != domain.TaskStateUpstreamFailed {
		t.Errorf("b = %q, want upstream_failed once a is terminally failed", got["b"])
	}
}

func TestPlanRunResetsUpForRetry(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:    map[string]int{"a": 2},
		MaxTries: map[string]int{"a": 3},
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("up_for_retry a = %q, want none (reset)", got["a"])
	}
}

func TestFinalizeRun(t *testing.T) {
	tasks := linear()
	if _, done := FinalizeRun(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateRunning})); done {
		t.Error("should not finalize while b is running")
	}
	if state, done := FinalizeRun(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateSuccess})); !done || state != domain.DagRunStateSuccess {
		t.Errorf("all success => (%q,%v), want (success,true)", state, done)
	}
	if state, done := FinalizeRun(st(tasks, map[string]domain.TaskState{"a": domain.TaskStateSuccess, "b": domain.TaskStateFailed})); !done || state != domain.DagRunStateFailed {
		t.Errorf("one failed => (%q,%v), want (failed,true)", state, done)
	}
}

// TestPlanRunHoldsUpForRetryDuringDelay pins issue #201: a task in up_for_retry
// must NOT be released back to `none` until `retry_delay_seconds` have elapsed
// since `ended_at`. Without this gate, retries fire on the next tick (~1s),
// defeating the user-declared backoff that exists precisely to give transient
// failure causes (rate limits, upstream blips) time to recover.
func TestPlanRunHoldsUpForRetryDuringDelay(t *testing.T) {
	endedAt := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 2},
		MaxTries:          map[string]int{"a": 3},
		EndedAt:           map[string]*time.Time{"a": &endedAt},
		RetryDelaySeconds: map[string]int{"a": 60}, // 1 minute cooldown
		Now:               endedAt.Add(10 * time.Second),
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("a planned %q at 10s after failure; should remain up_for_retry until +60s", got["a"])
	}
}

// TestPlanRunReleasesUpForRetryAfterDelay completes the contract: the same
// task IS released to `none` (and then to `scheduled` via decideStart in the
// same plan pass) once the cooldown has elapsed.
func TestPlanRunReleasesUpForRetryAfterDelay(t *testing.T) {
	endedAt := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 2},
		MaxTries:          map[string]int{"a": 3},
		EndedAt:           map[string]*time.Time{"a": &endedAt},
		RetryDelaySeconds: map[string]int{"a": 60},
		Now:               endedAt.Add(61 * time.Second),
	}
	got := planMap(run)
	// up_for_retry → none in this tick; subsequent tick handles none → scheduled
	// (the existing TestPlanRunResetsUpForRetry pins the same single-step shape).
	if got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q at 61s after failure; want none (cooldown elapsed, ready to reset)", got["a"])
	}
}

// TestPlanRunHoldsUpForRescheduleUntilTime: a reschedule-mode sensor parked in
// up_for_reschedule is NOT re-dispatched until reschedule_at passes, and its
// downstream waits meanwhile (ADR 0040 Phase B, #380).
func TestPlanRunHoldsUpForRescheduleUntilTime(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:        linear(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(-30 * time.Second),
	}
	got := planMap(run)
	if _, ok := got["a"]; ok {
		t.Errorf("a planned %q before reschedule_at; should stay up_for_reschedule", got["a"])
	}
	if _, ok := got["b"]; ok {
		t.Errorf("downstream b planned %q while a is up_for_reschedule; should wait", got["b"])
	}
}

// TestPlanRunReleasesUpForRescheduleAfterTime: once reschedule_at passes, the task
// returns to none for re-dispatch (next tick does none → scheduled), the same
// single-step shape as the retry release.
func TestPlanRunReleasesUpForRescheduleAfterTime(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:        linear(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(time.Second),
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q after reschedule_at; want none (ready to re-dispatch)", got["a"])
	}
}

// TestPlanRunRescheduleIgnoresRetryBudget: reschedule is not a retry — re-dispatch
// happens even with the retry budget exhausted (try_number is preserved, no budget
// consumed; #380). This is the key difference from the up_for_retry rail.
func TestPlanRunRescheduleIgnoresRetryBudget(t *testing.T) {
	at := time.Date(2026, 5, 31, 12, 0, 0, 0, time.UTC)
	run := RunState{
		Tasks:        linear(),
		States:       map[string]domain.TaskState{"a": domain.TaskStateUpForReschedule, "b": domain.TaskStateNone},
		Tries:        map[string]int{"a": 3},
		MaxTries:     map[string]int{"a": 3}, // exhausted — must not block a reschedule
		RescheduleAt: map[string]*time.Time{"a": &at},
		Now:          at.Add(time.Second),
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q with retry budget exhausted; reschedule must ignore budget", got["a"])
	}
}

// TestPlanRunBackwardsCompatNoRetryDelay confirms that when retry_delay_seconds
// is 0 or absent (legacy behavior), the task transitions immediately — no
// regression for DAGs that opted out of backoff.
func TestPlanRunBackwardsCompatNoRetryDelay(t *testing.T) {
	run := RunState{
		Tasks:             linear(),
		States:            map[string]domain.TaskState{"a": domain.TaskStateUpForRetry, "b": domain.TaskStateNone},
		Tries:             map[string]int{"a": 2},
		MaxTries:          map[string]int{"a": 3},
		RetryDelaySeconds: map[string]int{"a": 0}, // no delay
		// EndedAt absent, Now zero — neither matters when delay == 0
	}
	if got := planMap(run); got["a"] != domain.TaskStateNone {
		t.Errorf("a = %q with no retry_delay; want none (immediate reset, same as existing behavior)", got["a"])
	}
}

func TestFinalizeRunWaitsForRetriableFailure(t *testing.T) {
	run := RunState{
		Tasks:    linear(),
		States:   map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateSuccess},
		Tries:    map[string]int{"a": 1},
		MaxTries: map[string]int{"a": 3},
	}
	if _, done := FinalizeRun(run); done {
		t.Error("must not finalize the run while a failed task can still retry")
	}
}

// TestFinalizeRunWaitsForInfraReplace: a failed+infra task within its infra
// budget is being re-placed, so the run must stay active (ADR 0051 Phase 1).
func TestFinalizeRunWaitsForInfraReplace(t *testing.T) {
	run := RunState{
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateSuccess},
		Tries:         map[string]int{"a": 1},
		MaxTries:      map[string]int{"a": 3},
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": 0},
	}
	if _, done := FinalizeRun(run); done {
		t.Error("must not finalize while an infra-failed task is still re-placeable")
	}
}

// TestFinalizeRunFinalizesExhaustedInfra: an infra fault preserves try_number, so
// an infra-EXHAUSTED task can still look "retriable" — but the planner routes an
// InfraFailed task EXCLUSIVELY and never app-retries it. FinalizeRun must mirror
// that and finalize the run as failed, never hang forever (ADR 0051 Phase 1).
func TestFinalizeRunFinalizesExhaustedInfra(t *testing.T) {
	run := RunState{
		Tasks:         linear(),
		States:        map[string]domain.TaskState{"a": domain.TaskStateFailed, "b": domain.TaskStateSuccess},
		Tries:         map[string]int{"a": 1}, // try_number untouched by infra faults
		MaxTries:      map[string]int{"a": 3}, // app-retry budget still "available"
		InfraFailed:   map[string]bool{"a": true},
		InfraAttempts: map[string]int{"a": infraMaxAttempts}, // infra budget spent
	}
	state, done := FinalizeRun(run)
	if !done || state != domain.DagRunStateFailed {
		t.Errorf("exhausted-infra task must finalize the run as failed (no hang), got (%q,%v)", state, done)
	}
}

// A scheduled task whose next_dispatch_at is in the future is NOT promoted to
// queued this tick — its previous dispatch failed and the backoff has not
// elapsed (ADR 0031 Amendment A). Mirrors the up_for_reschedule gate.
func TestPlanRunDefersScheduledDuringDispatchBackoff(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	future := now.Add(30 * time.Second)
	run := RunState{
		Tasks:          linear(),
		States:         map[string]domain.TaskState{"a": domain.TaskStateScheduled},
		Now:            now,
		NextDispatchAt: map[string]*time.Time{"a": &future},
	}
	if got := planMap(run)["a"]; got == domain.TaskStateQueued {
		t.Error("a scheduled task still in dispatch backoff must not be promoted to queued")
	}
}

// Once next_dispatch_at has passed, the scheduled task is promoted to queued so
// the dispatch is re-attempted.
func TestPlanRunRedispatchesAfterBackoffElapses(t *testing.T) {
	now := time.Date(2026, 7, 31, 12, 0, 0, 0, time.UTC)
	past := now.Add(-1 * time.Second)
	run := RunState{
		Tasks:          linear(),
		States:         map[string]domain.TaskState{"a": domain.TaskStateScheduled},
		Now:            now,
		NextDispatchAt: map[string]*time.Time{"a": &past},
	}
	if got := planMap(run)["a"]; got != domain.TaskStateQueued {
		t.Errorf("a scheduled task past its backoff should be queued, got %s", got)
	}
}

// Absent next_dispatch_at (the common case: never failed to dispatch) promotes
// immediately, preserving today's behavior.
func TestPlanRunSchedulesImmediatelyWithoutBackoff(t *testing.T) {
	run := RunState{
		Tasks:  linear(),
		States: map[string]domain.TaskState{"a": domain.TaskStateScheduled},
	}
	if got := planMap(run)["a"]; got != domain.TaskStateQueued {
		t.Errorf("a scheduled task with no backoff should be queued, got %s", got)
	}
}

// independent returns n root tasks with no dependencies — a fan-out whose tasks
// are all simultaneously ready, the shape the per-DAG max_active_tasks cap
// bounds (ADR 0053 Stage 1).
func independent(n int) []domain.TaskSpec {
	tasks := make([]domain.TaskSpec, n)
	for i := range tasks {
		tasks[i] = domain.TaskSpec{TaskID: fmt.Sprintf("t%d", i), Type: domain.TaskTypePython}
	}
	return tasks
}

// scheduledStates marks every task `scheduled` — all ready to promote to queued.
func scheduledStates(tasks []domain.TaskSpec) map[string]domain.TaskState {
	states := make(map[string]domain.TaskState, len(tasks))
	for _, t := range tasks {
		states[t.TaskID] = domain.TaskStateScheduled
	}
	return states
}

// countQueued tallies the scheduled→queued promotions in a plan.
func countQueued(transitions []PlannedTransition) int {
	n := 0
	for _, p := range transitions {
		if p.To == domain.TaskStateQueued {
			n++
		}
	}
	return n
}

// TestPlanRunMaxActiveTasksGatesPromotion: a fan-out of T ready tasks under
// max_active_tasks=k promotes exactly k to queued this tick; the rest stay
// scheduled and are re-evaluated next tick (ADR 0053 Stage 1).
func TestPlanRunMaxActiveTasksGatesPromotion(t *testing.T) {
	tasks := independent(5)
	run := RunState{Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: 2}
	if got := countQueued(PlanRun(run)); got != 2 {
		t.Errorf("promoted %d tasks, want 2 (max_active_tasks cap)", got)
	}
}

// TestPlanRunMaxActiveTasksCountsActiveSiblings: the cap is against the DAG-wide
// count of already-active (queued+running) task instances (ActiveTaskCount), so
// a DAG holding 2 active TIs under a cap of 3 admits only one more this tick.
func TestPlanRunMaxActiveTasksCountsActiveSiblings(t *testing.T) {
	tasks := independent(5)
	run := RunState{Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: 3, ActiveTaskCount: 2}
	if got := countQueued(PlanRun(run)); got != 1 {
		t.Errorf("promoted %d, want 1 (3 cap − 2 already active)", got)
	}
}

// TestPlanRunMaxActiveTasksFullAdmitsNone: at the cap no scheduled task is
// promoted; they stay parked until a sibling finishes and frees a slot.
func TestPlanRunMaxActiveTasksFullAdmitsNone(t *testing.T) {
	tasks := independent(4)
	run := RunState{Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: 2, ActiveTaskCount: 2}
	if got := countQueued(PlanRun(run)); got != 0 {
		t.Errorf("promoted %d, want 0 (DAG at max_active_tasks)", got)
	}
}

// TestPlanRunMaxActiveTasksReleasesAsTasksFinish: once active TIs drain
// (finished → terminal, no longer counted in ActiveTaskCount), headroom reopens
// and the parked tasks promote on a later tick.
func TestPlanRunMaxActiveTasksReleasesAsTasksFinish(t *testing.T) {
	tasks := independent(4)
	full := RunState{Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: 2, ActiveTaskCount: 2}
	if got := countQueued(PlanRun(full)); got != 0 {
		t.Fatalf("full DAG promoted %d, want 0", got)
	}
	drained := RunState{Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: 2, ActiveTaskCount: 0}
	if got := countQueued(PlanRun(drained)); got != 2 {
		t.Errorf("after tasks finished promoted %d, want 2", got)
	}
}

// TestPlanRunMaxActiveTasksUnsetPromotesAll: an unset cap (0) is unlimited —
// today's behavior — so every ready task promotes. This is the Lite-safety
// invariant: a DAG that never sets max_active_tasks plans byte-identically.
func TestPlanRunMaxActiveTasksUnsetPromotesAll(t *testing.T) {
	tasks := independent(5)
	run := RunState{Tasks: tasks, States: scheduledStates(tasks)} // MaxActiveTasks 0 = unlimited
	if got := countQueued(PlanRun(run)); got != 5 {
		t.Errorf("unset cap promoted %d, want all 5", got)
	}
}

// TestPlanRunMaxActiveTasksNonPositiveUnlimited: a non-positive cap (defensive,
// e.g. a hand-edited spec) also means unlimited — fail open, never lock a DAG out.
func TestPlanRunMaxActiveTasksNonPositiveUnlimited(t *testing.T) {
	tasks := independent(3)
	run := RunState{Tasks: tasks, States: scheduledStates(tasks), MaxActiveTasks: -1}
	if got := countQueued(PlanRun(run)); got != 3 {
		t.Errorf("negative cap promoted %d, want all 3 (unlimited)", got)
	}
}

// --- Named-pool admission gate (ADR 0053 Stage 3) ---------------------------

const testTenant = "t1"

// pooledTasks returns n independent tasks all drawing on the named pool.
func pooledTasks(n int, pool string) []domain.TaskSpec {
	tasks := independent(n)
	for i := range tasks {
		tasks[i].Pool = pool
	}
	return tasks
}

// poolRun builds an enabled-pool run: every task scheduled, in the given tenant,
// with the supplied per-pool budget and current occupancy maps (keyed by PoolKey).
func poolRun(tasks []domain.TaskSpec, budgets, active map[string]int) RunState {
	return RunState{
		TenantID: testTenant, Tasks: tasks, States: scheduledStates(tasks),
		PoolsEnabled: true, PoolBudgets: budgets, PoolActive: active,
	}
}

// TestPlanRunPoolGatesPromotion: a fan-out of tasks all in a 2-slot pool promotes
// exactly 2 this tick; the rest stay scheduled (ADR 0053 Stage 3).
func TestPlanRunPoolGatesPromotion(t *testing.T) {
	tasks := pooledTasks(5, "p")
	budgets := map[string]int{PoolKey(testTenant, "p"): 2}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, nil))); got != 2 {
		t.Errorf("promoted %d, want 2 (pool slot cap)", got)
	}
}

// TestPlanRunPoolCountsActiveOccupancy: the cap is against the pool's cross-DAG
// active occupancy, so a pool of 3 already holding 1 active TI admits 2 more.
func TestPlanRunPoolCountsActiveOccupancy(t *testing.T) {
	tasks := pooledTasks(5, "p")
	budgets := map[string]int{PoolKey(testTenant, "p"): 3}
	active := map[string]int{PoolKey(testTenant, "p"): 1}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, active))); got != 2 {
		t.Errorf("promoted %d, want 2 (3 slots − 1 occupied)", got)
	}
}

// TestPlanRunPoolFullAdmitsNone: a pool at capacity promotes nothing; tasks stay
// parked until a slot frees, the same discipline max_active_tasks uses.
func TestPlanRunPoolFullAdmitsNone(t *testing.T) {
	tasks := pooledTasks(4, "p")
	budgets := map[string]int{PoolKey(testTenant, "p"): 2}
	active := map[string]int{PoolKey(testTenant, "p"): 2}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, active))); got != 0 {
		t.Errorf("promoted %d, want 0 (pool full)", got)
	}
}

// TestPlanRunPoolShrunkBelowOccupancyAdmitsNone: a pool re-sized below what it
// already runs (default_pool_slots lowered through the service API) admits
// nothing and never computes negative headroom; admission resumes as running
// tasks drain below the new size.
func TestPlanRunPoolShrunkBelowOccupancyAdmitsNone(t *testing.T) {
	tasks := pooledTasks(4, "p")
	budgets := map[string]int{PoolKey(testTenant, "p"): 2}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, map[string]int{PoolKey(testTenant, "p"): 5}))); got != 0 {
		t.Errorf("promoted %d over a shrunk pool, want 0", got)
	}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, map[string]int{PoolKey(testTenant, "p"): 1}))); got != 1 {
		t.Errorf("promoted %d once drained to 1 of 2, want 1", got)
	}
}

// TestPlanRunPoolUnsetTaskUsesDefaultPool: a task with no declared pool draws on
// the implicit default_pool, so a 2-slot default_pool bounds it to 2.
func TestPlanRunPoolUnsetTaskUsesDefaultPool(t *testing.T) {
	tasks := independent(5) // no Pool set → default_pool
	budgets := map[string]int{PoolKey(testTenant, domain.DefaultPoolName): 2}
	if got := countQueued(PlanRun(poolRun(tasks, budgets, nil))); got != 2 {
		t.Errorf("promoted %d, want 2 (unset pool → default_pool cap)", got)
	}
}

// TestPlanRunPoolDisabledIsNoOp is the Lite-safety lock: with PoolsEnabled false,
// the pool gate never runs even when a budget would otherwise bind, so planning is
// byte-identical to the max_active_tasks-only path — every ready task promotes.
func TestPlanRunPoolDisabledIsNoOp(t *testing.T) {
	tasks := pooledTasks(5, "p")
	run := RunState{
		TenantID: testTenant, Tasks: tasks, States: scheduledStates(tasks),
		PoolsEnabled: false, // Lite / non-Pro
		PoolBudgets:  map[string]int{PoolKey(testTenant, "p"): 2},
		PoolActive:   map[string]int{PoolKey(testTenant, "p"): 0},
	}
	if got := countQueued(PlanRun(run)); got != 5 {
		t.Errorf("pools disabled promoted %d, want all 5 (Lite no-op)", got)
	}
}

// TestPlanRunPoolUndefinedIsUnlimited: an enabled gate whose task references a
// pool with no budget entry fails open (unlimited) rather than deadlocking the DAG.
func TestPlanRunPoolUndefinedIsUnlimited(t *testing.T) {
	tasks := pooledTasks(3, "ghost")
	budgets := map[string]int{PoolKey(testTenant, "other"): 1} // no "ghost" entry
	if got := countQueued(PlanRun(poolRun(tasks, budgets, nil))); got != 3 {
		t.Errorf("undefined pool promoted %d, want all 3 (fail open)", got)
	}
}

// TestPlanRunPoolComposesWithMaxActiveTasks: both gates must pass, so the tighter
// of the two caps wins in each direction.
func TestPlanRunPoolComposesWithMaxActiveTasks(t *testing.T) {
	// max_active_tasks is the tighter gate.
	tasks := pooledTasks(6, "p")
	budgets := map[string]int{PoolKey(testTenant, "p"): 5}
	run := poolRun(tasks, budgets, nil)
	run.MaxActiveTasks = 2
	if got := countQueued(PlanRun(run)); got != 2 {
		t.Errorf("promoted %d, want 2 (max_active_tasks is tighter)", got)
	}
	// pool is the tighter gate.
	budgets2 := map[string]int{PoolKey(testTenant, "p"): 2}
	run2 := poolRun(pooledTasks(6, "p"), budgets2, nil)
	run2.MaxActiveTasks = 5
	if got := countQueued(PlanRun(run2)); got != 2 {
		t.Errorf("promoted %d, want 2 (pool is tighter)", got)
	}
}

// TestPlanRunPoolSeparatePoolsIndependent: tasks in different pools draw on
// separate budgets, so one full pool does not starve another.
func TestPlanRunPoolSeparatePoolsIndependent(t *testing.T) {
	tasks := append(pooledTasks(2, "a"), pooledTasks(2, "b")...)
	// task ids collide across the two pooledTasks calls (t0,t1 each); rekey b's.
	tasks[2].TaskID, tasks[3].TaskID = "b0", "b1"
	budgets := map[string]int{
		PoolKey(testTenant, "a"): 1,
		PoolKey(testTenant, "b"): 2,
	}
	run := poolRun(tasks, budgets, nil)
	if got := countQueued(PlanRun(run)); got != 3 {
		t.Errorf("promoted %d, want 3 (1 from pool a + 2 from pool b)", got)
	}
}
