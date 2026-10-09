package executor

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// DecisionRecorder records the reaper's per-tick decision metrics. It is the
// narrow slice of the scheduler's metrics sink the reapers need — only the one
// counter — so the executor depends on a capability, not on the observability
// package. A nil DecisionRecorder is tolerated (tests need not stub it).
type DecisionRecorder interface {
	RecordSchedulerDecision(decisionType string)
}

// ReaperStore is the full store surface the five reapers need, composed from
// each reaper's own slice. The metadatabase-backed SchedulerStore satisfies all
// five, so production wires through one type; a unit test fakes just the slice
// its reaper touches.
type ReaperStore interface {
	ReapStore
	HeartbeatReapStore
	DispatchLostReapStore
	PodLostReapStore
	WarmWorkerLostReapStore
}

// Default reaper thresholds. These carry over verbatim from the values the
// scheduler used to own; they are the floor a stuck run/TI/pod must sit idle
// before a backstop reaps it.
//
//   - orphan (5m):        a running dag run may stay quiet this long before the
//     run reaper declares it orphaned — well above any healthy tick or hand-off.
//   - agent-lost (90s):   6x the default 15s agent heartbeat interval, tolerating
//     a handful of missed pings before failing the TI as agent_lost.
//   - dispatch-lost (3m): well above healthy dispatch latency on Lite or Pro, so
//     a live dispatch is never reaped but a stuck queued TI is caught quickly.
//   - pod-lost (60s):     a floor against a transient "pod not listed yet" blip
//     right after the running transition; the reap still needs a no-live-pod read.
const (
	defaultOrphanThreshold       = 5 * time.Minute
	defaultAgentLostThreshold    = 90 * time.Second
	defaultDispatchLostThreshold = 3 * time.Minute
	defaultPodLostGrace          = 60 * time.Second
	// defaultSettlingGrace is the post-leadership window during which NO reaper
	// fires (see Reaper.settling). Set to 2× the agent-lost threshold so a single
	// control-plane restart + re-election (whose recorded silence can approach
	// the threshold) cannot trip a mass false reap, while staying well under the
	// 10-minute agent token TTL so a re-heartbeat still authenticates.
	// Ladder: heartbeat(15s) < threshold(90s) < grace(180s) < tokenTTL(600s);
	// 2 × maintenance interval (60s) < grace, so at least two reconcile sweeps
	// complete before the gate can open.
	defaultSettlingGrace = 2 * defaultAgentLostThreshold
)

// ReaperConfig holds the idle thresholds and the post-leadership settling grace
// the reapers apply. Zero values are legal but reap aggressively; callers pass
// DefaultReaperConfig unless a test or load harness deliberately overrides them.
type ReaperConfig struct {
	OrphanThreshold       time.Duration
	AgentLostThreshold    time.Duration
	DispatchLostThreshold time.Duration
	// PodLostGrace is the per-task liveness floor measured from the TI's running
	// transition, below which the pod-lost reaper does not consult pod liveness.
	PodLostGrace time.Duration
	// SettlingGrace is the minimum time after this instance acquires leadership
	// before ANY reaper may act — one rung of the leader-settling gate, which
	// also requires a synced pod informer and a completed reconciler sweep. See
	// defaultSettlingGrace and Reaper.settling. Zero disables the whole gate.
	SettlingGrace time.Duration
}

// DefaultReaperConfig returns the production thresholds — the exact values the
// scheduler configured before the reapers moved here.
func DefaultReaperConfig() ReaperConfig {
	return ReaperConfig{
		OrphanThreshold:       defaultOrphanThreshold,
		AgentLostThreshold:    defaultAgentLostThreshold,
		DispatchLostThreshold: defaultDispatchLostThreshold,
		PodLostGrace:          defaultPodLostGrace,
		SettlingGrace:         defaultSettlingGrace,
	}
}

// destructiveGate reports whether a reaper may take a destructive action — mark
// a TI or run failed, delete a pod — right now. Every reaper consults it
// immediately before each such call (not only at the top of the tick), so a
// shutdown or leader step-down that lands after the candidate list was read
// still stops the write that would follow. A nil gate is open (Lite / tests).
//
// The invariant: a leader that is terminating or stepping down must not mark or
// delete. Its context is being canceled, its view of the fleet is about to go
// stale, and the successor redoes every reap under its own settling gate — so
// a destructive write on the way out is at best redundant and at worst the
// false reap the gate exists to prevent.
type destructiveGate func(ctx context.Context) bool

