package agentrpc

import (
	"errors"
	"log/slog"
	"sync"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// ReclaimReason names why an assignment was reclaimed (ADR 0058 N1b, H1).
type ReclaimReason int

const (
	// ReclaimLeaseExpired means the worker did not ack the assignment as started
	// before its lease elapsed.
	ReclaimLeaseExpired ReclaimReason = iota
	// ReclaimRefused means the worker acked with started=false (it cannot or will
	// not run it).
	ReclaimRefused
	// ReclaimWorkerGone means the worker's stream ended while it held an unacked
	// assignment (a disconnected worker can never ack it).
	ReclaimWorkerGone
)

// ReclaimEvent is emitted when a handed-out assignment must be re-placed. The
// placement layer consumes it to re-dispatch the attempt on the infra budget. It
// carries the attempt identity (RunID, TaskID, TryNumber) — populated from the
// leaseState at every emit site — so the observer can re-place the exact attempt
// (ADR 0058 N1d-c, H2) without re-deriving it. It deliberately carries no
// attempt_token.
type ReclaimEvent struct {
	AssignmentID string
	DagVersionID string
	Reason       ReclaimReason
	RunID        string
	TaskID       string
	TryNumber    int
	// AttemptEpoch is the execution of TryNumber the assignment was dispatched as
	// (ADR 0051 amendment), so the re-place is fenced to exactly that attempt.
	AttemptEpoch int
}

// WarmBinding is the durable binding an ack (started=true) establishes: the warm
// worker pod (PodName) now serving a specific attempt (RunID, TaskID, TryNumber).
// The handler persists it (BindWarmAttempt) so a later failover reaper can match
// bound attempts against the live warm-pod set (ADR 0058 N1d-a1). PodName is the
// worker's own downward-API pod name it sent in WorkerRegister — the reaper's join
// key against ListWarmPods — NOT the registry's authenticated identity.
type WarmBinding struct {
	RunID     string
	TaskID    string
	TryNumber int
	// AttemptEpoch is the execution of TryNumber this binding is for (ADR 0051
	// amendment), so an ack never binds a worker onto a later attempt.
	AttemptEpoch int
	PodName      string
}

// registeredWorker is one warm worker's registry entry. send is the handler's
// outbound assignment channel; the handler owns Send()ing what lands on it.
type registeredWorker struct {
	identity   string
	dagVersion string
	// podName is the worker's OWN Kubernetes pod name (WorkerRegister.pod_name,
	// sourced from the downward API). It is the durable key a started attempt is
	// bound to; distinct from identity, which is the authenticated stream credential.
	podName string
	// sourceDelivery is the worker's WorkerRegister.source_delivery: it runs a
	// source-mode attempt from the dag_source its assignment carries (ADR
	// 0095). Only such a worker is handed a source-mode assignment.
	sourceDelivery bool
	// warnedNoSource records that this entry was already reported as passed
	// over for a source-mode attempt, so the warning comes once per worker.
	warnedNoSource bool
	send           chan *agentv1.WorkAssignment
	// busy is set once the worker acks an assignment as started; it is cleared
	// when the worker signals SlotFree.
	busy bool
	// streamDone is closed when the stream that registered this entry has ended
	// (the handler passes its stream context's Done channel). nil means the
	// registry has no such signal and judges the entry by heartbeats alone.
	streamDone <-chan struct{}
	// lastSeen is when this entry's stream last showed it was alive: its
	// registration, or any later worker message (Touch).
	lastSeen time.Time
	// superseded is closed when a new registration replaces this entry because
	// it went stale, so the handler serving the stale stream ends it instead of
	// keeping a second connection open for the same identity.
	superseded chan struct{}
}

// workerLivenessGrace is how long a registered worker's stream may stay silent
// before a new registration under the same identity may replace it. A warm
// worker sends a heartbeat on its assignment stream every 15 seconds (the
// agent's DefaultHeartbeatInterval, reused for the stream heartbeat), so the
// grace is four missed heartbeats: long enough that a slow or briefly stalled
// worker is never displaced, short enough that a stream that is wedged but not
// yet torn down by the transport does not lock its identity out for long. It
// only matters while the old stream is still open: a reconnect after the old
// stream has ended is accepted at once.
const workerLivenessGrace = 60 * time.Second

// ErrWorkerAlreadyRegistered is returned by Register when the identity already
// has a live registration: its stream is still connected and has heartbeated
// within workerLivenessGrace.
var ErrWorkerAlreadyRegistered = errors.New("warm worker identity already has a live registration")

// leaseState tracks one in-flight assignment awaiting an ack. It carries the
// assignment's attempt identity (runID, taskID, tryNumber) so a started ack can
// return the WarmBinding to persist without the handler re-deriving it.
type leaseState struct {
	worker     *registeredWorker
	dagVersion string
	runID      string
	taskID     string
	tryNumber  int
	// attemptEpoch is the epoch the dispatcher claimed for this assignment.
	attemptEpoch int
	timer        *time.Timer
	// expired is set once the ack deadline passed with no ack. The lease then
	// stays as a tombstone for leaseTombstone (see onLeaseExpire): the worker
	// may still ack it late and run the attempt, so it is neither offered new
	// work nor forgotten while that ack can arrive.
	expired bool
}

// leaseTombstone is how long an expired lease is remembered after its ack
// deadline. A worker acks after resetting its scratch and runs the attempt
// whatever the server answers, so an ack can arrive after the lease expired. The
// tombstone lets that late started ack still bind the attempt and mark the worker
// busy, and keeps the worker out of the free set meanwhile, so it is never handed
// a second assignment while it may be running the first. Five minutes is well
// past the dispatch-lost threshold (3 minutes): by then the attempt has been
// recovered by the reaper and a later ack is fenced by the attempt epoch.
const leaseTombstone = 5 * time.Minute

// WorkerRegistry is the concurrency-safe home of the warm-worker fleet and the
// H1 ack/lease machine (ADR 0058 N1b).
//
// Data structures and complexity:
//   - workers: identity -> entry. Register/Deregister/MarkFree are O(1).
//   - free:    dag_version -> (identity -> entry). Assign grabs one free worker
//     of a dag_version in O(1) (a single map-range that breaks on the first
//     element), then removes it in O(1).
//   - leases:  assignment_id -> in-flight lease. Ack and lease-expiry are O(1).
type WorkerRegistry struct {
	mu      sync.Mutex
	workers map[string]*registeredWorker
	free    map[string]map[string]*registeredWorker
	leases  map[string]*leaseState

	onReclaim func(ReclaimEvent)
	// onClaim, when set, runs after a started ack's binding is persisted, so the
	// pool reconciler can refill the idle buffer at once (event-driven refill).
	onClaim func()
	// leaseFor computes an assignment's ack deadline. Injectable so tests drive
	// reclaim deterministically with a tiny lease.
	leaseFor func(*agentv1.WorkAssignment) time.Duration
	// tombstoneFor is how long an expired lease is kept (leaseTombstone).
	// Injectable so tests drive the tombstone deterministically.
	tombstoneFor time.Duration
	// now is the clock the liveness grace is measured on. Injectable for tests.
	now func() time.Time
	// sweepPolicy is what a registration that does not confirm the /tmp and
	// /dev/shm sweep gets (#1678); sweepDecisions meters each one.
	sweepPolicy    SweepPolicy
	sweepDecisions DecisionRecorder
}

// DecisionRecorder meters a control-plane decision by type. The production
// implementation is observability.Metrics (dexaflow_scheduler_decisions_total).
type DecisionRecorder interface {
	RecordSchedulerDecision(decisionType string)
}

// SweepPolicy is how the registry treats a warm worker whose register does not
// confirm attempt_home_sweep (#1678).
type SweepPolicy int

const (
	// SweepIgnore accepts every worker without looking (the read-only root is
	// off, so no sweep is expected).
	SweepIgnore SweepPolicy = iota
	// SweepWarn accepts the worker, logs a warning and meters it.
	SweepWarn
	// SweepRefuse refuses the registration, logs and meters it, so the worker
	// never joins the pool and its DAG version's attempts run on dedicated pods.
	SweepRefuse
)

// DecisionWarmWorkerSweepUnconfirmed is the decision type metered when a warm
// worker that does not confirm the attempt home sweep is accepted (SweepWarn).
const DecisionWarmWorkerSweepUnconfirmed = "warm_worker_sweep_unconfirmed"

// DecisionWarmWorkerRefusedNoSweep is the decision type metered when a warm
// worker is refused because it does not confirm the attempt home sweep
// (SweepRefuse).
const DecisionWarmWorkerRefusedNoSweep = "warm_worker_refused_no_sweep"

// DecisionWarmWorkerNoSourceDelivery is the decision type metered, once per
// worker, when a source-mode attempt passes over a free warm worker that did
// not register with source_delivery (ADR 0095): its agent predates it, and
// the attempt takes a dedicated pod unless another worker can run it.
const DecisionWarmWorkerNoSourceDelivery = "warm_worker_no_source_delivery"

// ReasonAttemptHomeSweepUnconfirmed is the stable reason logged for a warm
// worker that does not confirm the sweep, and carried by its refusal.
const ReasonAttemptHomeSweepUnconfirmed = "attempt_home_sweep_unconfirmed"

// NewWorkerRegistry builds a registry whose reclaim events are delivered to
// onReclaim (may be nil).
func NewWorkerRegistry(onReclaim func(ReclaimEvent)) *WorkerRegistry {
	return &WorkerRegistry{
		workers:   map[string]*registeredWorker{},
		free:      map[string]map[string]*registeredWorker{},
		leases:    map[string]*leaseState{},
		onReclaim: onReclaim,
		leaseFor: func(a *agentv1.WorkAssignment) time.Duration {
			return time.Duration(a.GetLeaseSeconds()) * time.Second
		},
		tombstoneFor: leaseTombstone,
		now:          time.Now,
	}
}

// SetOnClaim sets the hook run each time a warm worker is claimed by a started
// attempt (after the binding that makes it count as busy is persisted). It must
// not block; nil disables it. Safe to call while the registry serves workers.
func (r *WorkerRegistry) SetOnClaim(fn func()) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.onClaim = fn
}

