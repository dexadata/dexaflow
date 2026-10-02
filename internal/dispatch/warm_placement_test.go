package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// warmEligibleTask is pythonTask without declared resources: it runs with the
// warm pod's, so it is placeable on a warm worker however that pod is sized.
func warmEligibleTask() domain.TaskSpec {
	t := pythonTask()
	t.Resources = nil
	return t
}

func strPtr(s string) *string { return &s }
func int64Ptr(n int64) *int64 { return &n }

// TestWarmPlacementCompatible is X4: a warm worker is created before any task is
// known, so it carries none of a task's own placement or pod metadata. A task
// that declares any of them must take the dedicated path, which applies them;
// placed on a warm worker it would silently run on the wrong node, without its
// GPU claim, or without the labels a NetworkPolicy selects on.
func TestWarmPlacementCompatible(t *testing.T) {
	cases := []struct {
		name string
		exec *domain.Execution
		want bool
	}{
		{"no execution", nil, true},
		{"empty execution", &domain.Execution{}, true},
		{"image pull policy only", &domain.Execution{ImagePullPolicy: "Always"}, true},
		{"node selector", &domain.Execution{NodeSelector: map[string]string{"pool": "gpu"}}, false},
		{"tolerations", &domain.Execution{Tolerations: []map[string]any{{"key": "gpu"}}}, false},
		{"affinity", &domain.Execution{Affinity: map[string]any{"nodeAffinity": map[string]any{}}}, false},
		{"topology spread", &domain.Execution{TopologySpreadConstraints: []map[string]any{{"maxSkew": 1}}}, false},
		{"priority class", &domain.Execution{PriorityClassName: "etl-low"}, false},
		{"runtime class", &domain.Execution{RuntimeClassName: strPtr("gvisor")}, false},
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

// TestWarmResourcesCompatible is X4: a task that declares no resources runs on a
// warm worker with the warm pod's resources, which by default are the platform
// default it would get on a dedicated pod. A task that declares its own runs warm
// only when the warm pod requests at least as much and caps no lower.
func TestWarmResourcesCompatible(t *testing.T) {
	q := func(cpu, mem string) *domain.ResourceQuantity { return &domain.ResourceQuantity{CPU: cpu, Memory: mem} }
	warm := &domain.Resources{Requests: q("500m", "512Mi"), Limits: q("500m", "512Mi")}
	unlimitedWarm := &domain.Resources{Requests: q("1", "1Gi")}
	burstyWarm := &domain.Resources{Requests: q("500m", "512Mi"), Limits: q("2", "2Gi")}
	storageWarm := &domain.Resources{
		Requests: &domain.ResourceQuantity{EphemeralStorage: "2Gi"},
		Limits:   &domain.ResourceQuantity{EphemeralStorage: "8Gi"},
	}
	cases := []struct {
		name string
		task *domain.Resources
		warm *domain.Resources
		want bool
	}{
		{"undeclared, warm sized", nil, warm, true},
		{"undeclared, warm unsized", nil, nil, true},
		{"declared equal", &domain.Resources{Requests: q("500m", "512Mi"), Limits: q("500m", "512Mi")}, warm, true},
		{"declared equal, other spelling", &domain.Resources{Requests: q("0.5", "512Mi"), Limits: q("0.5", "0.5Gi")}, warm, true},
		{"declared smaller", &domain.Resources{Requests: q("250m", "256Mi"), Limits: q("250m", "256Mi")}, warm, true},
		{"request bigger", &domain.Resources{Requests: q("2", "512Mi"), Limits: q("2", "512Mi")}, warm, false},
		{"limit bigger", &domain.Resources{Requests: q("500m", "512Mi"), Limits: q("500m", "4Gi")}, warm, false},
		{"no limit, warm limited", &domain.Resources{Requests: q("250m", "")}, warm, false},
		{"no limit, warm unlimited", &domain.Resources{Requests: q("250m", "")}, unlimitedWarm, true},
		{"request, warm unsized", &domain.Resources{Requests: q("250m", "")}, nil, false},
		{"unparseable", &domain.Resources{Requests: q("lots", "")}, unlimitedWarm, false},
		{"claim", &domain.Resources{Claims: []map[string]any{{"name": "gpu"}}}, unlimitedWarm, false},
		// A limit with no request makes Kubernetes request the limit, so the warm
		// pod must request at least that much.
		{"limit only, within warm", &domain.Resources{Limits: q("250m", "256Mi")}, warm, true},
		{"limit only, above warm request", &domain.Resources{Limits: q("1", "256Mi")}, burstyWarm, false},
		// An unlimited warm pod does not give a task the cap it declared (its QoS
		// and its noisy-neighbour bound), so it goes to a dedicated pod.
		{"limit, warm unlimited", &domain.Resources{Requests: q("250m", ""), Limits: q("250m", "")}, unlimitedWarm, false},
		{"limit only, warm unlimited", &domain.Resources{Limits: q("500m", "")}, unlimitedWarm, false},
		{"ephemeral limit only, warm unlimited", &domain.Resources{Limits: &domain.ResourceQuantity{EphemeralStorage: "1Gi"}}, unlimitedWarm, false},
		{"ephemeral limit only, warm covers", &domain.Resources{Limits: &domain.ResourceQuantity{EphemeralStorage: "1Gi"}}, storageWarm, true},
		{"ephemeral limit only, above warm request", &domain.Resources{Limits: &domain.ResourceQuantity{EphemeralStorage: "4Gi"}}, storageWarm, false},
	}
	for _, c := range cases {
		task := domain.TaskSpec{Resources: c.task}
		if got := warmResourcesCompatible(task, c.warm); got != c.want {
			t.Errorf("%s: warmResourcesCompatible = %v, want %v", c.name, got, c.want)
		}
	}
}

// A task pinned to a node pool must not be placed on a warm worker; the
// dedicated pod it gets instead carries the node selector.
func TestDispatchSkipsWarmForPlacement(t *testing.T) {
	placer := &fakePlacer{ok: true}
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	d.SetWarmPlacer(placer)

	task := warmEligibleTask()
	task.Execution = &domain.Execution{NodeSelector: map[string]string{"pool": "gpu"}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", task); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 {
		t.Errorf("warm placer called %d times; a task with a node selector must skip warm placement", placer.calls)
	}
	if exec.req.Execution.NodeSelector["pool"] != "gpu" {
		t.Errorf("dedicated pod must carry the node selector, got %+v", exec.req.Execution.NodeSelector)
	}
}

// A task that declares resources other than the warm pod's takes the dedicated
// path; one that declares none stays warm-eligible.
func TestDispatchSkipsWarmForDifferentResources(t *testing.T) {
	warm := &domain.Resources{
		Requests: &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi"},
		Limits:   &domain.ResourceQuantity{CPU: "500m", Memory: "512Mi"},
	}
	placer := &fakePlacer{ok: true}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, &fakeExecutor{})
	d.SetWarmPlacer(placer)
	d.SetWarmPodResources(warm)

	big := pythonTask()
	big.Resources = &domain.Resources{Limits: &domain.ResourceQuantity{Memory: "8Gi"}}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", big); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 0 {
		t.Fatalf("a task declaring 8Gi must not run on a 512Mi warm worker; placer calls = %d", placer.calls)
	}
	if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", warmEligibleTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if placer.calls != 1 {
		t.Errorf("a task declaring no resources should be warm-eligible; placer calls = %d, want 1", placer.calls)
	}
}