// gateOpen evaluates a possibly-nil destructiveGate.
func gateOpen(g destructiveGate, ctx context.Context) bool {
	return g == nil || g(ctx)
}

// Reaper is the execution-side backstop that fails stuck runs and task instances
// the scheduler dispatched but that then went dark. It bundles the five
// independent reapers behind one ReapOnce entrypoint the leader's maintenance
// loop drives once per cycle, after the pod reconciler's sweep, so the caller
// depends on a single capability rather than wiring each reaper itself.
type Reaper struct {
	orphan         *orphanReaper
	agentLost      *agentLostReaper
	dispatchLost   *dispatchLostReaper
	podLost        *podLostReaper
	warmWorkerLost *warmWorkerLostReaper
	logger         *slog.Logger
	rec            DecisionRecorder
	// inStepDown reports whether the scheduler is in a graceful leader step-down.
	// It downgrades an expected context-cancel fanout of the run-context to WARN
	// and, with ctx and leading, closes the destructive gate: a leader stepping
	// down must not mark or delete. Nil is tolerated ("not stepping down").
	inStepDown func() bool
	// leading reports whether this instance currently holds scheduler leadership;
	// part of the destructive gate. Nil is tolerated ("leading") so Lite and
	// tests without an election are unaffected.
	leading func() bool
	// now is the clock the settling gate reads; time.Now in production, injected
	// by tests so the gate's three conditions can be driven deterministically.
	now func() time.Time
	// settlingGrace is ReaperConfig.SettlingGrace.
	settlingGrace time.Duration
	// leaderSince, informerSynced and lastSweepCompleted are the three inputs of
	// the leader-settling gate (see settling). Each is nil when its subsystem is
	// not wired (Lite has leadership but no informer or reconciler), and a nil
	// input is "satisfied", so an unwired condition never holds the reaper.
	leaderSince        func() time.Time
	informerSynced     func() bool
	lastSweepCompleted func() time.Time
}

// NewReaper constructs the reapers and wires their pod-teardown / liveness
// capability (pods) and, for the two K8s-aware reapers, the presence cache. The
// wiring mirrors exactly what the scheduler used to do inline: every reaper gets
// pods; only the dispatch-lost and pod-lost reapers get the cache. Nil pods
// (Lite/subprocess) keeps every reaper DB-only and makes the pod-lost reaper a
// no-op; Lite gates agent-lost and dispatch-lost on the agent process instead
// (SetProcessLiveness).
//
// warmPods is the live warm-pod seam (ADR 0058 N1d-a2), threaded to the two warm
// consumers exactly the way pods/cache are threaded: the warm-worker-lost reaper
// (which recovers a dead worker's attempts) and the dispatch-lost reaper's H3
// defer. Nil (warm pools off / not wired) makes the warm reaper a no-op and the
// dispatch-lost warm defer inert — with the flag off no TI ever carries a
// warm_worker_id either, so both warm paths are doubly inert, byte-for-byte today.
func NewReaper(store ReaperStore, pods PodManager, cache PodPresenceCache, warmPods WarmPodLister, rec DecisionRecorder, logger *slog.Logger, cfg ReaperConfig, inStepDown func() bool) *Reaper {
	orphan := newOrphanReaper(store, logger, cfg.OrphanThreshold, rec)
	orphan.pods = pods

	agentLost := newAgentLostReaper(store, logger, cfg.AgentLostThreshold, rec)
	agentLost.pods = pods

	dispatchLost := newDispatchLostReaper(store, logger, cfg.DispatchLostThreshold, rec)
	dispatchLost.pods = pods
	dispatchLost.cache = cache
	dispatchLost.warmPods = warmPods

	podLost := newPodLostReaper(store, logger, cfg.PodLostGrace, rec)
	podLost.pods = pods
	podLost.cache = cache

	warmWorkerLost := newWarmWorkerLostReaper(store, logger, rec)
	warmWorkerLost.warmPods = warmPods

	r := &Reaper{
		orphan:         orphan,
		agentLost:      agentLost,
		dispatchLost:   dispatchLost,
		podLost:        podLost,
		warmWorkerLost: warmWorkerLost,
		logger:         logger,
		rec:            rec,
		inStepDown:     inStepDown,
		now:            time.Now,
		settlingGrace:  cfg.SettlingGrace,
	}
	// One destructive gate, shared by every reaper: each re-checks it right before
	// a mark or delete, so a cancel/step-down that lands mid-tick still stops the
	// write. The closure reads r's predicates at call time, so SetLeading may be
	// wired after construction.
	orphan.gate = r.mayReap
	agentLost.gate = r.mayReap
	dispatchLost.gate = r.mayReap
	podLost.gate = r.mayReap
	warmWorkerLost.gate = r.mayReap
	return r
}