// SetSweepPolicy sets how the registry treats a warm worker whose register
// does not confirm attempt_home_sweep (#1678). With
// execution.warm_read_only_root_filesystem on, isolation between attempts
// relies on the agent in the task image to empty /tmp and /dev/shm, and an
// agent built on an older runtime silently does not. rec (may be nil) meters
// each unconfirmed worker. Call it before the registry serves workers.
func (r *WorkerRegistry) SetSweepPolicy(p SweepPolicy, rec DecisionRecorder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.sweepPolicy = p
	r.sweepDecisions = rec
}

// SweepPolicy reports the policy SetSweepPolicy set (SweepIgnore by default).
func (r *WorkerRegistry) SweepPolicy() SweepPolicy {
	p, _ := r.sweepPolicyAndRecorder()
	return p
}

// sweepPolicyAndRecorder reports the sweep policy and the recorder its
// decisions are metered on.
func (r *WorkerRegistry) sweepPolicyAndRecorder() (SweepPolicy, DecisionRecorder) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.sweepPolicy, r.sweepDecisions
}

// claimed runs the onClaim hook, if any, outside the registry lock.
func (r *WorkerRegistry) claimed() {
	r.mu.Lock()
	fn := r.onClaim
	r.mu.Unlock()
	if fn != nil {
		fn()
	}
}

