package agentrpc

import (
	"errors"
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
	send    chan *agentv1.WorkAssignment
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
}

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
	// leaseFor computes an assignment's ack deadline. Injectable so tests drive
	// reclaim deterministically with a tiny lease.
	leaseFor func(*agentv1.WorkAssignment) time.Duration
	// now is the clock the liveness grace is measured on. Injectable for tests.
	now func() time.Time
}

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
		now: time.Now,
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
		identity:   identity,
		dagVersion: dagVersion,
		podName:    podName,
		send:       send,
		streamDone: streamDone,
		lastSeen:   now,
		superseded: make(chan struct{}),
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

// Assign hands a WorkAssignment to some free worker of dagVersion by pushing it
// onto that worker's outbound channel and starting its lease. It returns false
// when no free worker of that dag_version exists (nothing was handed out and no
// lease was started).
func (r *WorkerRegistry) Assign(dagVersion string, a *agentv1.WorkAssignment) bool {
	r.mu.Lock()
	w := r.takeFreeLocked(dagVersion)
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
// unknown assignment (already expired or already settled) also returns ok=false.
// Only ok=true carries a non-nil binding the handler should persist.
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
// H4 (ADR 0058 N1d-c): a missed ack is usually a transient slow-start, not a
// wedge, so the worker is returned to the free set rather than stranded holding
// a pool slot forever — but only if the registry still points at exactly this
// entry (a reconnect under the same identity installs a fresh entry we must not
// disturb) and it is not busy serving another acked attempt. A genuine wedge is
// bounded later by the D9 lifetime/attempt caps (out of scope here). Done under
// the lock, consistent with the other free-set mutations.
func (r *WorkerRegistry) onLeaseExpire(assignmentID string) {
	r.mu.Lock()
	ls, ok := r.leases[assignmentID]
	if !ok {
		r.mu.Unlock()
		return
	}
	delete(r.leases, assignmentID)
	if w := ls.worker; r.workers[w.identity] == w && !w.busy {
		r.addFreeLocked(w)
	}
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

func (r *WorkerRegistry) takeFreeLocked(dagVersion string) *registeredWorker {
	set := r.free[dagVersion]
	for id, w := range set {
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
