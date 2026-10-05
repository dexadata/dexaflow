package agentrpc

import (
	"context"
	"testing"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/metadata"
)

// TestGetTaskSpecCarriesAttemptEpoch: the task spec tells the agent which
// execution it is, read from its own verified token (ADR 0051 amendment). A
// legacy token reports epoch 0, the value the legacy rule assigns it.
func TestGetTaskSpecCarriesAttemptEpoch(t *testing.T) {
	srv, a := newServer(&fakeStore{spec: TaskSpec{Operator: "python"}})
	for _, tc := range []struct {
		name string
		has  bool
		n    int
		want int64
	}{{"epoch 3", true, 3, 3}, {"legacy", false, 0, 0}} {
		t.Run(tc.name, func(t *testing.T) {
			id := testIdentity()
			id.HasAttemptEpoch, id.AttemptEpoch = tc.has, tc.n
			token, err := a.IssueAgentToken(id, time.Hour)
			if err != nil {
				t.Fatalf("IssueAgentToken: %v", err)
			}
			ctx := metadata.NewIncomingContext(context.Background(), metadata.Pairs("authorization", "Bearer "+token))
			spec, err := srv.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
			if err != nil {
				t.Fatalf("GetTaskSpec: %v", err)
			}
			if spec.GetAttemptEpoch() != tc.want {
				t.Errorf("spec attempt_epoch = %d, want %d", spec.GetAttemptEpoch(), tc.want)
			}
		})
	}
}