// Register records a warm worker under its authenticated identity, ready to take
// work for dagVersion. podName is the worker's own pod name, carried so a started
// ack can bind the attempt to it. streamDone is closed when the registering
// stream ends (nil when the caller has no such signal). It returns the entry so
// the caller can Deregister exactly the entry it created.
//
// One live registration per identity: while an earlier registration's stream is
// still connected and has heartbeated within workerLivenessGrace, a second
// Register is refused with ErrWorkerAlreadyRegistered and the live entry is left
// untouched. A reconnect is accepted once the earlier stream has ended (its
// streamDone is closed, or it already deregistered), or once it has been silent
// for longer than the grace; in the stale case the old entry's superseded
// channel is closed so its handler ends. The fresh entry starts free.
func (r *WorkerRegistry) Register(identity, dagVersion, podName string, send chan *agentv1.WorkAssignment, streamDone <-chan struct{}) (*registeredWorker, error) {
	return r.RegisterWorker(identity, dagVersion, WorkerInfo{PodName: podName}, send, streamDone)
}

// WorkerInfo is what a warm worker's registration says about it besides its
// identity and pool: the locator a started attempt is bound to, and what it
// can run.
type WorkerInfo struct {
	// PodName is WorkerRegister.pod_name.
	PodName string
	// SourceDelivery is WorkerRegister.source_delivery (ADR 0095).
	SourceDelivery bool
}

