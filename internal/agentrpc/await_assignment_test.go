package agentrpc

import (
	"context"
	"io"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// ─── test doubles ───────────────────────────────────────────────────────────

// fakeAwaitStream is an in-memory bidi AwaitAssignment server stream. The test
// pushes WorkerMessages (and terminal errors) onto recv; assignments the handler
// Sends land on sent.
type fakeAwaitStream struct {
	grpc.ServerStream
	ctx  context.Context
	recv chan recvItem
	sent chan *agentv1.WorkAssignment
}

type recvItem struct {
	m   *agentv1.WorkerMessage
	err error
}

func newFakeAwaitStream(ctx context.Context) *fakeAwaitStream {
	return &fakeAwaitStream{
		ctx:  ctx,
		recv: make(chan recvItem, 16),
		sent: make(chan *agentv1.WorkAssignment, 16),
	}
}

func (s *fakeAwaitStream) Context() context.Context { return s.ctx }

func (s *fakeAwaitStream) Recv() (*agentv1.WorkerMessage, error) {
	it := <-s.recv
	return it.m, it.err
}

func (s *fakeAwaitStream) Send(a *agentv1.WorkAssignment) error {
	s.sent <- a
	return nil
}

func (s *fakeAwaitStream) pushMsg(m *agentv1.WorkerMessage) { s.recv <- recvItem{m: m} }
func (s *fakeAwaitStream) pushErr(err error)                { s.recv <- recvItem{err: err} }

func regMsg(dagVersion string) *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Register{
		Register: &agentv1.WorkerRegister{DagVersionId: dagVersion},
	}}
}

func regMsgPod(dagVersion, podName string) *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Register{
		Register: &agentv1.WorkerRegister{DagVersionId: dagVersion, PodName: podName},
	}}
}

func ackMsg(assignmentID string, started bool) *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Ack{
		Ack: &agentv1.AssignmentAck{AssignmentId: assignmentID, Started: started},
	}}
}

// newWarmServer builds a Server with warm pools ENABLED (a registry wired) plus
// the JWT authenticator whose token ctxWithToken mints (identity "ti-1").
func newWarmServer(t *testing.T, onReclaim func(ReclaimEvent)) (*Server, *auth.JWTAuthenticator, *WorkerRegistry) {
	t.Helper()
	srv, a := newServer(&fakeStore{})
	reg := NewWorkerRegistry(onReclaim)
	srv.SetWarmPools(reg)
	return srv, a, reg
}

// awaitEventually polls cond until true or the deadline, failing the test otherwise.
func awaitEventually(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within deadline")
}

// ─── (a) inert when warm pools off ──────────────────────────────────────────

func TestAwaitAssignmentInertWhenWarmPoolsOff(t *testing.T) {
	srv, a := newServer(&fakeStore{}) // no SetWarmPools => off
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	err := srv.AwaitAssignment(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("warm pools off: got %v, want FailedPrecondition", err)
	}
}

// ─── leader gate (warm-pool Hole B) ─────────────────────────────────────────

// TestAwaitAssignmentRefusedOnFollower is the server side of the warm-pool
// leader-gate fix: with warm pools ON but this replica NOT the scheduler leader,
// AwaitAssignment refuses with FailedPrecondition BEFORE the worker is registered,
// so a warm worker never lands in a follower's in-memory registry that the
// leader-only placer never consults.
func TestAwaitAssignmentRefusedOnFollower(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	srv.SetLeaderCheck(func() bool { return false })
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	err := srv.AwaitAssignment(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("follower AwaitAssignment: got %v, want FailedPrecondition", err)
	}
	if reg.registered("ti-1") {
		t.Fatal("a follower must not register the worker in its own registry")
	}
}

// TestAwaitAssignmentProceedsOnLeader: with leaderCheck true the handler proceeds
// to registration exactly as an unchecked server does — the leader serves.
func TestAwaitAssignmentProceedsOnLeader(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	srv.SetLeaderCheck(func() bool { return true })
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	go func() { _ = srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	stream.pushErr(io.EOF) // clean shutdown
}

// TestAwaitAssignmentNilLeaderCheckUnchecked: a nil leaderCheck means "unchecked"
// (single-node / tests without wiring), so the handler serves as before.
func TestAwaitAssignmentNilLeaderCheckUnchecked(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil) // no SetLeaderCheck
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	go func() { _ = srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	stream.pushErr(io.EOF) // clean shutdown
}

// ─── (b) non-register first message ─────────────────────────────────────────

func TestAwaitAssignmentRequiresRegisterFirst(t *testing.T) {
	srv, a, _ := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(ackMsg("as-1", true)) // first message is an ack, not a register
	err := srv.AwaitAssignment(stream)
	if status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("non-register first message: got %v, want FailedPrecondition", err)
	}
}

