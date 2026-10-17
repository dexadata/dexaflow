package agentrpc

import (
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// fakeDecisions records metered decision types.
type fakeDecisions struct {
	mu    sync.Mutex
	types []string
}

func (f *fakeDecisions) RecordSchedulerDecision(decisionType string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.types = append(f.types, decisionType)
}

func (f *fakeDecisions) count(decisionType string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, d := range f.types {
		if d == decisionType {
			n++
		}
	}
	return n
}

func regMsgSweep(dagVersion string, sweep bool) *agentv1.WorkerMessage {
	return &agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Register{
		Register: &agentv1.WorkerRegister{DagVersionId: dagVersion, PodName: "warm-pod-1", AttemptHomeSweep: sweep},
	}}
}

// TestAwaitAssignmentRefusesWorkerWithoutSweepWhenRequired locks #1678: with
// warm workers on a read-only root, a worker whose agent does not confirm it
// empties /tmp and /dev/shm between attempts (a task image built on an older
// runtime) is refused with a stable reason and metered, and never joins the
// pool, so its DAG version's attempts run on dedicated pods.
func TestAwaitAssignmentRefusesWorkerWithoutSweepWhenRequired(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	rec := &fakeDecisions{}
	reg.SetSweepPolicy(SweepRefuse, rec)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsgSweep("dagver-1", false))

	done := make(chan error, 1)
	go func() { done <- srv.AwaitAssignment(stream) }()
	var err error
	select {
	case err = <-done:
	case <-time.After(2 * time.Second):
		stream.pushErr(io.EOF)
		t.Fatal("a worker without the sweep was accepted: the stream stayed open")
	}
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("register without sweep: got %v, want PermissionDenied", err)
	}
	if msg := status.Convert(err).Message(); !strings.Contains(msg, ReasonAttemptHomeSweepUnconfirmed) {
		t.Errorf("refusal message %q does not carry the stable reason %q", msg, ReasonAttemptHomeSweepUnconfirmed)
	}
	if reg.registered("ti-1") {
		t.Fatal("a worker that does not confirm the sweep must not be registered")
	}
	if reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "x"}) {
		t.Fatal("the pool must have no warm worker to place on, so the attempt runs dedicated")
	}
	if got := rec.count(DecisionWarmWorkerRefusedNoSweep); got != 1 {
		t.Errorf("refusals metered = %d, want 1", got)
	}
}

// TestAwaitAssignmentAcceptsWorkerWithSweepWhenRequired: a worker that confirms
// the sweep registers as before when the sweep is required.
func TestAwaitAssignmentAcceptsWorkerWithSweepWhenRequired(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	rec := &fakeDecisions{}
	reg.SetSweepPolicy(SweepRefuse, rec)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsgSweep("dagver-1", true))
	go func() { _ = srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	if got := rec.count(DecisionWarmWorkerRefusedNoSweep); got != 0 {
		t.Errorf("refusals metered = %d, want 0", got)
	}
	stream.pushErr(io.EOF)
}

// TestAwaitAssignmentWarnsAndAcceptsWorkerWithoutSweep: under SweepWarn (the
// read-only root on, refusal not asked for) a worker that does not confirm the
// sweep is metered and still registers.
func TestAwaitAssignmentWarnsAndAcceptsWorkerWithoutSweep(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	rec := &fakeDecisions{}
	reg.SetSweepPolicy(SweepWarn, rec)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsgSweep("dagver-1", false))
	go func() { _ = srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	if got := rec.count(DecisionWarmWorkerSweepUnconfirmed); got != 1 {
		t.Errorf("unconfirmed workers metered = %d, want 1", got)
	}
	if got := rec.count(DecisionWarmWorkerRefusedNoSweep); got != 0 {
		t.Errorf("refusals metered = %d, want 0", got)
	}
	stream.pushErr(io.EOF)
}

// TestAwaitAssignmentSweepIgnoredByDefault: with the read-only root off (the
// default policy), a worker that does not confirm the sweep registers exactly
// as before.
func TestAwaitAssignmentSweepIgnoredByDefault(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	if p := reg.SweepPolicy(); p != SweepIgnore {
		t.Fatalf("a new registry has sweep policy %v; the default must ignore the sweep", p)
	}
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsgSweep("dagver-1", false))
	go func() { _ = srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	stream.pushErr(io.EOF)
}
