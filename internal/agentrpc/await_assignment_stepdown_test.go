package agentrpc

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func slotFreeMsg() *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_SlotFree{SlotFree: &agentv1.SlotFree{}}}
}

// openLeaderStream opens an AwaitAssignment stream on a leader whose leadership
// the returned flag controls, and waits for the worker to register.
func openLeaderStream(t *testing.T) (*fakeAwaitStream, *WorkerRegistry, *atomic.Bool, <-chan error) {
	t.Helper()
	srv, a, reg := newWarmServer(t, nil)
	srv.leaderPoll = 5 * time.Millisecond
	leading := &atomic.Bool{}
	leading.Store(true)
	srv.SetLeaderCheck(leading.Load)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	ended := make(chan error, 1)
	go func() { ended <- srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	return stream, reg, leading, ended
}

func requireNotLeaderEnd(t *testing.T, ended <-chan error) {
	t.Helper()
	select {
	case err := <-ended:
		if status.Code(err) != codes.FailedPrecondition {
			t.Fatalf("stream ended with %v, want FailedPrecondition (not the leader, reconnect)", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("stream still open 2s after the step-down")
	}
}

func requireStillOpen(t *testing.T, ended <-chan error, why string) {
	t.Helper()
	select {
	case err := <-ended:
		t.Fatalf("stream ended (%v) %s", err, why)
	case <-time.After(100 * time.Millisecond):
	}
}

// TestAwaitAssignmentEndsIdleStreamOnStepDown: the leader gate is not only
// checked when the stream opens. After this replica steps down without a
// restart, an idle worker's stream ends with FailedPrecondition so the worker
// re-dials toward the new leader, and the worker leaves this registry, where no
// placer runs any more (#1602).
func TestAwaitAssignmentEndsIdleStreamOnStepDown(t *testing.T) {
	_, reg, leading, ended := openLeaderStream(t)
	leading.Store(false)
	requireNotLeaderEnd(t, ended)
	if reg.registered("ti-1") {
		t.Fatal("the worker is still registered on the replica that stepped down")
	}
}

// TestAwaitAssignmentKeepsBusyStreamUntilSlotFreeOnStepDown: a worker running
// an attempt keeps its stream through a step-down, so it can finish and signal
// SlotFree on it, and is sent away right after.
func TestAwaitAssignmentKeepsBusyStreamUntilSlotFreeOnStepDown(t *testing.T) {
	stream, reg, leading, ended := openLeaderStream(t)
	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagRunId: "r", TaskId: "t", TryNumber: 1, LeaseSeconds: 30}) {
		t.Fatal("assign failed")
	}
	<-stream.sent
	stream.pushMsg(ackMsg("as-1", true))
	awaitEventually(t, func() bool { return reg.busy("ti-1") })

	leading.Store(false)
	requireStillOpen(t, ended, "while the worker was busy with an acked attempt")

	stream.pushMsg(slotFreeMsg())
	requireNotLeaderEnd(t, ended)
}

// TestAwaitAssignmentKeepsLeasedStreamUntilAckOnStepDown: an assignment handed
// out just before the step-down is settled by the worker's ack on this stream
// before the stream is sent away, so the ack is not lost to a closed stream.
func TestAwaitAssignmentKeepsLeasedStreamUntilAckOnStepDown(t *testing.T) {
	stream, reg, leading, ended := openLeaderStream(t)
	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagRunId: "r", TaskId: "t", TryNumber: 1, LeaseSeconds: 30}) {
		t.Fatal("assign failed")
	}
	<-stream.sent

	leading.Store(false)
	requireStillOpen(t, ended, "while an assignment awaited its ack")

	stream.pushMsg(ackMsg("as-1", false))
	requireNotLeaderEnd(t, ended)
}

// TestAwaitAssignmentAnnouncesRegistration: once the worker is registered the
// handler sends a response header saying so. A worker resets its not-leader
// reconnect budget on it, since a stream a leader accepted and later sent away
// on a step-down is not one more failed search for the leader.
func TestAwaitAssignmentAnnouncesRegistration(t *testing.T) {
	stream, _, _, _ := openLeaderStream(t)
	select {
	case md := <-stream.headers:
		if got := md.Get(warmRegisteredHeader); len(got) != 1 || got[0] != "true" {
			t.Fatalf("response header %v, want %s: true", md, warmRegisteredHeader)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no response header after registration")
	}
	if warmRegisteredHeader != "dexaflow-warm-registered" {
		t.Fatalf("header name %q changed; the agent reads dexaflow-warm-registered", warmRegisteredHeader)
	}
	stream.pushErr(io.EOF)
}

// TestAwaitAssignmentFollowerAnnouncesNothing: a refused stream carries no
// registration header, so the worker keeps counting the rejection.
func TestAwaitAssignmentFollowerAnnouncesNothing(t *testing.T) {
	srv, a, _ := newWarmServer(t, nil)
	srv.SetLeaderCheck(func() bool { return false })
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	if err := srv.AwaitAssignment(stream); status.Code(err) != codes.FailedPrecondition {
		t.Fatalf("follower: got %v, want FailedPrecondition", err)
	}
	select {
	case md := <-stream.headers:
		t.Fatalf("a follower sent a response header: %v", md)
	default:
	}
}

// TestAwaitAssignmentStaysOpenWhileLeading: the periodic gate does not end a
// stream on a replica that is still the leader.
func TestAwaitAssignmentStaysOpenWhileLeading(t *testing.T) {
	stream, reg, _, ended := openLeaderStream(t)
	requireStillOpen(t, ended, "on a replica that is still the leader")
	if !reg.registered("ti-1") {
		t.Fatal("worker deregistered on the leader")
	}
	stream.pushErr(io.EOF)
	if err := <-ended; err != nil {
		t.Fatalf("clean close: %v", err)
	}
}

// TestAwaitAssignmentKeepsTombstonedStreamOnStepDown: an assignment whose lease
// expired is kept as a tombstone because the worker may still ack it late and
// run it (#1600). Such a worker is not idle, so a step-down does not send it
// away while its late ack can still arrive; it is sent away once that ack
// settles the assignment.
func TestAwaitAssignmentKeepsTombstonedStreamOnStepDown(t *testing.T) {
	reclaims := make(chan ReclaimEvent, 4)
	srv, a, reg := newWarmServer(t, func(ev ReclaimEvent) { reclaims <- ev })
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return 5 * time.Millisecond }
	reg.tombstoneFor = time.Hour
	srv.leaderPoll = 5 * time.Millisecond
	leading := &atomic.Bool{}
	leading.Store(true)
	srv.SetLeaderCheck(leading.Load)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	ended := make(chan error, 1)
	go func() { ended <- srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "as-1", DagRunId: "r", TaskId: "t", TryNumber: 1}) {
		t.Fatal("assign failed")
	}
	<-stream.sent
	select {
	case ev := <-reclaims:
		if ev.Reason != ReclaimLeaseExpired {
			t.Fatalf("reclaim reason = %v, want ReclaimLeaseExpired", ev.Reason)
		}
	case <-time.After(time.Second):
		t.Fatal("expected the lease to expire")
	}

	leading.Store(false)
	requireStillOpen(t, ended, "while an expired lease could still be acked late")

	stream.pushMsg(ackMsg("as-1", false))
	requireNotLeaderEnd(t, ended)
}
