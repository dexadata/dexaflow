package executor

import (
	"fmt"
	"time"
)

// ResilienceLadder is the set of timing knobs whose relative ORDER the
// control-plane-restart recovery depends on. Each is owned by a different
// package (agent, executor, scheduler, server main, operator config) and tuned
// for its own reason, so nothing but this type states that they must line up.
// The invariants:
//
//	heartbeat interval  <  agent-lost threshold  <  settling grace  <  attempt token TTL
//	2 × maintenance interval  <  settling grace
//	longest infra re-place delay  <  orphan threshold
//	task termination grace + 2 × maintenance interval  <  infra confirmation valve  <  orphan threshold
//	attempt token TTL  <  max attempt credential lifetime   (when the ceiling is enabled)
//
// Why each rung matters:
//   - heartbeat < threshold: a healthy agent must beat well inside the silence
//     window or the agent-lost reaper fails live tasks.
//   - threshold < settling grace: after a (re-)election the reapers wait longer
//     than the silence they punish, so the whole fleet can re-heartbeat.
//   - settling grace < token TTL: the grace ends while a re-heartbeat can still
//     authenticate and renew the bearer; otherwise the fleet is unreapable AND
//     unable to report — every in-flight task is lost.
//   - 2×maintenance < settling grace: the leader's maintenance loop runs the
//     reconciler's sweep and then the reapers as ONE ordered cycle, so a reap
//     structurally follows a completed sweep — the settling gate additionally
//     requires that a sweep completed under the new leader. This rung is what
//     makes the grace meaningful on top of that ordering: at least two whole
//     cycles fit inside it, so a settle that failed transiently on the first
//     sweep (a DB hiccup) is retried before the gate can open, and the liveness
//     valve at 2×grace has seen at least four cycles before it declares the
//     sweep broken. A single cycle is not enough: the first tick under a new
//     leader may land anywhere in the interval, so "one interval below the
//     grace" can mean zero completed cycles.
//   - infra re-place delay < orphan threshold: a run whose only live task is
//     parked in its longest infra re-place backoff has no activity to show the
//     orphan-run reaper; the threshold must outlast that parking or the reaper
//     eats a run that is still recovering from the very fault being retried.
//   - termination grace + 2×maintenance < infra confirmation valve (ADR 0052
//     amendment): a reaped pod is stopped in place, its task container exits
//     within the termination grace, and the reconciler needs a sweep after that
//     to read the record and confirm or override the mark. A valve that opens
//     sooner lets the planner re-place on the guess before the evidence could
//     be read. A DAG that declares a longer termination grace gets the valve,
//     not the record, for that task.
//   - infra confirmation valve < orphan threshold: a run whose only live task
//     is a provisional mark shows no activity to the orphan-run reaper.
//   - token TTL < credential lifetime: heartbeat renewal keeps an attempt's
//     bearer alive only while the attempt is younger than the ceiling. A ceiling
//     below the TTL means the first renewal is already refused, the bearer
//     lapses at the TTL, and every task longer than one TTL is unreapable AND
//     unable to report — the restart recovery is silently disabled. This is the
//     ONLY operator-tunable rung; every other value is a build-time constant.
//
// Warm pools share the same ladder: a warm worker's per-attempt bearer is renewed
// by the same heartbeat under the same ceiling, and the warm-worker-lost reaper
// reuses the pod-lost mark, so no warm-specific rung exists.
type ResilienceLadder struct {
	HeartbeatInterval  time.Duration
	AgentLostThreshold time.Duration
	// SettlingGrace is the post-leadership window during which no reaper fires
	// (ReaperConfig.SettlingGrace); the one grace every reaper shares.
	SettlingGrace   time.Duration
	AttemptTokenTTL time.Duration
	// ReconcileInterval is the period of the leader's maintenance loop: one
	// reconciler sweep followed by one reaper pass per cycle.
	ReconcileInterval time.Duration
	// OrphanThreshold is how long a running dag run may sit with no activity
	// before the orphan-run reaper fails it (ReaperConfig.OrphanThreshold).
	OrphanThreshold time.Duration
	// InfraReplaceMaxDelay is the longest the scheduler may park an infra-failed
	// task before re-placing it: the backoff before the final permitted re-place
	// plus the whole de-synchronizing jitter window. The scheduler owns and
	// computes it; it is passed in because the scheduler package depends on this
	// one, so the validator cannot read it directly.
	InfraReplaceMaxDelay time.Duration
	// MaxAttemptCredentialLifetime is the operator's auth.max_attempt_credential_lifetime:
	// the ceiling on how long heartbeat renewal keeps an attempt's bearer alive.
	// A non-positive value is the documented "no ceiling" setting; the rung that
	// depends on it is then trivially satisfied and skipped.
	MaxAttemptCredentialLifetime time.Duration
	// TaskTerminationGrace is the default termination grace of a task pod: how
	// long a pod stopped in place may take for its task container to exit.
	TaskTerminationGrace time.Duration
	// InfraConfirmMaxWait is the scheduler's infra confirmation valve
	// (scheduler.InfraConfirmMaxWait), passed in for the same reason as
	// InfraReplaceMaxDelay.
	InfraConfirmMaxWait time.Duration
}