// ─── (c) register => present in registry ────────────────────────────────────

func TestAwaitAssignmentRegistersWorker(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	go func() { _ = srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	if got := reg.dagVersionOf("ti-1"); got != "dagver-1" {
		t.Fatalf("registered dag version = %q, want dagver-1", got)
	}
	stream.pushErr(io.EOF) // clean shutdown
}

// A warm-worker credential is minted for exactly one dag_version pool (one
// tenant's DAG version). Registering for any other pool would let it receive
// that pool's assignments, and with them another tenant's task secrets, so the
// payload's dag_version_id must match the token's.
func TestAwaitAssignmentRejectsRegisterForAnotherPool(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a)) // token pool: dagver-1
	stream.pushMsg(regMsg("dagver-other-tenant"))
	done := make(chan error, 1)
	go func() { done <- srv.AwaitAssignment(stream) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		stream.pushErr(io.EOF)
		t.Fatal("register for another pool was accepted: the stream stayed open")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("register for another pool: got %v, want PermissionDenied", err)
	}
	if reg.registered("ti-1") {
		t.Fatal("a worker registering for another pool must not be registered")
	}
	if reg.Assign("dagver-other-tenant", &agentv1.WorkAssignment{AssignmentId: "x"}) {
		t.Fatal("the other pool must have no worker to assign to")
	}
}

// ─── (d) Assign pushes a WorkAssignment the stream receives ─────────────────

func TestAwaitAssignmentDeliversAssignment(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	go func() { _ = srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	if reg.Assign("other-version", &agentv1.WorkAssignment{AssignmentId: "x"}) {
		t.Fatal("Assign to a dag version with no free worker should return false")
	}
	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "as-1", TaskId: "extract", LeaseSeconds: 30}) {
		t.Fatal("Assign to a free worker's dag version should return true")
	}
	select {
	case got := <-stream.sent:
		if got.GetAssignmentId() != "as-1" || got.GetTaskId() != "extract" {
			t.Fatalf("delivered assignment = %+v, want as-1/extract", got)
		}
	case <-time.After(time.Second):
		t.Fatal("no assignment delivered to the stream")
	}
	stream.pushErr(io.EOF)
}

// ─── (e) ack started=true within lease => busy, no reclaim ──────────────────

func TestAckStartedWithinLeaseMarksBusyNoReclaim(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour } // long lease

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", "pod-1", send)
	// The assignment carries the attempt identity (run/task/try); the lease must
	// carry it through so a started ack can return it as the binding to persist.
	if !reg.Assign("v1", &agentv1.WorkAssignment{
		AssignmentId: "as-1", DagRunId: "run-1", TaskId: "extract", TryNumber: 2,
	}) {
		t.Fatal("Assign should succeed")
	}
	<-send // drain the delivered assignment
	binding, ok := reg.Ack("as-1", true)

	if !ok || binding == nil {
		t.Fatalf("Ack(started=true) = (%+v, %v), want a non-nil binding + ok=true", binding, ok)
	}
	if binding.RunID != "run-1" || binding.TaskID != "extract" || binding.TryNumber != 2 || binding.PodName != "pod-1" {
		t.Fatalf("binding = %+v, want run-1/extract/2/pod-1 (the attempt identity + the worker's pod name)", binding)
	}
	if !reg.busy("w1") {
		t.Fatal("worker should be marked busy after ack started=true")
	}
	select {
	case ev := <-reclaims:
		t.Fatalf("unexpected reclaim after ack started=true: %+v", ev)
	case <-time.After(50 * time.Millisecond):
	}
}

// ─── (f) lease expiry with no ack => reclaim ────────────────────────────────

