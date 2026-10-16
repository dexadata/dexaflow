package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// reportServer is a minimal control plane: ReportState answers ok, or with
// appErr when one is set (an application-class failure of the handler).
type reportServer struct {
	agentv1.UnimplementedAgentServiceServer
	appErr  error
	reports atomic.Int32
}

func (s *reportServer) ReportState(context.Context, *agentv1.ReportStateRequest) (*agentv1.ReportStateResponse, error) {
	s.reports.Add(1)
	if s.appErr != nil {
		return nil, s.appErr
	}
	return &agentv1.ReportStateResponse{}, nil
}

// proxiedControlPlane serves srv behind an HTTP/2 front end that fails the
// first failN requests with fail (an L7 load balancer or mesh sidecar during a
// control-plane outage) and forwards the rest to the gRPC server. It returns a
// Runner whose client is the production Dial to that front end, with instant
// retry sleeps, and the number of requests the front end saw.
func proxiedControlPlane(t *testing.T, srv *reportServer, failN int32, fail http.HandlerFunc) (*Runner, *atomic.Int32) {
	t.Helper()
	gs := grpc.NewServer()
	agentv1.RegisterAgentServiceServer(gs, srv)
	var seen atomic.Int32
	var protocols http.Protocols
	protocols.SetUnencryptedHTTP2(true)
	front := &http.Server{
		Protocols:         &protocols,
		ReadHeaderTimeout: 5 * time.Second,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if seen.Add(1) <= failN {
				fail(w, r)
				return
			}
			gs.ServeHTTP(w, r)
		}),
	}
	var lc net.ListenConfig
	lis, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = front.Serve(lis) }()
	t.Cleanup(func() { _ = front.Close(); gs.Stop() })

	client, conn, _, err := Dial(lis.Addr().String(), "token", true, "")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	r := &Runner{
		Client:   client,
		Hostname: "pod-1",
		Version:  "test",
		afterFunc: func(time.Duration) <-chan time.Time {
			ch := make(chan time.Time, 1)
			ch <- time.Time{}
			return ch
		},
	}
	return r, &seen
}

// lb500 is a load balancer answering 500 for a backend that is down: grpc-go
// maps HTTP 500 to codes.Unknown (502, 503 and 504 map to Unavailable).
func lb500(w http.ResponseWriter, _ *http.Request) {
	http.Error(w, "upstream connect error", http.StatusInternalServerError)
}

// rstMidStream resets the stream (RST_STREAM INTERNAL_ERROR), as a proxy does
// when its upstream connection drops under an open stream: grpc-go surfaces it
// as codes.Internal.
func rstMidStream(http.ResponseWriter, *http.Request) {
	panic(http.ErrAbortHandler)
}

// TestTerminalReportRetriesTransportClassErrorsBehindLB covers #908: behind an
// L7 load balancer or a mesh, a control-plane outage reaches the agent as an
// HTTP 500 (codes.Unknown) or a reset stream (codes.Internal) rather than as
// Unavailable. The terminal report must keep retrying those, as it does for
// Unavailable, and land directly once the control plane is back.
func TestTerminalReportRetriesTransportClassErrorsBehindLB(t *testing.T) {
	for name, fail := range map[string]http.HandlerFunc{"lb-500": lb500, "rst-mid-stream": rstMidStream} {
		t.Run(name, func(t *testing.T) {
			srv := &reportServer{}
			r, seen := proxiedControlPlane(t, srv, 3, fail)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			if err := r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, ""); err != nil {
				t.Fatalf("terminal report must land once the control plane is back, got %v", err)
			}
			if got := srv.reports.Load(); got != 1 {
				t.Errorf("control plane received %d reports, want 1", got)
			}
			if got := seen.Load(); got != 4 {
				t.Errorf("front end saw %d requests, want 4 (3 failed + 1 delivered)", got)
			}
		})
	}
}

// TestTerminalReportDoesNotRetryApplicationUnknown: an Unknown the control
// plane's own handler returns is a server-side defect, not an outage. Retrying
// it forever would hide the defect behind a loop, so it stays terminal: one
// attempt, error returned. A handler's Internal is retried only within
// transientRetryBudget (TestReportInternalRetryIsBounded), never without bound.
func TestTerminalReportDoesNotRetryApplicationUnknown(t *testing.T) {
	for _, code := range []codes.Code{codes.Unknown} {
		t.Run(code.String(), func(t *testing.T) {
			srv := &reportServer{appErr: status.Error(code, "reporting task state")}
			r, _ := proxiedControlPlane(t, srv, 0, nil)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			err := r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, "")
			if status.Code(errors.Unwrap(err)) != code {
				t.Fatalf("want the handler's %v returned, got %v", code, err)
			}
			if got := srv.reports.Load(); got != 1 {
				t.Errorf("application-class %v was attempted %d times, want 1", code, got)
			}
		})
	}
}

// TestTransportClassErr pins the classifier on the shapes grpc-go produces for
// a broken transport, and on what it must leave alone.
func TestTransportClassErr(t *testing.T) {
	cases := []struct {
		err  error
		want bool
	}{
		{status.Error(codes.Unknown, "unexpected HTTP status code received from server: 500 (Internal Server Error); transport: received unexpected content-type \"text/plain\""), true},
		{status.Error(codes.Internal, "stream terminated by RST_STREAM with error code: INTERNAL_ERROR"), true},
		{status.Error(codes.Internal, "server closed the stream without sending trailers"), true},
		{status.Error(codes.Internal, "unexpected EOF"), true},
		{status.Error(codes.Internal, "EOF"), true},
		{status.Error(codes.Unknown, "read tcp 10.0.0.1:1->10.0.0.2:2: read: connection reset by peer"), true},
		{status.Error(codes.Internal, "transport is closing"), true},
		{status.Error(codes.Internal, "write tcp: broken pipe"), true},
		{status.Error(codes.Internal, "reporting task state"), false},
		{status.Error(codes.Unknown, "panic in handler"), false},
		{status.Error(codes.Internal, "unexpected HTTP status code received from server: 400 (Bad Request)"), false},
		{status.Error(codes.InvalidArgument, "unexpected EOF"), false},
		{status.Error(codes.PermissionDenied, "connection reset"), false},
		{errors.New("plain error"), false},
	}
	for _, c := range cases {
		if got := transportClassErr(c.err); got != c.want {
			t.Errorf("transportClassErr(%v) = %v, want %v", c.err, got, c.want)
		}
	}
}
