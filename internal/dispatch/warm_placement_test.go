package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func strPtr(s string) *string { return &s }

// A task pinned to gVisor must never reach a warm worker (plain runc pod): it
// takes the dedicated path, which sets its runtime class.
func TestDispatchSkipsWarmForRuntimeClass(t *testing.T) {
	placer := &fakePlacer{ok: true}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)

	task := pythonTask()
	task.Execution = &domain.Execution{RuntimeClassName: strPtr("gvisor")}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 {
		t.Errorf("warm placer called %d times; a task with a runtime class must skip warm placement", placer.calls)
	}
	if rc := exec.req.Execution.RuntimeClassName; rc == nil || *rc != "gvisor" {
		t.Errorf("dedicated pod must carry the runtime class, got %v", rc)
	}
}

// A task that declares a node selector takes the dedicated path too.
func TestDispatchSkipsWarmForNodeSelector(t *testing.T) {
	placer := &fakePlacer{ok: true}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)

	task := pythonTask()
	task.Execution = &domain.Execution{NodeSelector: map[string]string{"pool": "gpu"}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 {
		t.Errorf("warm placer called %d times; a task with a node selector must skip warm placement", placer.calls)
	}
}
