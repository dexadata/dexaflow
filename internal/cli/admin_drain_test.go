package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// drainServer serves a two-DAG control plane. It records pause PATCHes and, for
// dagRuns, returns `runningPolls` polls' worth of a running run before draining
// to empty — so a test can prove drain pauses first, then polls until clear.
func drainServer(t *testing.T, runningPolls int32) (*httptest.Server, *pauseRecorder, *int32) {
	t.Helper()
	rec := &pauseRecorder{paused: map[string]bool{}}
	var runsPolls int32
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/dags", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			http.NotFound(w, r)
			return
		}
		dags := []apiclient.DAG{{DagId: strptr("etl")}, {DagId: strptr("reports")}}
		total := 2
		writeJSON(t, w, apiclient.DAGCollection{Dags: &dags, TotalEntries: &total})
	})
	var mu sync.Mutex
	mux.HandleFunc("/api/v2/dags/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPatch {
			id := strings.TrimPrefix(strings.TrimSuffix(r.URL.Path, "/dagRuns"), "/api/v2/dags/")
			rec.record(id, true)
			writeJSON(t, w, apiclient.DAG{DagId: strptr(id), IsPaused: boolptr(true)})
			return
		}
		// dagRuns listing: only "etl" ever has a running run, and only for the
		// first runningPolls polls.
		mu.Lock()
		poll := atomic.AddInt32(&runsPolls, 1)
		mu.Unlock()
		var runs []apiclient.DAGRun
		if strings.Contains(r.URL.Path, "/dags/etl/") && (poll+1)/2 <= runningPolls {
			runs = []apiclient.DAGRun{{DagRunId: strptr("run-1"), DagId: strptr("etl"), State: runState(apiclient.DAGRunStateRunning)}}
		}
		total := len(runs)
		writeJSON(t, w, apiclient.DAGRunCollection{DagRuns: &runs, TotalEntries: &total})
	})
	return httptest.NewServer(mux), rec, &runsPolls
}

func TestWaitForRunsToDrainClears(t *testing.T) {
	// Running for the first poll round, empty afterwards.
	srv, _, _ := drainServer(t, 1)
	defer srv.Close()

	remaining, timedOut, err := waitForRunsToDrain(context.Background(), newTestClient(t, srv.URL),
		5*time.Second, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForRunsToDrain: %v", err)
	}
	if timedOut {
		t.Errorf("timedOut = true, want the runs to drain before the deadline")
	}
	if len(remaining) != 0 {
		t.Errorf("remaining = %+v, want empty", remaining)
	}
}

func TestWaitForRunsToDrainTimesOut(t *testing.T) {
	// A very high running-poll count means runs never drain within the window.
	srv, _, _ := drainServer(t, 1000)
	defer srv.Close()

	remaining, timedOut, err := waitForRunsToDrain(context.Background(), newTestClient(t, srv.URL),
		30*time.Millisecond, 5*time.Millisecond)
	if err != nil {
		t.Fatalf("waitForRunsToDrain: %v", err)
	}
	if !timedOut {
		t.Errorf("timedOut = false, want true when runs never drain")
	}
	if len(remaining) == 0 {
		t.Errorf("remaining is empty, want the still-running run reported on timeout")
	}
}

// TestAdminDrainPausesThenDrains drives the whole command: it must pause every
// DAG first, then poll until the active run clears, and exit zero.
func TestAdminDrainPausesThenDrains(t *testing.T) {
	srv, rec, _ := drainServer(t, 1)
	defer srv.Close()

	out, _, err := run(t, "admin", "drain", "--timeout", "5s", "--poll-interval", "5ms",
		"--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("admin drain: %v", err)
	}
	if got := strings.Join(rec.ids(), ","); got != "etl,reports" {
		t.Errorf("paused ids = %q, want etl,reports", got)
	}
	if !strings.Contains(out, "Drain complete") {
		t.Errorf("output = %q, want a completion line", out)
	}
}

func TestAdminDrainTimesOutNonZero(t *testing.T) {
	srv, rec, _ := drainServer(t, 1000)
	defer srv.Close()

	out, _, err := run(t, "admin", "drain", "--timeout", "30ms", "--poll-interval", "5ms",
		"--server", srv.URL, "--token", "x")
	if err == nil {
		t.Errorf("expected non-zero exit when drain times out")
	}
	// It must still have paused everything before giving up.
	if got := strings.Join(rec.ids(), ","); got != "etl,reports" {
		t.Errorf("paused ids = %q, want etl,reports even on timeout", got)
	}
	if !strings.Contains(out, "run-1") {
		t.Errorf("output = %q, want the still-running run reported", out)
	}
}

func TestAdminDrainNoWaitSkipsPolling(t *testing.T) {
	srv, rec, pollPtr := drainServer(t, 1000)
	defer srv.Close()

	_, _, err := run(t, "admin", "drain", "--no-wait", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("admin drain --no-wait: %v", err)
	}
	if got := strings.Join(rec.ids(), ","); got != "etl,reports" {
		t.Errorf("paused ids = %q, want etl,reports", got)
	}
	if p := atomic.LoadInt32(pollPtr); p != 0 {
		t.Errorf("dagRuns was polled %d times, want 0 under --no-wait", p)
	}
}
