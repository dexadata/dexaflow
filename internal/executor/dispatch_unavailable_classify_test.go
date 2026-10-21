package executor

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"syscall"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// webhookDown builds the error the apiserver returns when a fail-closed
// admission webhook cannot be reached: a 500 InternalError, not the 403 of a
// webhook that answered and denied the request.
func webhookDown() error {
	return apierrors.NewInternalError(errors.New(
		`failed calling webhook "validate.example.com": failed to call webhook: Post "https://webhook.svc:443/validate": dial tcp 10.0.0.9:443: connect: connection refused`))
}

// syscallErr builds the error chain client-go hands back when the TCP
// connection to the apiserver fails: a *url.Error around a *net.OpError
// around the errno.
func syscallErr(errno syscall.Errno) error {
	return &url.Error{Op: "Post", URL: "https://10.0.0.1:443/api/v1/namespaces/ns/pods", Err: &net.OpError{
		Op: "dial", Net: "tcp", Err: os.NewSyscallError("connect", errno),
	}}
}

// TestClassifyDispatchErrorUnavailable pins the verdict for each shape a
// control plane outage takes (#1606). An apiserver that is restarting, an
// etcd leader change, a fail-closed webhook that is down or a client deadline
// is Unavailable: re-offered off the dispatch budget, bounded by a time window.
// A request the apiserver understood and refused stays Rejected (bounded
// attempts), and cluster backpressure stays Backpressure (unbounded).
func TestClassifyDispatchErrorUnavailable(t *testing.T) {
	gr := schema.GroupResource{Resource: "pods"}
	for _, tc := range []struct {
		name string
		err  error
		want Disposition
	}{
		{"503_service_unavailable", apierrors.NewServiceUnavailable("the server is currently unable to handle the request"), Unavailable},
		{"503_wrapped", fmt.Errorf("creating pod for task a: %w", apierrors.NewServiceUnavailable("etcdserver: leader changed")), Unavailable},
		{"504_gateway_timeout", apierrors.NewTimeoutError("the request timed out", 0), Unavailable},
		{"500_server_timeout", apierrors.NewServerTimeout(gr, "create", 1), Unavailable},
		{"500_webhook_down", webhookDown(), Unavailable},
		{"500_webhook_down_wrapped", fmt.Errorf("creating pod for task a: %w", webhookDown()), Unavailable},
		{"connection_refused", syscallErr(syscall.ECONNREFUSED), Unavailable},
		{"connection_reset", fmt.Errorf("creating pod for task a: %w", syscallErr(syscall.ECONNRESET)), Unavailable},
		{"client_deadline", fmt.Errorf("creating pod for task a: %w", &url.Error{Op: "Post", URL: "https://k", Err: context.DeadlineExceeded}), Unavailable},
		{"client_deadline_bare", context.DeadlineExceeded, Unavailable},
		{"net_timeout", &url.Error{Op: "Post", URL: "https://k", Err: timeoutErr{}}, Unavailable},

		// Cluster backpressure keeps its own, unbounded, path.
		{"429_too_many_requests", apierrors.NewTooManyRequests("Priority and Fairness: request rejected", 1), Backpressure},
		{"403_quota", quota403(), Backpressure},
		// The client rate limiter wraps a deadline error, but nothing was sent.
		{"client_rate_limiter", fmt.Errorf("creating pod: %w", fmt.Errorf("client rate limiter Wait returned an error: %w", context.DeadlineExceeded)), Deferred},

		// The apiserver answered and refused the request: retrying the same
		// spec cannot help, so the bounded attempt budget applies.
		{"400_bad_request", apierrors.NewBadRequest("invalid pod"), Rejected},
		{"403_rbac", rbac403(), Rejected},
		{"403_webhook_denied", admissionDenied(), Rejected},
		{"404_namespace_not_found", apierrors.NewNotFound(schema.GroupResource{Resource: "namespaces"}, "prod"), Rejected},
		{"409_already_exists", apierrors.NewAlreadyExists(gr, "etl-a-1-abcd"), Rejected},
		{"422_invalid", apierrors.NewInvalid(schema.GroupKind{Kind: "Pod"}, "a", nil), Rejected},
		{"canceled", context.Canceled, Rejected},
		{"plain_text_refused", errors.New("connection refused"), Rejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := classifyDispatchError(tc.err); got != tc.want {
				t.Errorf("classifyDispatchError(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}

// timeoutErr is a net.Error that timed out, the shape of an http.Client
// Timeout.
type timeoutErr struct{}

func (timeoutErr) Error() string   { return "Client.Timeout exceeded while awaiting headers" }
func (timeoutErr) Timeout() bool   { return true }
func (timeoutErr) Temporary() bool { return true }

// TestExecuteClassifiesAnUnavailableAPIServer drives the real client-go
// clientset against a fake apiserver, so the verdict is pinned on the errors
// client-go actually returns, not on hand-built ones.
func TestExecuteClassifiesAnUnavailableAPIServer(t *testing.T) {
	status := func(code int, reason metav1.StatusReason, msg string) http.HandlerFunc {
		return func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_, _ = fmt.Fprintf(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","code":%d,"reason":%q,"message":%q}`, code, reason, msg)
		}
	}
	for _, tc := range []struct {
		name    string
		handler http.HandlerFunc
		timeout time.Duration
		want    Disposition
	}{
		{"503", status(503, metav1.StatusReasonServiceUnavailable, "apiserver is shutting down"), 0, Unavailable},
		{"504", status(504, metav1.StatusReasonTimeout, "the request timed out"), 0, Unavailable},
		{"500_webhook", status(500, metav1.StatusReasonInternalError, `Internal error occurred: failed calling webhook "v.example.com"`), 0, Unavailable},
		{"reset", func(w http.ResponseWriter, _ *http.Request) {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				return
			}
			if tcp, ok := conn.(*net.TCPConn); ok {
				_ = tcp.SetLinger(0) // close with RST
			}
			_ = conn.Close()
		}, 0, Unavailable},
		{"client_deadline", func(_ http.ResponseWriter, r *http.Request) {
			// Answer only after the client gave up; bounded so the server can
			// close whatever the transport does with the abandoned request.
			select {
			case <-r.Context().Done():
			case <-time.After(time.Second):
			}
		}, 200 * time.Millisecond, Unavailable},
		{"422", status(422, metav1.StatusReasonInvalid, "Pod is invalid"), 0, Rejected},
		{"400", status(400, metav1.StatusReasonBadRequest, "bad request"), 0, Rejected},
		{"404", status(404, metav1.StatusReasonNotFound, `namespaces "ns" not found`), 0, Rejected},
		{"429", status(429, metav1.StatusReasonTooManyRequests, "too many requests"), 0, Backpressure},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			disp, err := executeAgainst(t, srv.URL, tc.timeout)
			if err == nil {
				t.Fatal("the create must fail")
			}
			if disp != tc.want {
				t.Errorf("disposition = %s, want %s (%v)", disp, tc.want, err)
			}
		})
	}

	t.Run("refused", func(t *testing.T) {
		ln, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := ln.Addr().String()
		_ = ln.Close() // nothing listens there now
		disp, err := executeAgainst(t, "http://"+addr, 0)
		if err == nil {
			t.Fatal("the create must fail")
		}
		if disp != Unavailable {
			t.Errorf("connection refused: disposition = %s, want unavailable (%v)", disp, err)
		}
	})
}

// executeAgainst runs one pod create through a real clientset pointed at host.
// timeout is the client's request timeout (rest.Config.Timeout); zero is none.
func executeAgainst(t *testing.T, host string, timeout time.Duration) (Disposition, error) {
	t.Helper()
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: host, Timeout: timeout, QPS: -1,
		ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	exec := NewKubernetesExecutor(cs, "ns")
	return exec.Execute(context.Background(), Request{RunID: "r", DagID: "d", TaskID: "t", TryNumber: 1, Image: "img"})
}
