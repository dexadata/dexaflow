package agent

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// sourceRuns is what sourceProbeCmd saw: for each attempt, the working
// directory it ran from ("" for the agent's own) and the dag.py there.
type sourceRuns struct {
	dirs  []string
	dagPy []string
}

// sourceProbeCmd is a CommandRunner that can run from a working directory
// (dirCommandRunner), as the production execRunner can.
type sourceProbeCmd struct {
	dir  string
	runs *sourceRuns
}

func (c sourceProbeCmd) InDir(dir string) CommandRunner {
	c.dir = dir
	return c
}

func (c sourceProbeCmd) Run(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
	c.runs.dirs = append(c.runs.dirs, c.dir)
	b, _ := os.ReadFile(filepath.Join(c.dir, "dag.py"))
	c.runs.dagPy = append(c.runs.dagPy, string(b))
	return 0, nil
}

func sourceAssignment(id, source string) *agentv1.WorkAssignment {
	sum := sha256.Sum256([]byte(source))
	return &agentv1.WorkAssignment{AssignmentId: id, AttemptToken: "tok-" + id,
		DagSource: []byte(source), DagSourceSha256: hex.EncodeToString(sum[:])}
}

func sourceWorker(t *testing.T, stream *fakeAssignmentStream, cmd CommandRunner, specs int) *WarmRunner {
	t.Helper()
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: warmSpecs(specs)}
	return &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: tokens,
		Cmd:           cmd,
		Hostname:      "warm-pod-1",
		Version:       "test",
		Env:           []string{"PATH=/usr/bin"},
		ScratchDir:    filepath.Join(t.TempDir(), "scratch"),
	}
}

// ADR 0095: a source-mode assignment carries the version's dag.py. The worker
// runs the attempt from a directory holding it, as a dedicated source-mode pod
// runs from its projected dag.py, and removes it once the attempt is done. An
// assignment without a source runs from the worker's own directory.
func TestWarmWorkerRunsASourceModeAttemptFromItsDagPy(t *testing.T) {
	const src = "from airflow.sdk import dag\n# v1\n"
	stream := &fakeAssignmentStream{ctx: context.Background(), assignments: []*agentv1.WorkAssignment{
		sourceAssignment("asg-1", src),
		{AssignmentId: "asg-2", AttemptToken: "tok-2"},
	}}
	runs := &sourceRuns{}
	w := sourceWorker(t, stream, sourceProbeCmd{runs: runs}, 2)

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if len(runs.dirs) != 2 {
		t.Fatalf("attempts run = %d, want 2", len(runs.dirs))
	}
	if want := w.attemptSourceDir(); runs.dirs[0] != want || runs.dagPy[0] != src {
		t.Errorf("source attempt ran from %q with dag.py %q, want %q with %q", runs.dirs[0], runs.dagPy[0], want, src)
	}
	if runs.dirs[1] != "" {
		t.Errorf("an attempt without a source ran from %q, want the worker's own directory", runs.dirs[1])
	}
	if _, err := os.Stat(filepath.Join(w.attemptSourceDir(), "dag.py")); !os.IsNotExist(err) {
		t.Errorf("dag.py is still on disk after the attempt: %v", err)
	}
}

// A source that does not match its SHA-256 is refused before anything runs:
// the worker answers started=false, frees its slot so the control plane can
// place the next attempt on it, and keeps serving.
func TestWarmWorkerRefusesASourceThatDoesNotMatchItsDigest(t *testing.T) {
	bad := sourceAssignment("asg-1", "print('a')\n")
	bad.DagSource = []byte("print('b')\n")
	stream := &fakeAssignmentStream{ctx: context.Background(), assignments: []*agentv1.WorkAssignment{
		bad,
		sourceAssignment("asg-2", "print('c')\n"),
	}}
	runs := &sourceRuns{}
	w := sourceWorker(t, stream, sourceProbeCmd{runs: runs}, 1)

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("WarmRunner.Run: %v", err)
	}
	if len(runs.dagPy) != 1 || runs.dagPy[0] != "print('c')\n" {
		t.Fatalf("attempts run = %q, want only the second one", runs.dagPy)
	}
	var acks []string
	for _, m := range stream.sent {
		switch {
		case m.GetAck() != nil:
			acks = append(acks, m.GetAck().GetAssignmentId()+"="+map[bool]string{true: "started", false: "refused"}[m.GetAck().GetStarted()])
		case m.GetSlotFree() != nil:
			acks = append(acks, "free")
		}
	}
	if got, want := strings.Join(acks, " "), "asg-1=refused free asg-2=started free"; got != want {
		t.Errorf("worker messages = %q, want %q", got, want)
	}
}

// A source the worker cannot stage is the worker's own failure, not the
// attempt's: it ends the worker (the pod is replaced and the attempt
// reclaimed) instead of refusing the attempt, which would hand it back to
// the same worker forever.
func TestWarmWorkerEndsWhenItCannotStageTheSource(t *testing.T) {
	stream := &fakeAssignmentStream{ctx: context.Background(), assignments: []*agentv1.WorkAssignment{
		sourceAssignment("asg-1", "print(1)\n"),
	}}
	w := sourceWorker(t, stream, &scratchProbeCmd{}, 1)

	if err := w.Run(context.Background(), "dagver-1"); err == nil {
		t.Fatal("WarmRunner.Run = nil, want the staging failure to end the worker")
	}
	for _, m := range stream.sent {
		if m.GetAck() != nil {
			t.Errorf("the worker answered the assignment (%+v); it must end without acking", m.GetAck())
		}
	}
}

// A worker confirms source delivery only when its command runner can set the
// task's working directory; the production runner can.
func TestWarmWorkerRegisterCarriesSourceDelivery(t *testing.T) {
	for name, tc := range map[string]struct {
		cmd  CommandRunner
		want bool
	}{
		"exec runner":      {NewExecRunner(), true},
		"dir-aware runner": {sourceProbeCmd{runs: &sourceRuns{}}, true},
		"plain runner":     {&scratchProbeCmd{}, false},
	} {
		t.Run(name, func(t *testing.T) {
			stream := &fakeAssignmentStream{ctx: context.Background()}
			w := sourceWorker(t, stream, tc.cmd, 0)
			if err := w.Run(context.Background(), "dagver-1"); err != nil {
				t.Fatalf("WarmRunner.Run: %v", err)
			}
			if got := stream.sent[0].GetRegister().GetSourceDelivery(); got != tc.want {
				t.Errorf("WorkerRegister.source_delivery = %v, want %v", got, tc.want)
			}
		})
	}
}

// The production runner starts the task in the directory InDir names.
func TestExecRunnerInDirSetsTheWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	r := NewExecRunner().(dirCommandRunner).InDir(dir)
	var out bytes.Buffer
	if code, err := r.Run(context.Background(), []string{"pwd", "-P"}, []string{"PATH=/usr/bin:/bin"}, &out, io.Discard); err != nil || code != 0 {
		t.Fatalf("pwd: code %d, err %v", code, err)
	}
	want, _ := filepath.EvalSymlinks(dir)
	if got := strings.TrimSpace(out.String()); got != want {
		t.Errorf("task ran in %q, want %q", got, want)
	}
}