func TestLeaseExpiryReclaims(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return 5 * time.Millisecond }

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", "pod-1", send)
	reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagVersionId: "v1"})
	<-send

	select {
	case ev := <-reclaims:
		if ev.AssignmentID != "as-1" || ev.Reason != ReclaimLeaseExpired {
			t.Fatalf("reclaim = %+v, want as-1/ReclaimLeaseExpired", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim event on lease expiry")
	}
}

// ─── (g) ack started=false => reclaim ───────────────────────────────────────

func TestAckRefusedReclaims(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour }

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", "pod-1", send)
	reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagVersionId: "v1"})
	<-send
	binding, ok := reg.Ack("as-1", false)

	if ok || binding != nil {
		t.Fatalf("Ack(started=false) = (%+v, %v), want (nil, false) — a refusal binds nothing", binding, ok)
	}
	select {
	case ev := <-reclaims:
		if ev.AssignmentID != "as-1" || ev.Reason != ReclaimRefused {
			t.Fatalf("reclaim = %+v, want as-1/ReclaimRefused", ev)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim event on ack started=false")
	}
	if reg.busy("w1") {
		t.Fatal("a worker that refused an assignment must not be marked busy")
	}
}

// ─── (f2) lease expiry returns the worker to the free set (H4) ──────────────

// TestLeaseExpiryReturnsWorkerToFree: a missed ack is a transient slow-start,
// not a wedge, so onLeaseExpire must return the worker to the free set rather
// than strand it holding a pool slot forever (ADR 0058 N1d-c, H4). Proven
// behaviourally: after the lease expires, a fresh Assign for the same
// dag_version hands the worker out again.
func TestLeaseExpiryReturnsWorkerToFree(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return 5 * time.Millisecond }

	send := make(chan *agentv1.WorkAssignment, 2)
	reg.Register("w1", "v1", "pod-1", send)
	if !reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagVersionId: "v1"}) {
		t.Fatal("first Assign should succeed")
	}
	<-send

	// Wait for the reclaim so the lease has certainly expired and the worker
	// has been returned to free under the lock.
	select {
	case ev := <-reclaims:
		if ev.Reason != ReclaimLeaseExpired {
			t.Fatalf("reclaim reason = %v, want ReclaimLeaseExpired", ev.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim on lease expiry")
	}
	if reg.busy("w1") {
		t.Fatal("a worker that never acked must not be busy")
	}

	// The worker is reusable: a subsequent Assign for the same dag_version hands
	// it out again (it would return false if the worker were stranded).
	awaitEventually(t, func() bool {
		return reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "as-2", DagVersionId: "v1"})
	})
	select {
	case got := <-send:
		if got.GetAssignmentId() != "as-2" {
			t.Fatalf("re-Assigned assignment = %q, want as-2", got.GetAssignmentId())
		}
	case <-time.After(time.Second):
		t.Fatal("expected the re-Assigned assignment on the worker's channel")
	}
}

// ─── (f3) reclaim events carry the attempt identity ─────────────────────────

// TestReclaimEventCarriesAttemptIdentity: every emit site populates the
// reclaimed attempt's (run, task, try) from the leaseState, so the observer can
// re-place the exact attempt (ADR 0058 N1d-c). Proven on the lease-expiry site.
func TestReclaimEventCarriesAttemptIdentity(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return 5 * time.Millisecond }

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", "pod-1", send)
	reg.Assign("v1", &agentv1.WorkAssignment{
		AssignmentId: "as-1", DagVersionId: "v1", DagRunId: "run-7", TaskId: "extract", TryNumber: 4,
	})
	<-send

	select {
	case ev := <-reclaims:
		if ev.RunID != "run-7" || ev.TaskID != "extract" || ev.TryNumber != 4 {
			t.Fatalf("reclaim identity = %s/%s/%d, want run-7/extract/4", ev.RunID, ev.TaskID, ev.TryNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim carrying the attempt identity")
	}
}

// TestReclaimRefusedCarriesAttemptIdentity: the refused-ack emit site also
// carries the attempt identity.
func TestReclaimRefusedCarriesAttemptIdentity(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour }

	send := make(chan *agentv1.WorkAssignment, 1)
	reg.Register("w1", "v1", "pod-1", send)
	reg.Assign("v1", &agentv1.WorkAssignment{
		AssignmentId: "as-1", DagVersionId: "v1", DagRunId: "run-7", TaskId: "load", TryNumber: 2,
	})
	<-send
	reg.Ack("as-1", false)

	select {
	case ev := <-reclaims:
		if ev.Reason != ReclaimRefused || ev.RunID != "run-7" || ev.TaskID != "load" || ev.TryNumber != 2 {
			t.Fatalf("refused reclaim = %v %s/%s/%d, want Refused run-7/load/2", ev.Reason, ev.RunID, ev.TaskID, ev.TryNumber)
		}
	case <-time.After(time.Second):
		t.Fatal("expected a refused reclaim carrying the attempt identity")
	}
}

