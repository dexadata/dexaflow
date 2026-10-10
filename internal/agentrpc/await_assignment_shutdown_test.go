package agentrpc

import (
	"context"
	"io"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestAwaitAssignmentEndsOnShutdown pins the warm-pool half of the eviction bug
// class. An idle warm worker holds its assignment stream open indefinitely — that
// is the whole point of the transport — so without a shutdown signal the bounded
// graceful stop waits its full budget on every single shutdown: the forced path
// becomes the normal path and the "graceful stop exceeded its bound" warning
// fires every time, which destroys its value as a signal. The handler must end
// the stream itself when the control plane starts shutting down, with the same
// Unavailable the log stream uses.
//
// What the worker does with it: its assignment-RECEIVE branch treats Unavailable
// as a clean end and the process exits 0, so the reconciler recreates the pod
// against whichever replica survives. It does NOT reconnect in place — only a
// not-leader FailedPrecondition does that, and this code is deliberately not
// that one, because a replica on its way out must not pull a worker's bounded
// reconnect budget back to itself.
func TestAwaitAssignmentEndsOnShutdown(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	srv.SetShutdown(shutdownCtx)

	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	errCh := make(chan error, 1)
	go func() { errCh <- srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	// The steady state of a warm worker: registered, idle, nothing else arriving.
	select {
	case err := <-errCh:
		t.Fatalf("AwaitAssignment returned %v while the worker sat idle; the stream must stay open until shutdown", err)
	case <-time.After(100 * time.Millisecond):
	}

	shutdown()
	select {
	case err := <-errCh:
		if status.Code(err) != codes.Unavailable {
			t.Fatalf("AwaitAssignment on shutdown = %v, want codes.Unavailable so the worker treats it as a transient stream end and reconnects", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitAssignment did not return within 2s of the shutdown signal; the graceful stop would burn its whole bound on this stream")
	}
	if reg.registered("ti-1") {
		t.Fatal("the worker is still registered after the handler returned; the deregister defer must run on the shutdown path too")
	}
}

// TestAwaitAssignmentDeliversAssignmentWithShutdownWired: wiring the shutdown
// signal must not change what a warm worker does while the control plane is
// alive. With the signal wired but not fired, registration, assignment delivery
// and the clean EOF end are exactly as before — the reconnect behavior only
// changes at shutdown.
func TestAwaitAssignmentDeliversAssignmentWithShutdownWired(t *testing.T) {
	srv, a, reg := newWarmServer(t, nil)
	shutdownCtx, shutdown := context.WithCancel(context.Background())
	defer shutdown()
	srv.SetShutdown(shutdownCtx)

	stream := newFakeAwaitStream(ctxWithWarmToken(t, a))
	stream.pushMsg(regMsg("dagver-1"))
	errCh := make(chan error, 1)
	go func() { errCh <- srv.AwaitAssignment(stream) }()
	awaitEventually(t, func() bool { return reg.registered("ti-1") })

	if !reg.Assign("dagver-1", &agentv1.WorkAssignment{AssignmentId: "as-1", TaskId: "extract", LeaseSeconds: 30}) {
		t.Fatal("Assign to a free worker's dag version should return true")
	}
	select {
	case got := <-stream.sent:
		if got.GetAssignmentId() != "as-1" || got.GetTaskId() != "extract" {
			t.Fatalf("delivered assignment = %+v, want as-1/extract", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no assignment delivered with the shutdown signal wired but not fired")
	}

	stream.pushErr(io.EOF)
	select {
	case err := <-errCh:
		if err != nil {
			t.Fatalf("AwaitAssignment on a clean EOF = %v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("AwaitAssignment did not return on a clean EOF")
	}
}
