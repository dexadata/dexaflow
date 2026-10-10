package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func strPtr(s string) *string { return &s }
func int64Ptr(n int64) *int64 { return &n }

// TestWarmPlacementCompatible: a warm worker is created before any task is
// known, so it carries none of a task's own placement or pod metadata. A task
// that declares any of them must take the dedicated path, which applies them;
// placed on a warm worker it would silently run on the wrong node, outside the
// sandbox its runtime class asks for (gVisor), without its device claim, or
// without the labels a NetworkPolicy selects on.
func TestWarmPlacementCompatible(t *testing.T) {
	cases := []struct {
		name string
		exec *domain.Execution
		want bool
	}{
		{"no execution", nil, true},
		{"empty execution", &domain.Execution{}, true},
		{"image pull policy only", &domain.Execution{ImagePullPolicy: "Always"}, true},
		{"default service account", &domain.Execution{ServiceAccount: "leoflow-task"}, true},
		{"node selector", &domain.Execution{NodeSelector: map[string]string{"pool": "gpu"}}, false},
		{"tolerations", &domain.Execution{Tolerations: []map[string]any{{"key": "gpu"}}}, false},
		{"affinity", &domain.Execution{Affinity: map[string]any{"nodeAffinity": map[string]any{}}}, false},
		{"topology spread", &domain.Execution{TopologySpreadConstraints: []map[string]any{{"maxSkew": 1}}}, false},
		{"priority class", &domain.Execution{PriorityClassName: "etl-low"}, false},
		{"runtime class", &domain.Execution{RuntimeClassName: strPtr("gvisor")}, false},
		{"empty runtime class", &domain.Execution{RuntimeClassName: strPtr("")}, false},
		{"termination grace", &domain.Execution{TerminationGracePeriodSeconds: int64Ptr(120)}, false},
		{"resource claims", &domain.Execution{ResourceClaims: []map[string]any{{"name": "gpu"}}}, false},
		{"labels", &domain.Execution{Labels: map[string]string{"team": "a"}}, false},
		{"annotations", &domain.Execution{Annotations: map[string]string{"sidecar.istio.io/inject": "false"}}, false},
	}
	for _, c := range cases {
		task := domain.TaskSpec{Execution: c.exec}
		if got := warmPlacementCompatible(task); got != c.want {
			t.Errorf("%s: warmPlacementCompatible = %v, want %v", c.name, got, c.want)
		}
	}
}

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
