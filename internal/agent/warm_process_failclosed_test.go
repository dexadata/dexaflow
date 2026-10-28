package agent

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// The process isolation steps of the warm worker are fail-closed like the
// filesystem sweeps (#1611): a worker that cannot harden itself serves nothing,
// and a worker whose post-attempt process sweep fails ends before it asks for
// more work. These tests make each step fail through the WarmRunner seam.

var errInjected = errors.New("injected failure")

// TestWarmWorkerHardeningFailureServesNothing: when hardenWarmProcess fails the
// worker returns before it registers or opens the assignment stream, so no
// attempt can run in a container whose agent is dumpable or not the subreaper.
func TestWarmWorkerHardeningFailureServesNothing(t *testing.T) {
	stream := &fakeAssignmentStream{
		ctx:         context.Background(),
		assignments: []*agentv1.WorkAssignment{{AssignmentId: "asg-1", AttemptToken: "tok-1"}},
	}
	cmd := &countingCmd{}
	w := failClosedRunner(stream, cmd, filepath.Join(t.TempDir(), "leoflow-warm-1"), t.TempDir(), t.TempDir())
	fake := w.StreamClient.(*warmFake)
	sweeps := 0
	w.harden = func() error { return errInjected }
	w.sweepProcs = func() error { sweeps++; return nil }

	err := w.Run(context.Background(), "dagver-1")

	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "hardening the warm worker process") {
		t.Fatalf("Run error = %v, want the hardening failure", err)
	}
	if fake.registered {
		t.Error("the worker registered after hardening failed")
	}
	if stream.idx != 0 {
		t.Errorf("assignments received = %d, want 0", stream.idx)
	}
	if len(stream.sent) != 0 {
		t.Errorf("messages sent on the stream = %d, want 0", len(stream.sent))
	}
	if cmd.runs != 0 || sweeps != 0 {
		t.Errorf("attempts run = %d, process sweeps = %d, want 0 and 0", cmd.runs, sweeps)
	}
}

// TestWarmWorkerProcessSweepFailureStopsWorker: when the process sweep after an
// attempt fails (a descendant survived, or /proc could not be walked) the worker
// ends before SlotFree, so the second assignment never runs next to a process
// the first one left. The process sweep does not depend on AttemptHome, so it is
// off here to show that the branch holds without the filesystem sweeps.
func TestWarmWorkerProcessSweepFailureStopsWorker(t *testing.T) {
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	cmd := &countingCmd{}
	w := failClosedRunner(stream, cmd, filepath.Join(t.TempDir(), "leoflow-warm-1"), t.TempDir(), t.TempDir())
	w.AttemptHome = false
	hardened := false
	sweeps := 0
	w.harden = func() error { hardened = true; return nil }
	w.sweepProcs = func() error { sweeps++; return errInjected }

	err := w.Run(context.Background(), "dagver-1")

	if !errors.Is(err, errInjected) || !strings.Contains(err.Error(), `sweeping processes after assignment "asg-1"`) {
		t.Fatalf("Run error = %v, want the process sweep failure for asg-1", err)
	}
	if !hardened {
		t.Error("the injected hardening step never ran")
	}
	if cmd.runs != 1 || sweeps != 1 {
		t.Errorf("attempts run = %d, process sweeps = %d, want 1 and 1", cmd.runs, sweeps)
	}
	acks, slotFrees := sentKinds(stream.sent)
	if acks != 1 {
		t.Errorf("acks sent = %d, want 1: the first attempt was claimed before it ran", acks)
	}
	if slotFrees != 0 {
		t.Errorf("SlotFree sent = %d, want 0: a worker with a survivor must not ask for more work", slotFrees)
	}
}

// TestWarmWorkerProcessSweepRunsAfterEveryAttempt: with a sweep that succeeds the
// worker keeps serving, and the sweep runs once per attempt, before its SlotFree.
func TestWarmWorkerProcessSweepRunsAfterEveryAttempt(t *testing.T) {
	stream := &fakeAssignmentStream{
		ctx: context.Background(),
		assignments: []*agentv1.WorkAssignment{
			{AssignmentId: "asg-1", AttemptToken: "tok-1"},
			{AssignmentId: "asg-2", AttemptToken: "tok-2"},
		},
	}
	cmd := &countingCmd{}
	w := failClosedRunner(stream, cmd, filepath.Join(t.TempDir(), "leoflow-warm-1"), t.TempDir(), t.TempDir())
	var slotFreesAtSweep []int
	w.harden = func() error { return nil }
	w.sweepProcs = func() error {
		_, sf := sentKinds(stream.sent)
		slotFreesAtSweep = append(slotFreesAtSweep, sf)
		return nil
	}

	if err := w.Run(context.Background(), "dagver-1"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(slotFreesAtSweep) != 2 || slotFreesAtSweep[0] != 0 || slotFreesAtSweep[1] != 1 {
		t.Errorf("SlotFree count seen by each sweep = %v, want [0 1]: each sweep runs before its attempt's SlotFree", slotFreesAtSweep)
	}
}
