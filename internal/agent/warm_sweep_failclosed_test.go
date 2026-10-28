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

// tmpBreakingCmd replaces the shared /tmp emptyDir with a regular file during its
// attempt, so the sweep after the attempt cannot list it.
type tmpBreakingCmd struct {
	sharedTmp string
	runs      int
}

func (c *tmpBreakingCmd) Run(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
	c.runs++
	if err := os.RemoveAll(c.sharedTmp); err != nil {
		return 1, err
	}
	if err := os.WriteFile(c.sharedTmp, []byte("x"), 0o600); err != nil {
		return 1, err
	}
	return 0, nil
}

// TestWarmWorkerPostAttemptSharedTmpSweepFailureStopsWorker: the same as the
// /dev/shm case for the /tmp emptyDir, which holds the generated dbt profile. The
// attempt itself was acked and run; only the next one is refused.
func TestWarmWorkerPostAttemptSharedTmpSweepFailureStopsWorker(t *testing.T) {
	sharedTmp := filepath.Join(t.TempDir(), "tmp")
	if err := os.Mkdir(sharedTmp, 0o700); err != nil {
		t.Fatal(err)
	}
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	cmd := &tmpBreakingCmd{sharedTmp: sharedTmp}
	// The scratch lives outside the broken dir so the failure comes from the
	// shared /tmp sweep and not from removing the scratch itself.
	w := failClosedRunner(stream, cmd, filepath.Join(t.TempDir(), "leoflow-warm-1"), sharedTmp, t.TempDir())

	err := w.Run(context.Background(), "dagver-1")

	if err == nil || !strings.Contains(err.Error(), `sweeping after assignment "asg-1"`) {
		t.Fatalf("Run error = %v, want the post-attempt sweep failure for asg-1", err)
	}
	if cmd.runs != 1 {
		t.Errorf("attempts run = %d, want 1", cmd.runs)
	}
	acks, slotFrees := sentKinds(stream.sent)
	if acks != 1 {
		t.Errorf("acks sent = %d, want 1: the first attempt was claimed before it ran", acks)
	}
	if slotFrees != 0 {
		t.Errorf("SlotFree sent = %d, want 0", slotFrees)
	}
}

// symlinkPlantingCmd plants, in each shared dir, a symlink to a directory outside
// it, the way a hostile attempt could aim the next sweep at files it must not
// touch.
type symlinkPlantingCmd struct{ dirs, targets []string }

func (c *symlinkPlantingCmd) Run(context.Context, []string, []string, io.Writer, io.Writer) (int, error) {
	for i, d := range c.dirs {
		if err := os.Symlink(c.targets[i], filepath.Join(d, "link")); err != nil {
			return 1, err
		}
	}
	return 0, nil
}

// TestWarmWorkerSweepRemovesSymlinksWithoutFollowingThem: the sweep removes a
// planted symlink itself and leaves what it points to alone, so an attempt cannot
// use the sweep to delete files outside /tmp or /dev/shm.
func TestWarmWorkerSweepRemovesSymlinksWithoutFollowingThem(t *testing.T) {
	sharedTmp, shm := t.TempDir(), t.TempDir()
	outTmp, outShm := t.TempDir(), t.TempDir()
	for _, d := range []string{outTmp, outShm} {
		if err := os.WriteFile(filepath.Join(d, "precious"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	stream := &fakeAssignmentStream{
		ctx:         context.Background(),
		assignments: []*agentv1.WorkAssignment{{AssignmentId: "asg-1", AttemptToken: "tok-1"}},
	}
	cmd := &symlinkPlantingCmd{dirs: []string{sharedTmp, shm}, targets: []string{outTmp, outShm}}
	w := failClosedRunner(stream, cmd, filepath.Join(sharedTmp, "leoflow-warm-1"), sharedTmp, shm)

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, d := range []string{sharedTmp, shm} {
		if _, err := os.Lstat(filepath.Join(d, "link")); !os.IsNotExist(err) {
			t.Errorf("symlink in %s survived the sweep (lstat err = %v)", d, err)
		}
	}
	for _, d := range []string{outTmp, outShm} {
		if _, err := os.Stat(filepath.Join(d, "precious")); err != nil {
			t.Errorf("the sweep followed a symlink and removed %s/precious: %v", d, err)
		}
	}
}

// TestWarmWorkerWithoutAttemptHomeIgnoresBrokenSharedDirs: the shared dir sweeps
// belong to the read-only root mode only. Without AttemptHome a pod whose
// /dev/shm or /tmp cannot be listed keeps serving, so the fail-closed branch
// never takes down workers that do not opt in.
func TestWarmWorkerWithoutAttemptHomeIgnoresBrokenSharedDirs(t *testing.T) {
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	cmd := &countingCmd{}
	w := failClosedRunner(stream, cmd, filepath.Join(t.TempDir(), "leoflow-warm-1"), notADir(t), notADir(t))
	w.AttemptHome = false

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if cmd.runs != 2 {
		t.Errorf("attempts run = %d, want 2", cmd.runs)
	}
	if acks, slotFrees := sentKinds(stream.sent); acks != 2 || slotFrees != 2 {
		t.Errorf("acks = %d, SlotFree = %d, want 2 and 2", acks, slotFrees)
	}
}