// ─── (h) stream close => deregistered ───────────────────────────────────────

func TestAwaitAssignmentDeregistersOnStreamClose(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	done := make(chan error, 1)
	go func() { done <- srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	stream.pushErr(io.EOF)

	if err := <-done; err != nil {
		t.Fatalf("clean stream close should return nil, got %v", err)
	}
	awaitEventually(t, func() bool { return !reg.registered("ti-1") })
}

// ─── (i) reconnect same identity => single entry ────────────────────────────

func TestAwaitAssignmentReconnectSameIdentitySingleEntry(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)

	s1 := newFakeAwaitStream(ctxWithWarmToken(t, a))
	s1.pushMsg(regMsgPod("dagver-1", "pod-a"))
	go func() { _ = srv.AwaitAssignment(s1) }()
	awaitEventually(t, func() bool { return reg.podNameOf("ti-1") == "pod-a" })

	// A reconnect with the SAME authenticated identity (and so the same pool) but
	// a new registration.
	s2 := newFakeAwaitStream(ctxWithWarmToken(t, a))
	s2.pushMsg(regMsgPod("dagver-1", "pod-b"))
	go func() { _ = srv.AwaitAssignment(s2) }()
	awaitEventually(t, func() bool { return reg.podNameOf("ti-1") == "pod-b" })

	if reg.size() != 1 {
		t.Fatalf("reconnect same identity: registry size = %d, want 1", reg.size())
	}
}

// ─── (j) started ack => handler persists the durable binding ────────────────

// TestAwaitAssignmentPersistsBindingOnStartedAck is the handler seam: when a
// worker acks an assignment as started, pumpWorkerMessages must call the store's
// BindWarmAttempt with the assignment's (run, task, try) and the worker's pod
// name (from WorkerRegister.pod_name), so the binding is durable for the reaper.
func TestAwaitAssignmentPersistsBindingOnStartedAck(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)
	reg := NewWorkerRegistry(nil)
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour } // no reclaim mid-test
	srv.SetWarmPools(reg)

	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsgPod("dagver-1", "warm-pod-7"))
	go func() { _ = srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{
		AssignmentId: "as-9", DagRunId: "run-42", TaskId: "load", TryNumber: 3, LeaseSeconds: 3600,
	}) {
		t.Fatal("Assign should succeed against the registered worker")
	}
	<-stream.sent // handler delivered the assignment down the stream

	stream.pushMsg(ackMsg("as-9", true))

	awaitEventually(t, func() bool { return len(store.warmBindingCalls()) == 1 })
	got := store.warmBindingCalls()[0]
	if got.runID != "run-42" || got.taskID != "load" || got.tryNumber != 3 || got.workerPod != "warm-pod-7" {
		t.Fatalf("BindWarmAttempt call = %+v, want run-42/load/3/warm-pod-7", got)
	}
	stream.pushErr(io.EOF)
}

// TestAwaitAssignmentDoesNotPersistOnRefusedAck: a started=false ack reclaims
// and binds nothing — the store's BindWarmAttempt must not be called.
func TestAwaitAssignmentDoesNotPersistOnRefusedAck(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)
	reclaims := make(chan ReclaimEvent, 1)
	reg := NewWorkerRegistry(func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour }
	srv.SetWarmPools(reg)

	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsgPod("dagver-1", "warm-pod-7"))
	go func() { _ = srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{
		AssignmentId: "as-9", DagRunId: "run-42", TaskId: "load", TryNumber: 3, LeaseSeconds: 3600,
	}) {
		t.Fatal("Assign should succeed")
	}
	<-stream.sent
	stream.pushMsg(ackMsg("as-9", false))

	// The refusal is observed as a reclaim; no binding is persisted.
	select {
	case <-reclaims:
	case <-time.After(time.Second):
		t.Fatal("expected a reclaim on refused ack")
	}
	if n := len(store.warmBindingCalls()); n != 0 {
		t.Fatalf("BindWarmAttempt calls = %d, want 0 on a refused ack", n)
	}
	stream.pushErr(io.EOF)
}
