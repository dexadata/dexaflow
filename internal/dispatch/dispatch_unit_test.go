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
	return unitDispatcherWith(t, domain.ResourceUnitConfig{})
}

func unitDispatcherWith(t *testing.T, c domain.ResourceUnitConfig) (*Dispatcher, *fakeExecutor) {
	t.Helper()
	c.CPU, c.Memory = "250m", "512Mi"
	unit, err := domain.ParseResourceUnit(c)
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

// A task larger than its size is Refused (fails once, no retries), not
// Rejected (retried as a possibly transient failure).
func TestDispatchRefusesATaskLargerThanItsSize(t *testing.T) {
	d, exec := unitDispatcher(t)
	task := domain.TaskSpec{TaskID: "big", Type: domain.TaskTypePython, Entrypoint: "dag:big",
		Resources: &domain.Resources{Limits: &domain.ResourceQuantity{Memory: "2Gi"}}}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "", task)
	if err == nil || disp != executor.Refused {
		t.Fatalf("Dispatch = %v, %v; want Refused with an error", disp, err)
	}
	if !strings.Contains(err.Error(), "size: 4") {
		t.Errorf("error %q should name the size the task needs", err)
	}
	if exec.req.TaskID != "" {
		t.Errorf("executor was called for a rejected task: %+v", exec.req)
	}
}

// fakeMisfits records the stages a tolerated misfit was counted at.
type fakeMisfits struct{ stages []string }

func (f *fakeMisfits) RecordUnitMisfit(stage string) { f.stages = append(f.stages, stage) }

// Under enforce: warn a task larger than its size runs with its own
// resources, and the misfit is counted (ADR 0066 §3, rollout).
func TestDispatchWarnRunsAMisfitWithItsOwnResources(t *testing.T) {
	d, exec := unitDispatcherWith(t, domain.ResourceUnitConfig{Enforce: domain.UnitEnforceWarn})
	rec := &fakeMisfits{}
	d.SetUnitMisfitRecorder(rec)
	task := domain.TaskSpec{TaskID: "big", Type: domain.TaskTypePython, Entrypoint: "dag:big",
		Resources: &domain.Resources{Limits: &domain.ResourceQuantity{Memory: "2Gi"}}}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "", task)
	if err != nil || disp != executor.Dispatched {
		t.Fatalf("Dispatch = %v, %v; want Dispatched under warn", disp, err)
	}
	if exec.req.Resources.Limits == nil || exec.req.Resources.Limits.Memory != "2Gi" {
		t.Errorf("limits = %+v, want the task's own 2Gi", exec.req.Resources.Limits)
	}
	if len(rec.stages) != 1 || rec.stages[0] != "dispatch" {
		t.Errorf("misfits recorded = %v, want one at dispatch", rec.stages)
	}
}

// A size above executor.unit.max_size is refused at dispatch too, for a DAG
// registered before the unit was set.
func TestDispatchRefusesASizeAboveMaxSize(t *testing.T) {
	d, exec := unitDispatcherWith(t, domain.ResourceUnitConfig{MaxSize: 8, Enforce: domain.UnitEnforceWarn})
	task := domain.TaskSpec{TaskID: "huge", Type: domain.TaskTypePython, Entrypoint: "dag:huge", PoolSlots: 9}
	disp, err := d.Dispatch(context.Background(), "run", "etl", "", task)
	if err == nil || disp != executor.Refused || !strings.Contains(err.Error(), "max_size") {
		t.Fatalf("Dispatch = %v, %v; want Refused naming max_size", disp, err)
	}
	if exec.req.TaskID != "" {
		t.Errorf("executor was called for a refused task: %+v", exec.req)
	}
}

// With a unit a warm pod is one unit, so a larger task skips warm placement
// and takes a dedicated pod; a size-1 task without resources is still placed
// warm (ADR 0066 §3, warm workers).
func TestDispatchWarmPlacementOnlyForOneUnitTasks(t *testing.T) {
	cases := map[string]struct {
		task     domain.TaskSpec
		wantWarm bool
	}{
		"size 1":        {domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t"}, true},
		"size 2":        {domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", PoolSlots: 2}, false},
		"own resources": {domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", Resources: &domain.Resources{Limits: &domain.ResourceQuantity{CPU: "100m"}}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			d, exec := unitDispatcher(t)
			placer := &fakePlacer{ok: true}
			d.SetWarmPlacer(placer)
			if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", c.task); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if gotWarm := placer.calls == 1; gotWarm != c.wantWarm {
				t.Errorf("placed warm = %v, want %v", gotWarm, c.wantWarm)
			}
			if gotPod := exec.req.TaskID != ""; gotPod == c.wantWarm {
				t.Errorf("dedicated pod = %v, want %v", gotPod, !c.wantWarm)
			}
		})
	}
}
