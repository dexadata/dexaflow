package agentrpc

import (
	"io"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// TestStartedAckNotifiesClaimAfterBinding is the claim half of event-driven
// refill: once a warm worker acks an attempt as started and the binding that
// makes it count as busy is persisted, the registry's claim hook fires, so the
// pool reconciler can top the idle buffer back up without waiting for its tick.
// The hook must run after the binding, or the reconciler would still see the
// worker idle.
func TestStartedAckNotifiesClaimAfterBinding(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)
	reg := NewWorkerRegistry(nil)
	reg.leaseFor = func(*agentv1.WorkAssignment) time.Duration { return time.Hour }
	var claims, bindsAtClaim atomic.Int32
	reg.SetOnClaim(func() {
		bindsAtClaim.Store(int32(len(store.warmBindingCalls()))) //nolint:gosec // tiny test count
		claims.Add(1)
	})
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
	<-stream.sent
	if claims.Load() != 0 {
		t.Fatal("the claim hook fired before the worker acked")
	}

	stream.pushMsg(ackMsg("as-9", true))
	awaitEventually(t, func() bool { return claims.Load() == 1 })
	if bindsAtClaim.Load() != 1 {
		t.Errorf("claim hook ran with %d bindings persisted, want 1 (after BindWarmAttempt)", bindsAtClaim.Load())
	}
	stream.pushErr(io.EOF)
}
