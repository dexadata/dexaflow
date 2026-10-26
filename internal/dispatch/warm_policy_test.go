package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestDispatchPlacesWhatItsWarmPolicySays locks the wiring the register
// response depends on (#1707): the policy a Dispatcher reports (WarmPolicy)
// is the one Dispatch places with. For every reason a task takes a dedicated
// pod, the real Dispatch offers the attempt to the warm placer exactly when
// the policy gives no reason, and the reason is the one the register reports.
func TestDispatchPlacesWhatItsWarmPolicySays(t *testing.T) {
	unit, err := domain.ParseResourceUnit(domain.ResourceUnitConfig{CPU: "250m", Memory: "512Mi"})
	if err != nil {
		t.Fatal(err)
	}
	limits, err := domain.ParseDefaultLimits("1", "1Gi")
	if err != nil {
		t.Fatal(err)
	}
	plain := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t"}
	with := func(f func(*domain.TaskSpec)) domain.TaskSpec {
		task := plain
		f(&task)
		return task
	}
	cases := []struct {
		name       string
		placer     bool
		sourceWarm bool
		unit       *domain.ResourceUnit
		limits     *domain.DefaultLimits
		resolved   Resolved
		task       domain.TaskSpec
		want       string
	}{
		{name: "eligible", placer: true, task: plain, want: ""},
		{name: "warm pools off", task: plain, want: domain.DedicatedWarmPoolsOff},
		{name: "staging", placer: true, resolved: Resolved{Staging: &domain.StagingConfig{Enabled: true}}, task: plain, want: domain.DedicatedStaging},
		{name: "source mode", placer: true, resolved: Resolved{Image: runtimeImage, Source: "print(1)\n"}, task: plain, want: domain.DedicatedSourceMode},
		{name: "source mode with warm pools", placer: true, sourceWarm: true, resolved: Resolved{Image: runtimeImage, Source: "print(1)\n"}, task: plain, want: ""},
		{name: "pinned service account", placer: true, task: with(func(t *domain.TaskSpec) {
			t.Execution = &domain.Execution{ServiceAccount: "custom-sa"}
		}), want: "service_account set"},
		{name: "default service account", placer: true, task: with(func(t *domain.TaskSpec) {
			t.Execution = &domain.Execution{ServiceAccount: "dexaflow-task"}
		}), want: ""},
		{name: "node selector", placer: true, task: with(func(t *domain.TaskSpec) {
			t.Execution = &domain.Execution{NodeSelector: map[string]string{"pool": "gpu"}}
		}), want: "node_selector set"},
		{name: "annotations", placer: true, task: with(func(t *domain.TaskSpec) {
			t.Execution = &domain.Execution{Annotations: map[string]string{"a": "b"}}
		}), want: "annotations set"},
		{name: "size above one unit", placer: true, unit: unit, task: with(func(t *domain.TaskSpec) { t.PoolSlots = 2 }),
			want: "size 2 is above the warm pod's 1 unit"},
		{name: "resources with a unit", placer: true, unit: unit, task: with(func(t *domain.TaskSpec) {
			t.Resources = &domain.Resources{Limits: &domain.ResourceQuantity{CPU: "100m"}}
		}), want: "resources set (a warm pod is 1 unit)"},
		{name: "above the default limits", placer: true, limits: limits, task: with(func(t *domain.TaskSpec) {
			t.Resources = &domain.Resources{Limits: &domain.ResourceQuantity{CPU: "2"}}
		}), want: "resources above the warm pod's default limits"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := c.resolved
			r.TaskInstanceID = "ti"
			if r.Image == "" {
				r.Image = "etl:v1"
			}
			placer := &fakePlacer{ok: true}
			exec := &fakeExecutor{}
			d := newDispatcher(&fakeResolver{resolved: r}, &fakeIssuer{token: "t"}, exec)
			if c.placer {
				d.SetWarmPlacer(placer)
			}
			d.SetDefaultTaskServiceAccount("dexaflow-task")
			d.SetSourceModeImage(runtimeImage)
			d.SetSourceModeWarm(c.sourceWarm)
			d.SetPlatformDefaults(PlatformDefaults{Unit: c.unit, Limits: c.limits})

			reason := d.WarmPolicy().DedicatedReason(domain.WarmVersion{Image: r.Image, HasSource: r.Source != "", Staging: r.Staging}, c.task)
			if reason != c.want {
				t.Fatalf("WarmPolicy().DedicatedReason = %q, want %q", reason, c.want)
			}
			if _, err := d.Dispatch(context.Background(), "run", "etl", "ver-1", c.task); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if gotWarm, wantWarm := placer.calls == 1, reason == ""; gotWarm != wantWarm {
				t.Errorf("placed on a warm worker = %v, but the policy says %q", gotWarm, reason)
			}
			if gotPod := exec.req.TaskID != ""; gotPod != (reason != "") {
				t.Errorf("dedicated pod = %v, but the policy says %q", gotPod, reason)
			}
		})
	}
}
