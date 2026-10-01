package agent

import (
	"context"
	"io"
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// serverClosedSink models the agent's gRPC log sink after the control plane
// ended the StreamLogs stream mid-task (an orderly shutdown now does this on
// SIGTERM): the first sends succeed, then every Send fails with io.EOF the way a
// finished client stream does, and Close returns nil once the drain sees the
// stream's terminal status.
type serverClosedSink struct {
	okSends int
	sends   int
	closed  bool
}

func (s *serverClosedSink) Send(*agentv1.LogLine) error {
	s.sends++
	if s.sends > s.okSends {
		return io.EOF
	}
	return nil
}

func (s *serverClosedSink) Close() error { s.closed = true; return nil }

// TestRunnerSucceedsWhenLogStreamClosesMidTask pins the contract the bounded
// control-plane shutdown relies on: the server ending a task's log stream must
// cost only the log lines emitted afterwards, never the task. The run completes,
// the outcome is reported as success, and the sink is still Closed.
func TestRunnerSucceedsWhenLogStreamClosesMidTask(t *testing.T) {
	client := &fakeClient{spec: &agentv1.TaskSpec{Operator: "python", Entrypoint: "dag:hello"}}
	cmd := &fakeCmd{stdout: "line one\nline two\nline three\n"}
	sink := &serverClosedSink{okSends: 2} // the stream dies during the task's output
	r := &Runner{Client: client, Cmd: cmd, Sink: sink, Hostname: "pod-1", Version: "test"}

	if err := r.Run(context.Background()); err != nil {
		t.Fatalf("Run failed because the log stream closed mid-task: %v", err)
	}
	if last := client.states[len(client.states)-1]; last != agentv1.TaskState_TASK_STATE_SUCCESS {
		t.Errorf("final state = %v, want success (log shipping is best-effort)", last)
	}
	if sink.sends <= sink.okSends {
		t.Errorf("sends = %d; the runner should have kept emitting after the stream failed", sink.sends)
	}
	if !sink.closed {
		t.Error("the sink must still be Closed so the drain runs and the connection can be torn down")
	}
}
