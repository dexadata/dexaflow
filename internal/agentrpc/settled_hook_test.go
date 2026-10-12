package agentrpc

import (
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/types/known/timestamppb"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// TestReportStateCallsSettledHookOnTerminalStates pins that a task settling is
// announced (so the scheduler can tick early) only once the terminal state is
// recorded, and never for a progress report, a reschedule, a stale report or a
// failed write.
func TestReportStateCallsSettledHookOnTerminalStates(t *testing.T) {
	cases := []struct {
		name  string
		store *fakeStore
		req   *agentv1.ReportStateRequest
		want  int
	}{
		{"success", &fakeStore{}, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}, 1},
		{"failed", &fakeStore{}, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_FAILED, ExitCode: 1}, 1},
		{"running", &fakeStore{}, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING}, 0},
		{"reschedule", &fakeStore{}, &agentv1.ReportStateRequest{
			State: agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE, RescheduleAt: timestamppb.New(time.Now().Add(time.Minute)),
		}, 0},
		{"stale", &fakeStore{reportErr: ErrStaleReport}, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}, 0},
		{"write error", &fakeStore{reportErr: errors.New("db down")}, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv, a := newServer(tc.store)
			calls := 0
			srv.SetSettledHook(func() { calls++ })
			_, _ = srv.ReportState(ctxWithToken(t, a), tc.req)
			if calls != tc.want {
				t.Errorf("settled hook called %d times, want %d", calls, tc.want)
			}
		})
	}
}

// Without a hook (the default), a terminal report behaves exactly as before.
func TestReportStateWithoutSettledHook(t *testing.T) {
	store := &fakeStore{}
	srv, a := newServer(store)
	if _, err := srv.ReportState(ctxWithToken(t, a), &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_SUCCESS}); err != nil {
		t.Fatalf("ReportState: %v", err)
	}
	if len(store.reported) != 1 {
		t.Fatalf("expected one reported state, got %d", len(store.reported))
	}
}
