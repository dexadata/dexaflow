package agentrpc_test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// failoverFixture is what a handler sees while Postgres fails over: the
// primary was told to shut down, wrapped the way the storage layer wraps it.
func failoverFixture() error {
	return fmt.Errorf("loading run: %w", &pgconn.PgError{
		Severity: "FATAL", Code: "57P01",
		Message: "terminating connection due to administrator command",
	})
}

// TestTransientStoreErrorsReachTheAgentAsUnavailable is R-01's server half.
// A database or Redis outage behind an agent RPC used to reach the agent as
// Internal, the code for a failure that repeats, so the agent gave up and the
// task failed as the user's. A store failure that a retry can outlast now
// reaches it as Unavailable, which the agent retries, with the same redacted
// message: the code changes, the disclosure rule does not.
func TestTransientStoreErrorsReachTheAgentAsUnavailable(t *testing.T) {
	cases := []struct {
		name   string
		poison func(*leakFakes)
		call   func(context.Context, agentv1.AgentServiceClient) error
		wantOp string
	}{
		{
			name:   "GetTaskSpec during a database failover",
			poison: func(f *leakFakes) { f.store.specErr = failoverFixture() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
				return err
			},
			wantOp: "loading task spec",
		},
		{
			name:   "ReportState during a database failover",
			poison: func(f *leakFakes) { f.store.reportErr = failoverFixture() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.ReportState(ctx, &agentv1.ReportStateRequest{State: agentv1.TaskState_TASK_STATE_RUNNING})
				return err
			},
			wantOp: "recording state",
		},
		{
			name:   "PushXCom while the xcom store is down",
			poison: func(f *leakFakes) { f.xcom.pushErr = failoverFixture() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.PushXCom(ctx, &agentv1.PushXComRequest{Key: "return_value", Value: []byte(`"v"`)})
				return err
			},
			wantOp: "storing xcom",
		},
		{
			name:   "FetchXCom while the xcom store is down",
			poison: func(f *leakFakes) { f.xcom.fetchErr = failoverFixture() },
			call: func(ctx context.Context, c agentv1.AgentServiceClient) error {
				_, err := c.FetchXCom(ctx, &agentv1.FetchXComRequest{UpstreamTaskId: "upstream", Key: "return_value"})
				return err
			},
			wantOp: "reading xcom",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newLeakFakes()
			tc.poison(f)
			client, ctx, _ := startLeakServer(t, f)

			err := tc.call(ctx, client)
			if got := status.Code(err); got != codes.Unavailable {
				t.Fatalf("status code = %v, want Unavailable (%v)", got, err)
			}
			msg := status.Convert(err).Message()
			if !strings.Contains(msg, tc.wantOp) {
				t.Errorf("message %q does not name the operation %q", msg, tc.wantOp)
			}
			for _, leak := range []string{"57P01", "administrator command", "SQLSTATE"} {
				if strings.Contains(msg, leak) {
					t.Errorf("the status the task pod receives leaks %q: %s", leak, msg)
				}
			}
		})
	}
}
