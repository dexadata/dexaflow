package dispatch

import (
	"context"
	"testing"
)

// TestDispatchStampsTheLeaderTerm: a dedicated pod request carries the lease
// term of the leader that created it (ADR 0106, point 5), read at the moment
// of the dispatch; without a term source it carries none.
func TestDispatchStampsTheLeaderTerm(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1", TryNumber: 1, AttemptEpoch: 1}}

	exec := &fakeExecutor{}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
		t.Fatal(err)
	}
	if exec.req.LeaderTerm != 0 {
		t.Fatalf("no term source, request term = %d, want 0", exec.req.LeaderTerm)
	}

	term := int64(41)
	d.SetLeaderTerm(func() int64 { return term })
	term = 42
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
		t.Fatal(err)
	}
	if exec.req.LeaderTerm != 42 {
		t.Fatalf("request term = %d, want the term at dispatch time, 42", exec.req.LeaderTerm)
	}
}
