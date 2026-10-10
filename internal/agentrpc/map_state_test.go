package agentrpc

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// TestMapStateCoversEveryReportedTransition pins the agent → domain state
// translation. The agent's gRPC contract carries a proto enum; the control
// plane consumes a domain string. A missing case here makes a real task
// transition (e.g. SUCCESS reported by the agent) drop on the floor with no
// state update — visible only as a "stuck running" task in the UI. The unit
// test makes every known mapping explicit, and the default catches an
// unsupported state with InvalidArgument (so a forwards-compatible agent
// reporting a new state hits a loud error instead of silently mapping to
// "running" or empty).
func TestMapStateCoversEveryReportedTransition(t *testing.T) {
	cases := []struct {
		in   agentv1.TaskState
		want domain.TaskState
	}{
		{agentv1.TaskState_TASK_STATE_RUNNING, domain.TaskStateRunning},
		{agentv1.TaskState_TASK_STATE_SUCCESS, domain.TaskStateSuccess},
		{agentv1.TaskState_TASK_STATE_FAILED, domain.TaskStateFailed},
		{agentv1.TaskState_TASK_STATE_SKIPPED, domain.TaskStateSkipped},
		{agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE, domain.TaskStateUpForReschedule},
	}
	for _, tc := range cases {
		t.Run(tc.in.String(), func(t *testing.T) {
			got, err := mapState(tc.in)
			if err != nil {
				t.Fatalf("mapState(%v) error: %v", tc.in, err)
			}
			if got != tc.want {
				t.Errorf("mapState(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}

	t.Run("unsupported state returns InvalidArgument", func(t *testing.T) {
		got, err := mapState(agentv1.TaskState_TASK_STATE_UNSPECIFIED)
		if err == nil {
			t.Fatalf("expected error, got state %q", got)
		}
		st, ok := status.FromError(err)
		if !ok {
			t.Fatalf("expected gRPC status error, got %v (type %T)", err, err)
		}
		if st.Code() != codes.InvalidArgument {
			t.Errorf("code = %v, want InvalidArgument", st.Code())
		}
		if !strings.Contains(st.Message(), "unsupported task state") {
			t.Errorf("message %q does not name 'unsupported task state'", st.Message())
		}
	})
}