// RegisterWorker is Register with everything the registration states about
// the worker.
func (r *WorkerRegistry) RegisterWorker(identity, dagVersion string, info WorkerInfo, send chan *agentv1.WorkAssignment, streamDone <-chan struct{}) (*registeredWorker, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	if old, ok := r.workers[identity]; ok {
		if r.liveLocked(old, now) {
			return nil, ErrWorkerAlreadyRegistered
		}
		// Drop the superseded entry from the free set; any lease still bound to it
		// keeps its own timer and reclaims independently.
		r.removeFreeLocked(old)
		close(old.superseded)
	}
	w := &registeredWorker{
		identity:       identity,
		dagVersion:     dagVersion,
		podName:        info.PodName,
		sourceDelivery: info.SourceDelivery,
		send:           send,
		streamDone:     streamDone,
		lastSeen:       now,
		superseded:     make(chan struct{}),
	}
	r.workers[identity] = w
	r.addFreeLocked(w)
	return w, nil
}

// Touch records that w's stream is alive (any worker message, including the
// periodic heartbeat). It is a no-op unless the registry still points at exactly
// this entry, so a superseded stream cannot keep its replacement alive.
func (r *WorkerRegistry) Touch(w *registeredWorker) {
	if w == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers[w.identity] == w {
		w.lastSeen = r.now()
	}
}

// liveLocked reports whether w still holds its identity's registration: its
// stream has not ended and it was seen within workerLivenessGrace. Requires r.mu.
func (r *WorkerRegistry) liveLocked(w *registeredWorker, now time.Time) bool {
	if w.streamDone != nil {
		select {
		case <-w.streamDone:
			return false
		default:
		}
	}
	return now.Sub(w.lastSeen) <= workerLivenessGrace
}

// Deregister removes a worker's entry, but only if the registry still points at
// exactly this entry — a reconnect under the same identity installs a new entry,
// and the stale stream's later Deregister must not evict the live one. Any
// in-flight leases the worker still held are reclaimed: a gone worker can never
// ack them.
func (r *WorkerRegistry) Deregister(w *registeredWorker) {
	if w == nil {
		return
	}
	r.mu.Lock()
	if r.workers[w.identity] != w {
		r.mu.Unlock()
		return
	}
	delete(r.workers, w.identity)
	r.removeFreeLocked(w)
	var reclaims []ReclaimEvent
	for aid, ls := range r.leases {
		if ls.worker == w {
			ls.timer.Stop()
			delete(r.leases, aid)
			reclaims = append(reclaims, ReclaimEvent{
				AssignmentID: aid,
				DagVersionID: ls.dagVersion,
				Reason:       ReclaimWorkerGone,
				RunID:        ls.runID,
				TaskID:       ls.taskID,
				TryNumber:    ls.tryNumber,
				AttemptEpoch: ls.attemptEpoch,
			})
		}
	}
	r.mu.Unlock()
	for _, ev := range reclaims {
		r.emitReclaim(ev)
	}
}

// retireIfIdle takes w off the free set and reports true when it is idle: still
// this identity's entry, not busy with an acked attempt, and holding no
// assignment that awaits its ack. An expired lease kept as a tombstone counts as
// awaiting its ack: the worker may still ack it late and run it (#1600). It
// reports false, changing nothing, for a
// worker that is serving or about to serve. The caller ends the worker's stream
// on true, so taking it off the free set in the same step keeps an assignment
// from being handed to a stream that is closing.
func (r *WorkerRegistry) retireIfIdle(w *registeredWorker) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.workers[w.identity] != w || w.busy {
		return false
	}
	for _, ls := range r.leases {
		if ls.worker == w {
			return false
		}
	}
	r.removeFreeLocked(w)
	return true
}