// maxAttemptCredentialLifetimeKey is the config key of the one operator-tunable
// rung, so the boot error points the operator at the knob that has to move.
const maxAttemptCredentialLifetimeKey = "auth.max_attempt_credential_lifetime"

// ladderRung names one knob (or a fixed multiple of one) for error messages.
type ladderRung struct {
	name string
	d    time.Duration
}

// ladderRelation is one strict ordering lo < hi. operator marks the relation's
// hi side as an operator-tunable config value: the error then names the config
// key; otherwise every value is a build-time constant and the only remedy is a
// code change, so the error asks for a bug report.
type ladderRelation struct {
	lo, hi   ladderRung
	why      string
	operator bool
}

// ValidateResilienceLadder checks the orderings the restart recovery depends on
// (see ResilienceLadder) and reports the first violated relation, naming both
// sides with their values. A relation between build-time constants can only be
// broken by a code change, so its error says so and asks for a bug report; the
// one relation involving an operator knob names the config key. All build-time
// rungs must be positive. It is pure — the server calls it once at boot and
// refuses to start on an error, turning what used to be a comment-level
// convention into an enforced invariant.
func ValidateResilienceLadder(l ResilienceLadder) error {
	hb := ladderRung{"heartbeat interval", l.HeartbeatInterval}
	thr := ladderRung{"agent-lost threshold", l.AgentLostThreshold}
	grace := ladderRung{"settling grace", l.SettlingGrace}
	ttl := ladderRung{"attempt token TTL", l.AttemptTokenTTL}
	rec := ladderRung{"maintenance interval", l.ReconcileInterval}
	orphan := ladderRung{"orphan threshold", l.OrphanThreshold}
	replace := ladderRung{"longest infra re-place delay", l.InfraReplaceMaxDelay}
	termGrace := ladderRung{"task termination grace", l.TaskTerminationGrace}
	valve := ladderRung{"infra confirmation valve", l.InfraConfirmMaxWait}

	for _, r := range []ladderRung{hb, thr, grace, ttl, rec, orphan, replace, termGrace, valve} {
		if r.d <= 0 {
			return fmt.Errorf("resilience ladder: %s (%v) must be positive", r.name, r.d)
		}
	}
	twoRec := ladderRung{"2 × maintenance interval", 2 * l.ReconcileInterval}
	relations := []ladderRelation{
		{lo: hb, hi: thr, why: "a live agent must heartbeat well inside the agent-lost silence window"},
		{lo: thr, hi: grace, why: "the post-leadership settling grace must outlast the silence it forgives so the fleet can re-heartbeat"},
		{lo: grace, hi: ttl, why: "a re-heartbeat must still authenticate and renew the bearer when the grace ends"},
		{lo: twoRec, hi: grace, why: "at least two reconcile-then-reap cycles must complete under a new leader before the settling gate may open, so a transiently failed settle is retried before any reaper acts"},
		{lo: replace, hi: orphan, why: "a run parked in its longest infra re-place backoff must not be reaped as orphaned while it is still recovering"},
		{
			lo:  ladderRung{"task termination grace + 2 × maintenance interval", l.TaskTerminationGrace + 2*l.ReconcileInterval},
			hi:  valve,
			why: "a stopped task pod's container must exit and a reconcile sweep must read its record before the planner may act on the infra guess",
		},
		{lo: valve, hi: orphan, why: "a run whose only live task is a provisional infra mark must not be reaped as orphaned while the mark awaits confirmation"},
	}
	if l.MaxAttemptCredentialLifetime > 0 {
		ceiling := ladderRung{maxAttemptCredentialLifetimeKey, l.MaxAttemptCredentialLifetime}
		relations = append(relations, ladderRelation{
			lo: ttl, hi: ceiling, operator: true,
			why: "heartbeat renewal must be able to extend the bearer at least once, or every task longer than one TTL loses its credential and the restart recovery is disabled",
		})
	}
	for _, rel := range relations {
		if rel.lo.d < rel.hi.d {
			continue
		}
		if rel.operator {
			return fmt.Errorf("resilience ladder: %s (%v) must be < %s (%v): %s — raise %s",
				rel.lo.name, rel.lo.d, rel.hi.name, rel.hi.d, rel.why, rel.hi.name)
		}
		return fmt.Errorf("resilience ladder: %s (%v) must be < %s (%v): %s — build-time invariant violated, please file a bug",
			rel.lo.name, rel.lo.d, rel.hi.name, rel.hi.d, rel.why)
	}
	return nil
}

