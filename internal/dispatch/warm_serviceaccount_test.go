package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

func TestWarmSACompatible(t *testing.T) {
	cases := []struct {
		name   string
		task   domain.TaskSpec
		warmSA string
		want   bool
	}{
		{"no execution", domain.TaskSpec{}, "leoflow-task", true},
		{"empty SA uses default", domain.TaskSpec{Execution: &domain.Execution{}}, "leoflow-task", true},
		{"SA equals warm default", domain.TaskSpec{Execution: &domain.Execution{ServiceAccount: "leoflow-task"}}, "leoflow-task", true},
		{"pinned different SA", domain.TaskSpec{Execution: &domain.Execution{ServiceAccount: "custom-sa"}}, "leoflow-task", false},
		{"pinned SA, no warm default", domain.TaskSpec{Execution: &domain.Execution{ServiceAccount: "custom-sa"}}, "", false},
	}
	for _, c := range cases {
		if got := warmSACompatible(c.task, c.warmSA); got != c.want {
			t.Errorf("%s: warmSACompatible = %v, want %v", c.name, got, c.want)
		}
	}
}

// A task pinning a ServiceAccount different from the warm workers' must NOT be
// placed on a warm worker (which can't adopt it) — it takes the dedicated path,
// which honors the pinned SA.
func TestDispatchSkipsWarmForPinnedSA(t *testing.T) {
	placer := &fakePlacer{ok: true}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)
	d.SetDefaultTaskServiceAccount("leoflow-task") // what warm workers run as

	task := pythonTask()
	task.Execution = &domain.Execution{ServiceAccount: "custom-sa"} // pins a different SA
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 {
		t.Errorf("warm placer called %d times; a pinned non-default SA must skip warm placement", placer.calls)
	}
	if exec.req.Execution.ServiceAccount != "custom-sa" {
		t.Errorf("dedicated pod must carry the pinned SA, got %q", exec.req.Execution.ServiceAccount)
	}
}

// A task with no pinned SA (uses the default) is still eligible for warm placement.
func TestDispatchUsesWarmForDefaultSA(t *testing.T) {
	placer := &fakePlacer{ok: true}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, &fakeExecutor{})
	d.SetWarmPlacer(placer)
	d.SetDefaultTaskServiceAccount("leoflow-task")
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", warmEligibleTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 1 {
		t.Errorf("a task with no pinned SA should be warm-eligible; placer calls = %d, want 1", placer.calls)
	}
	_ = executor.Dispatched
}
