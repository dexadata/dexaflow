package agent

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// The X3.2 sweeps of the shared /tmp emptyDir and /dev/shm are fail-closed: a
// sweep that cannot run ends the worker instead of serving the next attempt on
// a filesystem it could not clean (#1611). These tests break the sweep with a
// path that is not a directory, which fails os.ReadDir for any user, root
// included, so they need no injection seam and no permission tricks.

// notADir returns the path of a regular file, standing in for a shared dir the
// sweep can no longer list.
func notADir(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "not-a-dir")
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// countingCmd counts the attempts it ran.
type countingCmd struct{ runs int }

func (c *countingCmd) Run(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
	c.runs++
	return 0, nil
}

// sentKinds counts the acks and SlotFree messages the stream carried.
func sentKinds(msgs []*agentv1.WorkerMessage) (acks, slotFrees int) {
	for _, m := range msgs {
		switch {
		case m.GetAck() != nil:
			acks++
		case m.GetSlotFree() != nil:
			slotFrees++
		}
	}
	return acks, slotFrees
}

func failClosedRunner(stream *fakeAssignmentStream, cmd CommandRunner, scratch, sharedTmp, shm string) *WarmRunner {
	tokens := NewTokenSource("bootstrap")
	client := &warmFake{fakeClient: &fakeClient{}, stream: stream, tokens: tokens, specs: warmSpecs(len(stream.assignments))}
	return &WarmRunner{
		StreamClient:  client,
		WorkClient:    client,
		AttemptTokens: tokens,
		Cmd:           cmd,
		Env:           []string{"PATH=/usr/bin"},
		ScratchDir:    scratch,
		AttemptHome:   true,
		SharedTmpDir:  sharedTmp,
		SharedMemDir:  shm,
	}
}

// TestWarmWorkerPreAckSharedMemSweepFailureStopsWorker: when /dev/shm cannot be
// swept before an attempt, the worker never acks it (the control plane reclaims
// it by lease expiry) and never runs it.
func TestWarmWorkerPreAckSharedMemSweepFailureStopsWorker(t *testing.T) {
	sharedTmp := t.TempDir()
	stream := &fakeAssignmentStream{
		ctx:         context.Background(),
		assignments: []*agentv1.WorkAssignment{{AssignmentId: "asg-1", AttemptToken: "tok-1"}},
	}
	cmd := &countingCmd{}
	w := failClosedRunner(stream, cmd, filepath.Join(sharedTmp, "leoflow-warm-1"), sharedTmp, notADir(t))

	err := w.Run(context.Background(), "dagver-1")

	if err == nil || !strings.Contains(err.Error(), "resetting scratch for assignment") {
		t.Fatalf("Run error = %v, want the pre-ack sweep failure", err)
	}
	if acks, _ := sentKinds(stream.sent); acks != 0 {
		t.Errorf("acks sent = %d, want 0: an attempt on an unswept worker must not be claimed", acks)
	}
	if cmd.runs != 0 {
		t.Errorf("attempts run = %d, want 0", cmd.runs)
	}
}

// TestWarmWorkerPreAckSharedTmpSweepFailureStopsWorker: the same for the /tmp
// emptyDir that holds the scratch.
func TestWarmWorkerPreAckSharedTmpSweepFailureStopsWorker(t *testing.T) {
	stream := &fakeAssignmentStream{
		ctx:         context.Background(),
		assignments: []*agentv1.WorkAssignment{{AssignmentId: "asg-1", AttemptToken: "tok-1"}},
	}
	cmd := &countingCmd{}
	w := failClosedRunner(stream, cmd, filepath.Join(t.TempDir(), "leoflow-warm-1"), notADir(t), t.TempDir())

	err := w.Run(context.Background(), "dagver-1")

	if err == nil || !strings.Contains(err.Error(), "resetting scratch for assignment") {
		t.Fatalf("Run error = %v, want the pre-ack sweep failure", err)
	}
	if acks, _ := sentKinds(stream.sent); acks != 0 {
		t.Errorf("acks sent = %d, want 0", acks)
	}
	if cmd.runs != 0 {
		t.Errorf("attempts run = %d, want 0", cmd.runs)
	}
}

// shmBreakingCmd replaces the shared memory dir with a regular file during its
// attempt, so the sweep after the attempt cannot list it.
type shmBreakingCmd struct {
	shm  string
	runs int
}

func (c *shmBreakingCmd) Run(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
	c.runs++
	if err := os.RemoveAll(c.shm); err != nil {
		return 1, err
	}
	if err := os.WriteFile(c.shm, []byte("x"), 0o600); err != nil {
		return 1, err
	}
	return 0, nil
}

// TestWarmWorkerPostAttemptSweepFailureStopsWorker: when the sweep after an
// attempt fails, the worker ends before it reports SlotFree, so no next attempt
// is dispatched to it and the second assignment never runs there.
func TestWarmWorkerPostAttemptSweepFailureStopsWorker(t *testing.T) {
	sharedTmp := t.TempDir()
	shm := filepath.Join(t.TempDir(), "shm")
	if err := os.Mkdir(shm, 0o700); err != nil {
		t.Fatal(err)
	}
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	cmd := &shmBreakingCmd{shm: shm}
	w := failClosedRunner(stream, cmd, filepath.Join(sharedTmp, "leoflow-warm-1"), sharedTmp, shm)

	err := w.Run(context.Background(), "dagver-1")

	if err == nil || !strings.Contains(err.Error(), "sweeping after assignment") {
		t.Fatalf("Run error = %v, want the post-attempt sweep failure", err)
	}
	if cmd.runs != 1 {
		t.Errorf("attempts run = %d, want 1: the worker must stop after the failed sweep", cmd.runs)
	}
	if _, slotFrees := sentKinds(stream.sent); slotFrees != 0 {
		t.Errorf("SlotFree sent = %d, want 0: an unswept worker must not ask for more work", slotFrees)
	}
}