// Assign hands a WorkAssignment to some free worker of dagVersion by pushing it
// onto that worker's outbound channel and starting its lease. It returns false
// when no free worker of that dag_version exists (nothing was handed out and no
// lease was started).
func (r *WorkerRegistry) Assign(dagVersion string, a *agentv1.WorkAssignment) bool {
	r.mu.Lock()
	w := r.takeFreeLocked(dagVersion, len(a.GetDagSource()) > 0)
	if w == nil {
		r.mu.Unlock()
		return false
	}
	// The worker was just removed from the free set, so nothing else races for
	// its channel; a non-blocking send cannot lose the assignment unless the
	// channel is already occupied (a caller Assigned twice without an ack). In
	// that case put it back and refuse.
	select {
	case w.send <- a:
	default:
		r.addFreeLocked(w)
		r.mu.Unlock()
		return false
	}
	aid := a.GetAssignmentId()
	ls := &leaseState{
		worker:       w,
		dagVersion:   dagVersion,
		runID:        a.GetDagRunId(),
		taskID:       a.GetTaskId(),
		tryNumber:    int(a.GetTryNumber()),
		attemptEpoch: int(a.GetAttemptEpoch()),
	}
	ls.timer = time.AfterFunc(r.leaseFor(a), func() { r.onLeaseExpire(aid) })
	r.leases[aid] = ls
	r.mu.Unlock()
	return true
}

// Ack settles the lease for assignmentID. started=true marks the worker busy,
// cancels the lease (no reclaim), and returns the WarmBinding to persist — the
// acked attempt's (run, task, try) plus the worker's pod name — with ok=true.
// started=false reclaims the assignment and returns ok=false. An ack for an
// unknown assignment (already settled, or expired past its tombstone) also
// returns ok=false. Only ok=true carries a non-nil binding the handler should
// persist.
//
// A late ack, one that arrives after the lease expired but while its tombstone
// is kept, is settled the same way: the worker runs the attempt whatever the
// server answers, so a started late ack still binds the attempt and marks the
// worker busy. A refused late ack returns the worker to the free set, which the
// expiry deliberately did not do.
func (r *WorkerRegistry) Ack(assignmentID string, started bool) (*WarmBinding, bool) {
	r.mu.Lock()
	ls, ok := r.leases[assignmentID]
	if !ok {
		r.mu.Unlock()
		return nil, false
	}
	delete(r.leases, assignmentID)
	ls.timer.Stop()
	if started {
		ls.worker.busy = true
		// A late started ack may land after a reconnect replaced this entry; the
		// busy bit then sits on the stale entry and the new one is unaffected.
		binding := &WarmBinding{
			RunID:        ls.runID,
			TaskID:       ls.taskID,
			TryNumber:    ls.tryNumber,
			AttemptEpoch: ls.attemptEpoch,
			PodName:      ls.worker.podName,
		}
		r.mu.Unlock()
		return binding, true
	}
	if ls.expired {
		r.returnFreeLocked(ls.worker)
	}
	ev := ReclaimEvent{
		AssignmentID: assignmentID,
		DagVersionID: ls.dagVersion,
		Reason:       ReclaimRefused,
		RunID:        ls.runID,
		TaskID:       ls.taskID,
		TryNumber:    ls.tryNumber,
		AttemptEpoch: ls.attemptEpoch,
	}
	r.mu.Unlock()
	r.emitReclaim(ev)
	return nil, false
}

// MarkFree records a worker's SlotFree signal: it clears busy and returns the
// worker to the free set so it can take new work. Unknown identities are ignored.
func (r *WorkerRegistry) MarkFree(identity string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[identity]
	if !ok {
		return
	}
	w.busy = false
	r.addFreeLocked(w)
}

// onLeaseExpire reclaims an assignment whose lease elapsed with no ack. If the
// lease was already settled (acked or the worker deregistered), it is a no-op.
//
// The worker is NOT returned to the free set here: it acks after a scratch reset
// and then runs the attempt regardless of the server's answer, so a slow worker
// can still ack and run this attempt. Offering it new work now would push a
// second assignment onto a worker that is busy with the first (#1600). The lease
// is kept as a tombstone instead (expired=true) so a late started ack still binds
// the attempt (Ack). When the tombstone elapses with no ack, onTombstoneExpire
// returns the worker to the free set (H4, ADR 0058 N1d-c): a missed ack is
// usually a transient slow start, not a wedge, and the worker must not stay
// stranded holding a pool slot forever.
func (r *WorkerRegistry) onLeaseExpire(assignmentID string) {
	r.mu.Lock()
	ls, ok := r.leases[assignmentID]
	if !ok || ls.expired {
		r.mu.Unlock()
		return
	}
	ls.expired = true
	ls.timer = time.AfterFunc(r.tombstoneFor, func() { r.onTombstoneExpire(assignmentID, ls) })
	ev := ReclaimEvent{
		AssignmentID: assignmentID,
		DagVersionID: ls.dagVersion,
		Reason:       ReclaimLeaseExpired,
		RunID:        ls.runID,
		TaskID:       ls.taskID,
		TryNumber:    ls.tryNumber,
		AttemptEpoch: ls.attemptEpoch,
	}
	r.mu.Unlock()
	r.emitReclaim(ev)
}

