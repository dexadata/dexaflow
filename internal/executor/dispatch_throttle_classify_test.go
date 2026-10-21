package executor

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A pod create the client-side rate limiter refuses because its wait would run
// past the caller's deadline never reaches the apiserver, so it is local
// backpressure (Deferred), not a failed dispatch: it must not count toward the
// dispatch-attempt budget that ends in dispatch_failed (R-03, #1596). It goes
// through a real client-go clientset so the real limiter produces the error.
func TestExecuteClassifiesClientRateLimiterRefusalAsDeferred(t *testing.T) {
	var posts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/pods") {
			posts.Add(1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write(body)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	// One request a minute, burst one: the first create takes the only token.
	cs, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL, QPS: 1.0 / 60, Burst: 1,
		ContentConfig: rest.ContentConfig{ContentType: "application/json", AcceptContentTypes: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	exec := NewKubernetesExecutor(cs, "ns")
	req := Request{RunID: "r", DagID: "d", TaskID: "t", TryNumber: 1, Image: "img"}
	if disp, cerr := exec.Execute(context.Background(), req); cerr != nil || disp != Dispatched {
		t.Fatalf("first create: disposition %s, error %v", disp, cerr)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	disp, err := exec.Execute(ctx, req)
	if err == nil {
		t.Fatal("the limiter must refuse a create whose wait exceeds the deadline")
	}
	if disp != Deferred {
		t.Errorf("a client rate limiter refusal must be Deferred, got %s (%v)", disp, err)
	}
	if got := posts.Load(); got != 1 {
		t.Errorf("the refused create must not reach the apiserver, got %d POSTs", got)
	}
}

// A context that expires while a request is in flight is not the limiter's
// refusal: the create may have reached the apiserver, so it is not Deferred
// (which records nothing). It is Unavailable, bounded by the scheduler's
// window, and the re-offer fences a pod the create may have made (ADR 0093).
func TestClassifyContextDeadlineIsNotDeferred(t *testing.T) {
	if got := classifyDispatchError(context.DeadlineExceeded); got != Unavailable {
		t.Errorf("a plain deadline must be Unavailable, got %s", got)
	}
}
