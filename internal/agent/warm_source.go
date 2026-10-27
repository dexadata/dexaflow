package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// dirCommandRunner is a CommandRunner that can run the task from a given
// working directory. The production execRunner is one; a warm worker confirms
// source delivery only when its runner is (ADR 0095).
type dirCommandRunner interface {
	InDir(dir string) CommandRunner
}

// attemptSourceDir is where a warm worker writes a source-mode assignment's
// dag.py, under the scratch every attempt starts from empty.
func (w *WarmRunner) attemptSourceDir() string {
	return filepath.Join(w.ScratchDir, "source")
}

// sourceDelivery reports whether this worker can run a source-mode attempt
// from the dag.py its assignment carries: its command runner can set the
// task's working directory.
func (w *WarmRunner) sourceDelivery() bool {
	_, ok := w.Cmd.(dirCommandRunner)
	return ok
}

// attemptCommand is the command runner for assignment a. Outside source mode
// it is the worker's own. A source-mode assignment carries the version's
// dag.py (ADR 0095): it is checked against its SHA-256, written as dag.py into
// a directory of the fresh scratch, and the task runs from there, as a
// dedicated source-mode pod runs from its projected dag.py (ADR 0067 §3), so
// `python -m leoflow_runtime` imports it. The file goes with the scratch.
//
// Only a source that does not match its digest is refused (errSourceMismatch):
// the attempt is placed again, and a damaged copy says nothing about this
// worker. Any other failure (a full scratch, a runner that cannot set the
// directory) is the worker's own, so it ends the worker like a failed scratch
// reset: the pod is replaced and the attempt reclaimed, instead of being
// refused and handed back to the same worker forever.
func (w *WarmRunner) attemptCommand(a *agentv1.WorkAssignment) (CommandRunner, error) {
	src := a.GetDagSource()
	if len(src) == 0 {
		return w.Cmd, nil
	}
	sum := sha256.Sum256(src)
	if got := hex.EncodeToString(sum[:]); got != a.GetDagSourceSha256() {
		return nil, fmt.Errorf("%w: the dag.py received has SHA-256 %s, the assignment says %q", errSourceMismatch, got, a.GetDagSourceSha256())
	}
	dr, ok := w.Cmd.(dirCommandRunner)
	if !ok {
		return nil, errors.New("this worker cannot run a source-mode attempt")
	}
	dir := w.attemptSourceDir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating %s: %w", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), src, 0o400); err != nil {
		return nil, fmt.Errorf("writing dag.py: %w", err)
	}
	return dr.InDir(dir), nil
}

// errSourceMismatch marks an assignment whose dag.py does not match its
// SHA-256, the one source failure the worker refuses instead of ending.
var errSourceMismatch = errors.New("dag.py does not match its digest")

// removeAttemptSource removes the dag.py an attempt ran from, when the
// worker does not sweep its whole scratch after every attempt. A worker that
// does (AttemptHome) removes it with the rest.
func (w *WarmRunner) removeAttemptSource() error {
	return os.RemoveAll(w.attemptSourceDir())
}

// refuseAssignment answers a with started=false, so the control plane
// reclaims it at once instead of waiting for its lease, then signals the slot
// free: a refused assignment leaves the worker out of the free set until it
// does. The worker keeps serving.
func (w *WarmRunner) refuseAssignment(stream agentv1.AgentService_AwaitAssignmentClient, a *agentv1.WorkAssignment) error {
	if err := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_Ack{
			Ack: &agentv1.AssignmentAck{AssignmentId: a.GetAssignmentId(), Started: false},
		},
	}); err != nil {
		return fmt.Errorf("refusing assignment %q: %w", a.GetAssignmentId(), err)
	}
	if err := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_SlotFree{SlotFree: &agentv1.SlotFree{}},
	}); err != nil {
		return fmt.Errorf("signaling slot free after refusing assignment %q: %w", a.GetAssignmentId(), err)
	}
	return nil
}
