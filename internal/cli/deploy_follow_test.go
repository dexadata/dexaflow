package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestDeployFollowNeedsTrigger: deploy is independent of runs, so --follow
// does not start a run by itself.
func TestDeployFollowNeedsTrigger(t *testing.T) {
	_, _, err := run(t, "deploy", t.TempDir(), "--follow", "--server", "http://127.0.0.1:1", "--token", "x", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--follow needs --trigger") {
		t.Fatalf("err = %v, want --follow to require --trigger", err)
	}
}

// TestDeployFollowRefusesAll: one exit code follows one run.
func TestDeployFollowRefusesAll(t *testing.T) {
	_, _, err := run(t, "deploy", "--all", "--trigger", "--follow", "--server", "http://127.0.0.1:1", "--token", "x", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--follow follows one run") {
		t.Fatalf("err = %v, want --follow refused with --all", err)
	}
}

// TestDeployTriggerFollowExitsWithTheRunResult drives `deploy --trigger
// --follow` through the real command on the already-deployed path (no build):
// it triggers, follows the run it started, and exits with the run-failed code.
func TestDeployTriggerFollowExitsWithTheRunResult(t *testing.T) {
	fastFollow(t)
	dir, builder, _ := deployFixture(t)
	label := labelOf(t, dir)
	deploys := &deployServer{current: label, registered: []string{label}}
	runs := &fakeRunServer{steps: []runStep{
		{state: "failed", tis: []fakeTI{{task: "extract", state: "failed", try: 1}}},
	}, logs: map[string]string{"extract/1": "boom\n"}}
	mux := http.NewServeMux()
	mux.Handle("/api/v2/dags/etl/details", deploys)
	mux.Handle("/api/v2/dags/etl/dagVersions", deploys)
	mux.Handle("/", runs)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--trigger", "--follow")
	if err == nil {
		t.Fatalf("a failed run must fail deploy --follow:\n%s", out)
	}
	if got := exitCodeOf(err); got != exitRunFailed {
		t.Errorf("exit code = %d (err %v), want %d", got, err, exitRunFailed)
	}
	if !strings.Contains(err.Error(), "failed task: extract") || !strings.Contains(out, "| boom") {
		t.Errorf("err %q / output %q should name extract and show its logs", err, out)
	}
}
