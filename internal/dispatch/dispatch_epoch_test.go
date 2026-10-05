package dispatch

import (
	"context"
	"testing"
)

// TestDispatchMintsAttemptEpoch: the dispatcher mints the agent's identity,
// the pod request and a warm assignment from the epoch the resolver claimed
// (ADR 0051 amendment), with the presence bit set so the token is never read
// as a legacy one.
func TestDispatchMintsAttemptEpoch(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{
		TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1", TryNumber: 2, AttemptEpoch: 5,
	}}

	t.Run("dedicated pod", func(t *testing.T) {
		iss := &fakeIssuer{token: "agent-token"}
		exec := &fakeExecutor{}
		if _, err := newDispatcher(res, iss, exec).Dispatch(context.Background(), "run-uuid", "etl", "", pythonTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if !iss.id.HasAttemptEpoch || iss.id.AttemptEpoch != 5 || iss.id.TryNumber != 2 {
			t.Errorf("token identity epoch has=%v n=%d try=%d, want has=true n=5 try=2", iss.id.HasAttemptEpoch, iss.id.AttemptEpoch, iss.id.TryNumber)
		}
		if exec.req.AttemptEpoch != 5 {
			t.Errorf("executor request epoch = %d, want 5", exec.req.AttemptEpoch)
		}
	})

	t.Run("warm assignment", func(t *testing.T) {
		iss := &fakeIssuer{token: "agent-token"}
		placer := &fakePlacer{ok: true}
		d := newDispatcher(res, iss, &fakeExecutor{})
		d.SetWarmPlacer(placer)
		if _, err := d.Dispatch(context.Background(), "run-uuid", "etl", "ver-1", pythonTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if placer.last == nil || placer.last.GetAttemptEpoch() != 5 {
			t.Fatalf("warm assignment epoch = %v, want 5", placer.last)
		}
		if !iss.id.HasAttemptEpoch || iss.id.AttemptEpoch != 5 {
			t.Errorf("warm attempt token epoch has=%v n=%d, want 5", iss.id.HasAttemptEpoch, iss.id.AttemptEpoch)
		}
	})
}
