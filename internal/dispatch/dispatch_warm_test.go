package dispatch

import (
	"context"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/executor"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// fakePlacer records the assignment it was handed and returns a canned result.
type fakePlacer struct {
	ok         bool
	calls      int
	lastDagVer string
	last       *agentv1.WorkAssignment
}

func (p *fakePlacer) Assign(dagVersionID string, a *agentv1.WorkAssignment) bool {
	p.calls++
	p.lastDagVer = dagVersionID
	p.last = a
	return p.ok
}

func TestDispatchPlacesOnWarmWorkerWhenFree(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{
		TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1", TryNumber: 3,
	}}
	iss := &fakeIssuer{token: "agent-token"}
	exec := &fakeExecutor{}
	placer := &fakePlacer{ok: true}

	d := NewDispatcher(exec, res, iss, "cp:9091", time.Hour)
	d.SetWarmPlacer(placer)

	disp, err := d.Dispatch(context.Background(), "run-uuid", "etl", "ver-7", warmEligibleTask())
	if err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if disp != executor.Dispatched {
		t.Errorf("disposition = %v, want Dispatched", disp)
	}
	// A warm placement must NOT fall through to the dedicated pod executor.
	if exec.req.TaskInstanceID != "" {
		t.Errorf("executor was called on a successful warm placement: %+v", exec.req)
	}
	if placer.calls != 1 {
		t.Fatalf("placer called %d times, want 1", placer.calls)
	}
	if placer.lastDagVer != "ver-7" {
		t.Errorf("placer got dag_version %q, want ver-7", placer.lastDagVer)
	}
	wa := placer.last
	if wa.GetDagRunId() != "run-uuid" || wa.GetTaskId() != "extract" || wa.GetTryNumber() != 3 {
		t.Errorf("assignment identity wrong: run=%q task=%q try=%d", wa.GetDagRunId(), wa.GetTaskId(), wa.GetTryNumber())
	}
	if wa.GetDagVersionId() != "ver-7" {
		t.Errorf("assignment dag_version = %q, want ver-7", wa.GetDagVersionId())
	}
	if wa.GetAttemptToken() != "agent-token" {
		t.Errorf("assignment attempt_token = %q, want agent-token", wa.GetAttemptToken())
	}
	if wa.GetAssignmentId() == "" {
		t.Error("assignment_id is empty; a fresh unique id is required")
	}
	if wa.GetLeaseSeconds() <= 0 {
		t.Errorf("lease_seconds = %d, want a positive constant", wa.GetLeaseSeconds())
	}
}

func TestDispatchAssignmentIDsAreUnique(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}
	placer := &fakePlacer{ok: true}
	d := NewDispatcher(&fakeExecutor{}, res, &fakeIssuer{token: "t"}, "cp:9091", time.Hour)
	d.SetWarmPlacer(placer)

	seen := map[string]bool{}
	for i := 0; i < 3; i++ {
		if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", warmEligibleTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		id := placer.last.GetAssignmentId()
		if seen[id] {
			t.Fatalf("assignment_id %q reused across dispatches", id)
		}
		seen[id] = true
	}
}

func TestDispatchFallsThroughToDedicatedOnWarmMiss(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{
		TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1", TryNumber: 1,
	}}
	exec := &fakeExecutor{}
	placer := &fakePlacer{ok: false} // no free warm worker

	d := NewDispatcher(exec, res, &fakeIssuer{token: "agent-token"}, "cp:9091", time.Hour)
	d.SetWarmPlacer(placer)

	if _, err := d.Dispatch(context.Background(), "run-uuid", "etl", "ver-7", warmEligibleTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 1 {
		t.Errorf("placer called %d times, want 1", placer.calls)
	}
	// A warm miss must degrade to the dedicated pod path, not strand the task.
	if exec.req.TaskInstanceID != "ti-1" {
		t.Errorf("executor NOT called on warm miss (task stranded): %+v", exec.req)
	}
	if exec.req.AgentToken != "agent-token" {
		t.Errorf("dedicated path lost the minted token: %+v", exec.req)
	}
}

func TestDispatchNilPlacerGoesStraightToDedicated(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", Image: "etl:v1"}}
	exec := &fakeExecutor{}

	// No SetWarmPlacer: placer stays nil (warm pools disabled), today's behavior.
	d := NewDispatcher(exec, res, &fakeIssuer{token: "t"}, "cp:9091", time.Hour)
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.TaskInstanceID != "ti-1" {
		t.Errorf("nil placer should dispatch dedicated: %+v", exec.req)
	}
}