// SetLeading wires the leadership predicate into the destructive gate, so a
// reaper tick on an instance that no longer holds leadership marks and deletes
// nothing. The maintenance loop already drives ReapOnce only while leading;
// this is the defensive re-check at the point of the write. Nil leaves the gate
// on ctx and step-down alone.
func (r *Reaper) SetLeading(fn func() bool) {
	r.leading = fn
}

// mayReap is the destructive gate: open only while the tick's context is live,
// the scheduler is not stepping down, and (when wired) this instance leads.
func (r *Reaper) mayReap(ctx context.Context) bool {
	if ctx.Err() != nil {
		return false
	}
	if r.inStepDown != nil && r.inStepDown() {
		return false
	}
	if r.leading != nil && !r.leading() {
		return false
	}
	return true
}

// SetLogSink wires the sink the agent-lost reaper uses to append a final
// "killed: agent_lost" marker to a reaped attempt's log stream, so a killed
// task's log ends with the reason instead of a silent truncation. Any logs.Sink
// satisfies the parameter; nil (Lite / unwired) leaves markers off.
func (r *Reaper) SetLogSink(s logSink) {
	r.agentLost.sink = s
}

// SetAttemptLifetimeCeiling wires auth.max_attempt_credential_lifetime into
// the agent-lost reaper: a silent attempt that has been running longer than it
// is failed for the credential ceiling, as a task failure under its retry
// policy, instead of as agent_lost, which would re-place it with a fresh
// credential and run it again from the start (#1461). Zero or negative (the
// default when unset) keeps every silent attempt agent_lost, as before.
func (r *Reaper) SetAttemptLifetimeCeiling(d time.Duration) {
	r.agentLost.ceiling = d
}

// SetLeaderSince wires the accessor the settling gate measures its grace from:
// when this instance last acquired scheduler leadership (zero while not
// leading). Measured from leadership acquisition, not process start, so a
// re-election also resets the gate. Nil (no leadership, as in tests) disables
// the gate entirely, since the other two inputs are only meaningful under a
// leader. Lite wires it too: its detached agents survive a restart with a stale
// heartbeat, exactly the signal the grace exists for (#916).
func (r *Reaper) SetLeaderSince(fn func() time.Time) {
	r.leaderSince = fn
}

// SetInformerSynced wires the pod informer's sync predicate into the settling
// gate. Until the cache has synced, the reapers' presence reads fall back to
// live LISTs (safe, but the fleet view a fresh leader is about to judge from is
// still being assembled), so the gate waits. Nil (no informer: Lite, or a
// non-Kubernetes executor) leaves this condition satisfied.
func (r *Reaper) SetInformerSynced(fn func() bool) {
	r.informerSynced = fn
}

// SetLastSweepCompleted wires the reconciler's last-completed-sweep record into
// the settling gate: the gate holds until a sweep has COMPLETED at or after
// leadership was acquired, because that sweep is what recovers the durable
// outcome of a task pod that finished during the outage; before it, "no live
// pod" and "no recent heartbeat" are indistinguishable from a lost task. Nil
// (no reconciler: Lite) leaves this condition satisfied.
func (r *Reaper) SetLastSweepCompleted(fn func() time.Time) {
	r.lastSweepCompleted = fn
}

// settlingVerdict is the settling gate's answer for one tick.
type settlingVerdict struct {
	// hold is true while the leader has not settled and the valve is shut: the
	// tick must do nothing.
	hold bool
	// valve is true when the leader has not settled but the liveness valve has
	// opened: the tick proceeds, loudly.
	valve bool
	// pending names the unsatisfied condition (grace, informer, sweep), for logs.
	pending string
	// elapsed is how long this instance has led.
	elapsed time.Duration
}

