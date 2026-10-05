package executor

import "context"

// Attempt names exactly one execution of a task instance: its run, its task,
// the try it is on and the attempt epoch the dispatcher claimed for it (ADR 0051
// amendment). The try alone is not enough: an infra re-place, a reschedule poke
// or a repeated dispatch starts a new execution on the same try, and only the
// epoch tells two pods of one try apart. A pod stamped before the epoch existed
// carries no epoch label and is epoch 0.
type Attempt struct {
	RunID        string
	TaskID       string
	TryNumber    int
	AttemptEpoch int
}

// PodManager is the slice of the Kubernetes executor the reapers use to (1)
// tear down a reaped task's pod and (2) check whether a queued TI's pod is
// actually live before declaring its dispatch lost (#474, #461).
//
// Before #474 the reapers only wrote DB state, so a reaped TI's pod kept
// running to completion — breaking at-most-once execution. The reapers now
// call these AFTER the durable DB transition so the pod is actually stopped.
//
// It is nil in Lite/subprocess, where tasks are host processes with no pods.
// Every call site guards the nil: a nil PodManager means "no pods to manage",
// and the dispatch-lost reaper falls back to its pure time-threshold behavior.
//
// The Kubernetes executor implements this; the interface lives here so the
// scheduler depends on a capability, not on the executor package.
type PodManager interface {
	// DeleteTaskPod deletes the pod for exactly one reaped attempt: the
	// (run, task, try, epoch) tuple. Pinning try-number and the attempt epoch
	// guarantees a newer live attempt (a retry on a new try, or an infra
	// re-place on the same try with a new epoch) is never deleted (#901). Each
	// pod is deleted by name with a UID precondition, so a pod the call did not
	// list is never acted on. Tolerates a missing pod.
	//
	// A pod already in a terminal phase is SKIPPED, not deleted (#928): the
	// teardown exists to stop a running container, a terminal pod has none, and
	// its termination message is the outcome record the reconciler settles the
	// attempt from (ADR 0052). That is why a reaper may call this unconditionally
	// after its mark without reading presence first — the guard is at the delete
	// site, so it holds for every reaper including the ones (agent-lost,
	// orphan-run) that read no presence at all.
	DeleteTaskPod(ctx context.Context, a Attempt) error
	// DeleteRunPods deletes every task pod of one reaped run. Used by the
	// orphan-run reaper, which abandons the whole run. The run-id is unique
	// per run, so no other run's pod can match. Tolerates missing pods. The
	// terminal-phase skip above applies per pod within the run (#928).
	DeleteRunPods(ctx context.Context, runID string) error
	// TaskPodPresence reports what the apiserver holds for exactly the
	// (run, task, try, epoch) attempt: a live pod, a present-but-finished pod,
	// or nothing. A reaper defers on a live pod: the dispatch landed, the node
	// is merely slow (#461). Try-number and epoch are pinned so a retried or
	// re-placed TI's liveness gate asks about the attempt it is about to fail,
	// not any older attempt whose pod may still linger (#723).
	TaskPodPresence(ctx context.Context, a Attempt) (PodPresence, error)
}

// PodPresence is the three-way answer to "what does the apiserver hold for this
// attempt's pod?".
//
// The distinction is load-bearing, not cosmetic. A bare bool collapsed the last
// two states, and the pod-lost reaper read that collapse as authorization to
// reap: for a task pod that had already finished it marked the task instance
// pod_lost and then DELETED the pod — destroying the termination-log evidence
// the reconciler recovers the task's durable outcome from. A finished task then
// reads as failed, and its run with it. Presence must be three-valued so a
// reaper can defer on "present but finished" while still reaping on a genuine
// absence, which is the state pod-lost exists for.
type PodPresence int

const (
	// PodPresenceLive means a pod for the attempt exists and is Pending or
	// Running: work may still be happening, so every reaper must defer. It is
	// deliberately the zero value — an unset or error-path presence then
	// authorizes nothing destructive.
	PodPresenceLive PodPresence = iota
	// PodPresenceTerminal means a pod for the attempt exists but none of them is
	// Pending or Running. Whatever happened to that attempt is recorded on the
	// pod object, so settling it belongs to the reconciler and no reaper may
	// delete it. Phase Unknown counts here as well: the pod object is still
	// there and it is not an absence. Note the backstop for a pod stuck in
	// Unknown is NOT the reconciler — classifyPod groups Unknown with
	// Pending/Running, so the reconciler neither settles nor collects it — but
	// the agent-lost reaper (heartbeats stop when a node goes unreachable) and
	// the orphan-run reaper. The phase has not been set by kubelet since 2015
	// and is deprecated upstream, so the practical exposure is nil; classifying
	// it as presence is the conservative direction. Note this is deliberately
	// WIDER than terminalForTeardown, which the pod teardown uses: presence asks
	// "is this an absence?" (Unknown is not), teardown asks "is there a container
	// to stop, and will anything else collect this?" (for Unknown, possibly yes
	// and no) — so Unknown defers a reap here and is still deleted there (#928).
	PodPresenceTerminal
	// PodPresenceAbsent means the apiserver holds no pod for the attempt at all
	// — the attempt's pod is genuinely gone (deleted, evicted, lost with its
	// node) and there is nothing left to settle from. This is the only presence
	// that authorizes a pod-lost reap.
	PodPresenceAbsent
)

// String names the presence for logs.
func (p PodPresence) String() string {
	switch p {
	case PodPresenceLive:
		return "live"
	case PodPresenceTerminal:
		return "terminal"
	case PodPresenceAbsent:
		return "absent"
	default:
		return "invalid"
	}
}

// PodPresenceCache is an optional read-through cache of pod presence — backed by
// a shared informer (PR-10) — that the pod-lost and dispatch-lost reapers consult
// ONLY to DEFER a reap, never to authorize one. Its trust is asymmetric (#461):
//
//   - CachedPodActive == true  => a pod is present and Pending/Running; the reaper
//     may skip the live LIST and defer, because presence is monotonic-safe against
//     cache lag (a pod the cache still shows as live cannot have been gone longer
//     than the lag, and deferring one tick is harmless).
//   - CachedPodActive == false => NOT authoritative. The reaper MUST fall through
//     to the live TaskPodPresence (quorum) read before any destructive action, so a
//     lagged/cold cache can only ever delay a reap by a tick, never cause a
//     false-positive one.
//
// It exists to remove the O(running-TIs)/sec apiserver LIST storm from the read
// path while keeping the kill decision on the live read. Nil in Lite/subprocess
// and before the informer warms: every candidate then uses the live path.
type PodPresenceCache interface {
	// CachedPodActive reports whether the cache holds a Pending/Running pod for
	// exactly the (run, task, try, epoch) attempt. Only a true return is
	// trusted (to defer); false is a "no speedup" signal that must not drive a
	// reap. Try-number and epoch are pinned so the cache gate cannot defer on an
	// older attempt's lingering pod (#723).
	CachedPodActive(a Attempt) bool
}
