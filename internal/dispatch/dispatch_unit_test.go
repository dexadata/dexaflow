package dispatch

import (
	"context"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// With an operator resource unit (ADR 0066 §3) the pod is sized from the
// task's pool_slots, and a task that asks for more than its size is refused
// even if it was registered before the unit was configured.

func unitDispatcher(t *testing.T) (*Dispatcher, *fakeExecutor) {
	t.Helper()
	unit, err := domain.ParseResourceUnit("250m", "512Mi")
	if err != nil {
		t.Fatal(err)
	}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetPlatformDefaults(PlatformDefaults{
		// The L0 default is replaced by the unit for a task that declares nothing.
		Resources: &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "2", Memory: "4Gi"}},
		Unit:      unit,
	})
	return d, exec
}

func TestDispatchSizesThePodFromTheUnit(t *testing.T) {
	d, exec := unitDispatcher(t)
	task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", PoolSlots: 3}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	r := exec.req.Resources
	if r.Requests == nil || r.Requests.CPU != "750m" || r.Requests.Memory != "1536Mi" {
		t.Errorf("requests = %+v, want 750m / 1536Mi (3 x unit)", r.Requests)
	}
	if r.Limits == nil || r.Limits.CPU != "750m" || r.Limits.Memory != "1536Mi" {
		t.Errorf("limits = %+v, want 750m / 1536Mi (3 x unit)", r.Limits)
	}
}

func TestDispatchKeepsFittingResourcesAndFillsGaps(t *testing.T) {
	d, exec := unitDispatcher(t)
	task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", PoolSlots: 2,
		Resources: &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "200m"}}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	r := exec.req.Resources
	if r.Requests.CPU != "200m" || r.Limits == nil || r.Limits.CPU != "500m" || r.Limits.Memory != "1Gi" {
		t.Errorf("resources = %+v / %+v, want declared request kept and limits from 2 x unit", r.Requests, r.Limits)
	}
}

func TestDispatchRejectsATaskLargerThanItsSize(t *testing.T) {
	d, exec := unitDispatcher(t)
	task := domain.TaskSpec{TaskID: "big", Type: domain.TaskTypePython, Entrypoint: "dag:big",
		Resources: &domain.Resources{Limits: &domain.ResourceQuantity{Memory: "2Gi"}}}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "", task)
	if err == nil || disp != executor.Rejected {
		t.Fatalf("Dispatch = %v, %v; want Rejected with an error", disp, err)
	}
	if !strings.Contains(err.Error(), "size: 4") {
		t.Errorf("error %q should name the size the task needs", err)
	}
	if exec.req.TaskID != "" {
		t.Errorf("executor was called for a rejected task: %+v", exec.req)
	}
}