// settling is the uniform leader-settling gate: after a (re-)election NO reaper
// may act until the leader has settled, meaning all of
//
//	elapsed since leadership >= SettlingGrace
//	pod informer cache synced          (when wired)
//	a reconciler sweep completed at or after leadership was acquired (when wired)
//
// A control-plane restart manufactures the very signals the reapers act on: a
// stale heartbeat (the receiver was down), a task pod that exited during the
// outage (its terminal report found no server), a run with no recent activity.
// Each reaper used to carry its own post-leadership grace; that closes the race
// per reaper and leaves the class open — the reaper cadence and the reconciler
// cadence were independent clocks, so a grace tuned against one sweep interval
// still allowed zero completed sweeps. One gate over the observable facts (the
// sweep DID complete; the cache IS synced; the fleet HAD a grace to re-heartbeat)
// closes the class for every present and future reaper.
//
// Liveness valve: if the leader has not settled after 2 × SettlingGrace (the
// reconciler cannot list pods, the informer never syncs) the gate opens anyway,
// warning and metering reap_settling_valve_open on every cycle it stays open.
// A permanently held gate would trade "reap wrong" for "never reap": stuck runs
// and lost tasks would accumulate silently, which is the failure the reapers
// exist to prevent. By then the reconciler has had at least four cycles, so the
// diagnosis "the sweep is broken" is real, not a scheduling artifact.
//
// Why opening it is safe — the argument the valve rests on. The dominant reason
// a sweep never completes is that the apiserver cannot be read: unreachable,
// unauthorized, throttled. The reconciler's pod LIST and the reapers' own
// pod-presence LIST hit that same apiserver, so that failure usually denies
// every pod-dependent reaper its authorization too: pod-lost and dispatch-lost
// defer on a query error, and the warm-worker-lost reaper aborts its tick with
// zero marks. They fail closed on their own, without the gate.
//
// But the two reads CAN diverge, and pretending otherwise is how this gate
// would grow a false reputation. RBAC cannot split them (one rule, one verb,
// one resource, and Kubernetes RBAC has no field or label granularity), yet a
// broad namespace-wide LIST can time out where a narrow server-side-filtered
// one succeeds — which is exactly the trigger this whole class was reported
// under — an informer that never syncs needs `watch` where the reapers need
// only `list`, and API Priority and Fairness can starve the heavy request while
// the cheap one gets through. That divergence is precisely why the valve must
// not be the only guard, and why pod-lost reaps on absence alone.
//
// The gate covers the remaining case — the apiserver answers but no sweep has
// yet run under this leadership — and the valve trades that window for not
// wedging the reapers forever.
//
// The valve must therefore never be the only thing standing between a
// recoverable pod and a destructive reap, because it is designed to open. That
// is why the pod-lost reaper reaps only when the attempt has NO pod object at
// all: a pod that is present but finished stays the reconciler's to settle from
// its termination log, valve open or shut. A reap authorized by presence alone
// cannot be manufactured by any grace, cadence or election timing.
//
// The warm-worker-lost reaper is gated too although its signal (a warm pod's
// own liveness, read by a live LIST that fails closed) is not one a restart
// manufactures: delaying it by the grace is harmless — a dead worker's attempts
// are recovered a little later, and warm-pool REFILL is a separate loop that
// is not gated — and one uniform gate is the point.
//
// Nil inputs are satisfied conditions: Lite wires only the leadership stamp, so
// it is held by the grace alone. A zero leadership stamp (not leading) also opens the gate
// here — the destructive gate mayReap owns the not-leading case.
func (r *Reaper) settling(now time.Time) settlingVerdict {
	if r.leaderSince == nil || r.settlingGrace <= 0 {
		return settlingVerdict{}
	}
	since := r.leaderSince()
	if since.IsZero() {
		return settlingVerdict{}
	}
	elapsed := now.Sub(since)
	pending := r.settlingPending(since, elapsed)
	if pending == "" {
		return settlingVerdict{elapsed: elapsed}
	}
	if elapsed >= 2*r.settlingGrace {
		return settlingVerdict{valve: true, pending: pending, elapsed: elapsed}
	}
	return settlingVerdict{hold: true, pending: pending, elapsed: elapsed}
}