// onTombstoneExpire forgets an expired lease whose late ack never came and
// returns its worker to the free set (H4). It is a no-op if the lease was settled
// meanwhile (a late ack, or the worker deregistered) or replaced. No reclaim is
// emitted: the lease expiry already emitted ReclaimLeaseExpired.
func (r *WorkerRegistry) onTombstoneExpire(assignmentID string, ls *leaseState) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.leases[assignmentID] != ls {
		return
	}
	delete(r.leases, assignmentID)
	r.returnFreeLocked(ls.worker)
}

// returnFreeLocked puts w back in the free set, but only if the registry still
// points at exactly this entry (a reconnect under the same identity installs a
// fresh entry we must not disturb) and it is not busy serving an acked attempt.
// Requires r.mu.
func (r *WorkerRegistry) returnFreeLocked(w *registeredWorker) {
	if r.workers[w.identity] == w && !w.busy {
		r.addFreeLocked(w)
	}
}

// emitReclaim delivers a reclaim event to the observer, always OUTSIDE the lock
// so the observer may re-enter the registry without deadlocking.
func (r *WorkerRegistry) emitReclaim(ev ReclaimEvent) {
	if r.onReclaim != nil {
		r.onReclaim(ev)
	}
}

// ── free-set helpers (all require r.mu held) ────────────────────────────────

func (r *WorkerRegistry) addFreeLocked(w *registeredWorker) {
	set := r.free[w.dagVersion]
	if set == nil {
		set = map[string]*registeredWorker{}
		r.free[w.dagVersion] = set
	}
	set[w.identity] = w
}

func (r *WorkerRegistry) removeFreeLocked(w *registeredWorker) {
	set := r.free[w.dagVersion]
	if set == nil {
		return
	}
	delete(set, w.identity)
	if len(set) == 0 {
		delete(r.free, w.dagVersion)
	}
}

// warnNoSourceDeliveryLocked logs and meters, once per worker entry, that a
// source-mode attempt passed w over.
func (r *WorkerRegistry) warnNoSourceDeliveryLocked(w *registeredWorker) {
	if w.warnedNoSource {
		return
	}
	w.warnedNoSource = true
	slog.Warn("warm worker passed over for a source-mode attempt: its agent does not confirm source delivery; rebuild the source mode runtime image on a runtime from 0.5.7 or later",
		"reason", DecisionWarmWorkerNoSourceDelivery, "identity", w.identity, "dag_version", w.dagVersion, "pod_name", w.podName)
	if r.sweepDecisions != nil {
		r.sweepDecisions.RecordSchedulerDecision(DecisionWarmWorkerNoSourceDelivery)
	}
}

// takeFreeLocked removes and returns a free worker of dagVersion, nil when
// none is free. With needSource only a worker that registered with
// source_delivery qualifies (ADR 0095): an older worker would run a
// source-mode attempt without its dag.py.
func (r *WorkerRegistry) takeFreeLocked(dagVersion string, needSource bool) *registeredWorker {
	set := r.free[dagVersion]
	for id, w := range set {
		if needSource && !w.sourceDelivery {
			r.warnNoSourceDeliveryLocked(w)
			continue
		}
		delete(set, id)
		if len(set) == 0 {
			delete(r.free, dagVersion)
		}
		return w
	}
	return nil
}

// ── test/introspection helpers ──────────────────────────────────────────────

func (r *WorkerRegistry) registered(identity string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, ok := r.workers[identity]
	return ok
}

func (r *WorkerRegistry) size() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.workers)
}

func (r *WorkerRegistry) busy(identity string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w, ok := r.workers[identity]
	return ok && w.busy
}

func (r *WorkerRegistry) dagVersionOf(identity string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[identity]; ok {
		return w.dagVersion
	}
	return ""
}

func (r *WorkerRegistry) lastSeenOf(identity string) time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[identity]; ok {
		return w.lastSeen
	}
	return time.Time{}
}

func (r *WorkerRegistry) podNameOf(identity string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if w, ok := r.workers[identity]; ok {
		return w.podName
	}
	return ""
}
