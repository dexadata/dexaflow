package agentrpc

import (
	"io"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// ADR 0095: a source-mode assignment carries the dag.py, and only a worker
// that registered with source_delivery runs from it. An older worker of the
// same pool is passed over, reported once, and an assignment without a
// source still goes to it.
func TestAssignGivesASourceModeAttemptOnlyToAWorkerWithSourceDelivery(t *testing.T) {
	reg := NewWorkerRegistry(nil)
	rec := &fakeDecisions{}
	reg.SetSweepPolicy(SweepIgnore, rec)
	oldSend := make(chan *agentv1.WorkAssignment, 1)
	if _, err := reg.Register("old", "v1", "pod-old", oldSend, nil); err != nil {
		t.Fatal(err)
	}
	src := &agentv1.WorkAssignment{AssignmentId: "a1", DagSource: []byte("print(1)\n"), LeaseSeconds: 30}

	for range 2 {
		if reg.Assign("v1", src) {
			t.Fatal("a source-mode attempt was placed on a worker without source delivery")
		}
	}
	if got := rec.count(DecisionWarmWorkerNoSourceDelivery); got != 1 {
		t.Errorf("passed-over workers metered = %d, want 1 (once per worker)", got)
	}

	newSend := make(chan *agentv1.WorkAssignment, 1)
	if _, err := reg.RegisterWorker("new", "v1", WorkerInfo{PodName: "pod-new", SourceDelivery: true}, newSend, nil); err != nil {
		t.Fatal(err)
	}
	if !reg.Assign("v1", src) {
		t.Fatal("a source-mode attempt was not placed on the worker with source delivery")
	}
	if got := <-newSend; got.GetAssignmentId() != "a1" {
		t.Errorf("worker with source delivery got %q, want a1", got.GetAssignmentId())
	}
	if !reg.Assign("v1", &agentv1.WorkAssignment{AssignmentId: "a2", LeaseSeconds: 30}) {
		t.Fatal("an assignment without a source was not placed on the older worker")
	}
	if got := <-oldSend; got.GetAssignmentId() != "a2" {
		t.Errorf("older worker got %q, want a2", got.GetAssignmentId())
	}
}

// The registration's source_delivery reaches the registry entry.
func TestAwaitAssignmentRecordsSourceDelivery(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(&agentv1.WorkerMessage{Msg: &agentv1.WorkerMessage_Register{
		Register: &agentv1.WorkerRegister{DagVersionId: "dagver-1", SourceDelivery: true},
	}})
	go func() { _ = srv.AwaitAssignment(stream) }()

	awaitEventually(t, func() bool { return reg.registered("ti-1") })
	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "a1", DagSource: []byte("print(1)\n"), LeaseSeconds: 30}) {
		t.Error("a worker that registered with source_delivery was not offered a source-mode attempt")
	}
	stream.pushErr(io.EOF)
}