// settlingPending names the first unsatisfied settling condition, or "" when
// the leader has settled. Order is cheapest-first; only the name differs.
func (r *Reaper) settlingPending(since time.Time, elapsed time.Duration) string {
	if elapsed < r.settlingGrace {
		return "grace"
	}
	if r.informerSynced != nil && !r.informerSynced() {
		return "informer_unsynced"
	}
	if r.lastSweepCompleted != nil && r.lastSweepCompleted().Before(since) {
		return "no_sweep_since_leadership"
	}
	return ""
}

// ReapOnce runs the five reapers once, in the order the scheduler used to drive
// them: orphan-run, then agent-lost, then dispatch-lost, then pod-lost, then
// warm-worker-lost. The dispatch-lost reaper runs AFTER the orphan-run reaper so
// a clean stuck-queued -> failed -> orphan-run-failed chain can complete in a
// single tick once the thresholds elapse.
//
// The whole tick is skipped — and the skip metered as reap_gate_skip — when the
// destructive gate is closed on entry: a canceled context (SIGTERM drain, the
// step-down cancel fan-out), a scheduler in step-down, or a lost leadership. It
// is likewise skipped, metered as reap_settling_skip, while the leader has not
// settled (see settling); once settling has lasted 2× the grace the liveness
// valve opens and the tick proceeds with a WARN and reap_settling_valve_open.
// The reapers also re-check the destructive gate before each individual write,
// so the early return is the cheap common case, not the only defense.
//
// Each reaper's infra-level list error is logged and metered but never returned:
// the reapers are independent backstops, so one's failure must not block the
// others, and a list error must not stall the caller's cycle. Per-candidate
// failures are already isolated inside each reaper's run. ReapOnce therefore
// always returns nil today; the error return is kept for the seam so a future
// hard-failure mode need not change the caller.
func (r *Reaper) ReapOnce(ctx context.Context) error {
	if !r.mayReap(ctx) {
		r.record("reap_gate_skip")
		return nil
	}
	if v := r.settling(r.now()); v.hold {
		r.record("reap_settling_skip")
		r.logger.Info("reapers holding until the leader settles", "pending", v.pending, "since_leadership", v.elapsed, "grace", r.settlingGrace)
		return nil
	} else if v.valve {
		r.record("reap_settling_valve_open")
		r.logger.Warn("reaper settling valve open: leader never settled, reaping anyway",
			"pending", v.pending, "since_leadership", v.elapsed, "valve_after", 2*r.settlingGrace)
	}
	if err := r.orphan.run(ctx); err != nil {
		r.logError("orphan reaper", err)
		r.record("orphan_list_error")
	}
	if err := r.agentLost.run(ctx); err != nil {
		r.logError("agent-lost reaper", err)
		r.record("agent_lost_list_error")
	}
	if err := r.dispatchLost.run(ctx); err != nil {
		r.logError("dispatch-lost reaper", err)
		r.record("dispatch_lost_list_error")
	}
	if err := r.podLost.run(ctx); err != nil {
		r.logError("pod-lost reaper", err)
		r.record("pod_lost_list_error")
	}
	if err := r.warmWorkerLost.run(ctx); err != nil {
		r.logError("warm-worker-lost reaper", err)
		r.record("warm_worker_lost_list_error")
	}
	return nil
}

// logError mirrors the scheduler's logSchedulerError: downgrade a reaper's error
// to WARN only when it wraps context.Canceled AND the scheduler is currently in a
// graceful leader step-down (the run-context is canceled as the leader releases
// the advisory lock, and every in-flight reaper returns "context canceled"
// milliseconds later — a non-event). Any other error, or a cancel outside a
// known step-down, stays ERROR: the tripwire that catches an unexpected cancel.
func (r *Reaper) logError(msg string, err error) {
	if r.inStepDown != nil && r.inStepDown() && errors.Is(err, context.Canceled) {
		r.logger.Warn(msg+" canceled (leader step-down in progress)", "error", err, "expected", true)
		return
	}
	r.logger.Error(msg, "error", err)
}

func (r *Reaper) record(decision string) {
	if r.rec != nil {
		r.rec.RecordSchedulerDecision(decision)
	}
}