// ResilienceLadderWarnings reports the ladder settings that are valid but
// remove a resilience backstop, so the server can surface them as boot WARNs.
// ValidateResilienceLadder deliberately accepts a non-positive credential
// ceiling — it is the operator's documented "no ceiling" setting — but that one
// value disables every wall-clock bound the ceiling carries: heartbeat renewal
// of an attempt's bearer becomes unbounded; a dedicated task pod whose DAG
// declares no execution timeout gets no ActiveDeadlineSeconds floor; and, with
// warm pools enabled, the per-attempt watchdog that keeps a wedged attempt from
// pinning a warm slot is off too (a warm pod has no pod-level deadline at all,
// and the worker lifetime cap drains between attempts, never mid-attempt). A
// task that wedges while still heartbeating then has no bound of its own even
// with a healthy control plane: the orphan-run reaper skips a run with a live
// task instance and agent-lost never fires on a live agent. None of these
// losses is an error; all are invisible without this signal. Pure, like the
// validator: the server calls it once at boot after the logger exists.
func ResilienceLadderWarnings(l ResilienceLadder) []LadderWarning {
	var warnings []LadderWarning
	if l.MaxAttemptCredentialLifetime <= 0 {
		warnings = append(warnings, LadderWarning{
			Msg: fmt.Sprintf("%s is disabled (%v): heartbeat renewal of an attempt's credential is unbounded, task pods with no declared execution_timeout get no activeDeadlineSeconds floor, and with warm pools enabled the per-attempt watchdog is off — a wedged or partitioned task pod has no wall-clock bound of its own",
				maxAttemptCredentialLifetimeKey, l.MaxAttemptCredentialLifetime),
			Key:   maxAttemptCredentialLifetimeKey,
			Value: l.MaxAttemptCredentialLifetime,
		})
	}
	return warnings
}

// LadderWarning is one boot WARN about a ladder setting that is valid but
// removes a resilience backstop: the operator-readable sentence plus the two
// things a monitoring rule needs as FIELDS. Logging only a pre-formatted string
// leaves the JSON record with nothing but msg, so the single alert an operator
// would actually want — "some instance booted with the credential ceiling
// disabled" — can only be written as a substring match on prose that a later
// reword silently breaks (#924). Msg stays self-contained (it names the key and
// the value too) so a plain-text log needs no field expansion to be read.
// Every rung of the ladder is a duration, so Value is one.
type LadderWarning struct {
	// Msg is the human-readable sentence: what is off and what it costs.
	Msg string
	// Key is the config key an operator would set to restore the backstop —
	// the attribute an alert rule matches on.
	Key string
	// Value is the offending setting as configured. slog's JSON handler renders
	// a duration as an integer nanosecond count, so an alert rule matches it
	// numerically (value <= 0 for a disabled ceiling), not as "0s" — that
	// spelling is the text handler's.
	Value time.Duration
}
