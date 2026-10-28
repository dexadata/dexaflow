package dispatch

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

type fakeResolver struct {
	resolved Resolved
	err      error
}

func (f *fakeResolver) ResolveTask(context.Context, string, string) (Resolved, error) {
	return f.resolved, f.err
}

type fakeIssuer struct {
	id    auth.AgentIdentity
	token string
	err   error
}

func (f *fakeIssuer) IssueAgentToken(id auth.AgentIdentity, _ time.Duration) (string, error) {
	f.id = id
	return f.token, f.err
}

type fakeExecutor struct {
	req  executor.Request
	disp executor.Disposition
	err  error
}

func (f *fakeExecutor) Execute(_ context.Context, req executor.Request) (executor.Disposition, error) {
	f.req = req
	return f.disp, f.err
}

func pythonTask() domain.TaskSpec {
	return domain.TaskSpec{
		TaskID:     "extract",
		Type:       domain.TaskTypePython,
		Entrypoint: "dag:extract",
		Env:        map[string]string{"FOO": "bar"},
		Resources:  &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "500m"}},
	}
}

func newDispatcher(res Resolver, iss TokenIssuer, exec executor.Executor) *Dispatcher {
	return NewDispatcher(exec, res, iss, "cp:9091", time.Hour)
}

func TestDispatchBuildsRequest(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{
		TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1",
		ImagePullPolicy: "IfNotPresent", TryNumber: 2,
	}}
	iss := &fakeIssuer{token: "agent-token"}
	exec := &fakeExecutor{}

	if _, err := newDispatcher(res, iss, exec).Dispatch(context.Background(), "run-uuid", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}

	req := exec.req
	if req.Image != "etl:v1" || req.Operator != "python" || req.Entrypoint != "dag:extract" {
		t.Errorf("request core fields wrong: %+v", req)
	}
	if req.TaskInstanceID != "ti-1" || req.TryNumber != 2 || req.TenantID != "acme" {
		t.Errorf("identity not propagated: %+v", req)
	}
	if req.AgentToken != "agent-token" || req.ControlPlaneAddr != "cp:9091" {
		t.Errorf("agent connection not set: token=%q addr=%q", req.AgentToken, req.ControlPlaneAddr)
	}
	if req.Env["FOO"] != "bar" || req.Resources.Requests == nil {
		t.Errorf("task fields not mapped: %+v", req)
	}
	if iss.id.TaskInstanceID != "ti-1" || iss.id.RunID != "run-uuid" || iss.id.DagID != "etl" {
		t.Errorf("token identity wrong: %+v", iss.id)
	}
}

func TestDispatchAppliesPlatformDefaults(t *testing.T) {
	defaults := PlatformDefaults{
		StagingSize:         "10Gi",
		StagingStorageClass: "efs-sc",
		Resources:           &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "250m", Memory: "256Mi"}},
	}

	t.Run("fills gaps the artifact left empty", func(t *testing.T) {
		res := &fakeResolver{resolved: Resolved{
			TaskInstanceID: "ti", Image: "etl:v1",
			Staging: &domain.StagingConfig{Enabled: true}, // size/class unset
		}}
		exec := &fakeExecutor{}
		d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
		d.SetPlatformDefaults(defaults)

		bare := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t"} // no resources
		if _, err := d.Dispatch(context.Background(), "run", "etl", "", bare); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if exec.req.StagingSize != "10Gi" || exec.req.StagingStorageClass != "efs-sc" {
			t.Errorf("staging defaults not filled: size=%q class=%q", exec.req.StagingSize, exec.req.StagingStorageClass)
		}
		if exec.req.Resources.Requests == nil || exec.req.Resources.Requests.CPU != "250m" {
			t.Errorf("resource defaults not filled: %+v", exec.req.Resources)
		}
	})

	t.Run("never overrides an explicit baked value", func(t *testing.T) {
		res := &fakeResolver{resolved: Resolved{
			TaskInstanceID: "ti", Image: "etl:v1",
			Staging: &domain.StagingConfig{Enabled: true, Size: "1Gi", StorageClass: "fast"},
		}}
		exec := &fakeExecutor{}
		d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)
		d.SetPlatformDefaults(defaults)

		if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if exec.req.StagingSize != "1Gi" || exec.req.StagingStorageClass != "fast" {
			t.Errorf("platform defaults clobbered explicit staging: %+v", exec.req)
		}
		if exec.req.Resources.Requests.CPU != "500m" {
			t.Errorf("platform defaults clobbered explicit resources: %+v", exec.req.Resources)
		}
	})
}

func TestDispatchPropagatesErrors(t *testing.T) {
	cases := map[string]*Dispatcher{
		"resolver": newDispatcher(&fakeResolver{err: errors.New("x")}, &fakeIssuer{token: "t"}, &fakeExecutor{}),
		"issuer":   newDispatcher(&fakeResolver{}, &fakeIssuer{err: errors.New("x")}, &fakeExecutor{}),
		"executor": newDispatcher(&fakeResolver{}, &fakeIssuer{token: "t"}, &fakeExecutor{err: errors.New("x")}),
	}
	for name, d := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err == nil {
				t.Errorf("%s failure should abort dispatch", name)
			}
		})
	}
}

// TestDispatchThreadsAttemptLifetimeCeiling: the operator's credential-lifetime
// ceiling reaches the executor request as whole seconds, so the pod builder can
// floor the pod's ActiveDeadlineSeconds with it when the task declares no
// timeout. Unset (the zero value) is passed through as zero — "no floor".
func TestDispatchThreadsAttemptLifetimeCeiling(t *testing.T) {
	res := &fakeResolver{resolved: Resolved{TaskInstanceID: "ti-1", TenantID: "acme", Image: "etl:v1", TryNumber: 1}}
	exec := &fakeExecutor{}
	d := newDispatcher(res, &fakeIssuer{token: "t"}, exec)

	if _, err := d.Dispatch(context.Background(), "run-uuid", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.AttemptLifetimeCeilingSeconds != 0 {
		t.Errorf("unset ceiling must pass through as 0, got %d", exec.req.AttemptLifetimeCeilingSeconds)
	}

	d.SetAttemptLifetimeCeiling(24 * time.Hour)
	if _, err := d.Dispatch(context.Background(), "run-uuid", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.AttemptLifetimeCeilingSeconds != 86400 {
		t.Errorf("ceiling must reach the request as seconds, got %d want 86400", exec.req.AttemptLifetimeCeilingSeconds)
	}
}

// TestDispatchAppliesDefaultLimits: the operator's default limits (#537) fill an
// empty CPU or memory limit on the task pod, whether the task declared requests
// only, nothing at all, or got the L0 resource default, and never change a
// declared limit. Without them a task that declares requests only runs with no
// limit, which is what the hardening item is about.
func TestDispatchAppliesDefaultLimits(t *testing.T) {
	limits, err := domain.ParseDefaultLimits("2", "4Gi")
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		defaults PlatformDefaults
		task     *domain.Resources
		want     domain.ResourceQuantity
	}{
		{
			name:     "requests only",
			defaults: PlatformDefaults{Limits: limits},
			task:     &domain.Resources{Requests: &domain.ResourceQuantity{CPU: "500m"}},
			want:     domain.ResourceQuantity{CPU: "2", Memory: "4Gi"},
		},
		{
			name:     "no resources and no L0 default",
			defaults: PlatformDefaults{Limits: limits},
			want:     domain.ResourceQuantity{CPU: "2", Memory: "4Gi"},
		},
		{
			name:     "declared limit kept",
			defaults: PlatformDefaults{Limits: limits},
			task:     &domain.Resources{Limits: &domain.ResourceQuantity{Memory: "16Gi"}},
			want:     domain.ResourceQuantity{CPU: "2", Memory: "16Gi"},
		},
		{
			name: "L0 default limits win over the default limits",
			defaults: PlatformDefaults{Limits: limits, Resources: &domain.Resources{
				Requests: &domain.ResourceQuantity{CPU: "250m"},
				Limits:   &domain.ResourceQuantity{CPU: "250m"},
			}},
			want: domain.ResourceQuantity{CPU: "250m", Memory: "4Gi"},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exec := &fakeExecutor{}
			d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
			d.SetPlatformDefaults(c.defaults)
			task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", Resources: c.task}
			if _, err := d.Dispatch(context.Background(), "run", "etl", "", task); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if got := exec.req.Resources.Limits; got == nil || *got != c.want {
				t.Errorf("limits = %+v; want %+v", got, c.want)
			}
			if c.task != nil && c.task.Limits != nil && c.task.Limits.CPU != "" {
				t.Errorf("dispatch wrote through to the task's resources: %+v", c.task.Limits)
			}
		})
	}
}

// TestDispatchWithoutDefaultLimitsLeavesLimitsEmpty is the behavior-neutral
// default: nothing configured, a requests-only task gets no limit, as before.
func TestDispatchWithoutDefaultLimitsLeavesLimitsEmpty(t *testing.T) {
	exec := &fakeExecutor{}
	d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
	if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
		t.Fatalf("Dispatch: %v", err)
	}
	if exec.req.Resources.Limits != nil {
		t.Errorf("limits = %+v; want none", exec.req.Resources.Limits)
	}
}

// Without a unit, warm pods are sized by the default limits (#537), so a task
// whose own limit would be larger takes a dedicated pod instead of running
// under the warm pod's smaller limit; a task within the default is still
// placed warm.
func TestDispatchWarmPlacementWithDefaultLimits(t *testing.T) {
	limits, err := domain.ParseDefaultLimits("2", "4Gi")
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string]struct {
		res      *domain.Resources
		wantWarm bool
	}{
		"no resources":          {nil, true},
		"within the default":    {&domain.Resources{Requests: &domain.ResourceQuantity{Memory: "1Gi"}}, true},
		"request above default": {&domain.Resources{Requests: &domain.ResourceQuantity{Memory: "8Gi"}}, false},
		"declared larger limit": {&domain.Resources{Limits: &domain.ResourceQuantity{CPU: "8"}}, false},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			exec := &fakeExecutor{}
			d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
			d.SetPlatformDefaults(PlatformDefaults{Limits: limits})
			placer := &fakePlacer{ok: true}
			d.SetWarmPlacer(placer)
			task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", Resources: c.res}
			if _, err := d.Dispatch(context.Background(), "run", "etl", "ver", task); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			if gotWarm := placer.calls == 1; gotWarm != c.wantWarm {
				t.Errorf("placed warm = %v, want %v", gotWarm, c.wantWarm)
			}
		})
	}
}

// TestDispatchFillsTheDimensionsATaskLeavesEmpty (#802): the L0 platform
// default fills each of cpu, memory and ephemeral storage that the task's own
// resources leave empty, and never a dimension the task sets in part. A task
// that declares only an ephemeral-storage limit used to drop the cpu and
// memory default and run BestEffort.
func TestDispatchFillsTheDimensionsATaskLeavesEmpty(t *testing.T) {
	l0 := &domain.Resources{
		Requests: &domain.ResourceQuantity{CPU: "250m", Memory: "512Mi"},
		Limits:   &domain.ResourceQuantity{CPU: "250m", Memory: "512Mi"},
	}
	q := func(cpu, mem, eph string) *domain.ResourceQuantity {
		return &domain.ResourceQuantity{CPU: cpu, Memory: mem, EphemeralStorage: eph}
	}
	cases := []struct {
		name         string
		task         *domain.Resources
		wantRequests *domain.ResourceQuantity
		wantLimits   *domain.ResourceQuantity
	}{
		{
			name:         "only an ephemeral-storage limit",
			task:         &domain.Resources{Limits: q("", "", "2Gi")},
			wantRequests: q("250m", "512Mi", ""),
			wantLimits:   q("250m", "512Mi", "2Gi"),
		},
		{
			name:         "only a cpu request and limit",
			task:         &domain.Resources{Requests: q("1", "", ""), Limits: q("1", "", "")},
			wantRequests: q("1", "512Mi", ""),
			wantLimits:   q("1", "512Mi", ""),
		},
		{
			// The task owns cpu: L0's 250m request is not paired with its 100m
			// limit, which would ask for more than the limit.
			name:         "a cpu limit below the default request",
			task:         &domain.Resources{Limits: q("100m", "", "")},
			wantRequests: q("", "512Mi", ""),
			wantLimits:   q("100m", "512Mi", ""),
		},
		{
			name:         "an empty resources object",
			task:         &domain.Resources{},
			wantRequests: q("250m", "512Mi", ""),
			wantLimits:   q("250m", "512Mi", ""),
		},
		{
			name:         "both dimensions declared",
			task:         &domain.Resources{Requests: q("2", "4Gi", ""), Limits: q("2", "4Gi", "")},
			wantRequests: q("2", "4Gi", ""),
			wantLimits:   q("2", "4Gi", ""),
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			exec := &fakeExecutor{}
			d := newDispatcher(&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}}, &fakeIssuer{token: "t"}, exec)
			d.SetPlatformDefaults(PlatformDefaults{Resources: l0})
			before := fmt.Sprintf("%+v %+v", c.task.Requests, c.task.Limits)
			task := domain.TaskSpec{TaskID: "t", Type: domain.TaskTypePython, Entrypoint: "dag:t", Resources: c.task}
			if _, err := d.Dispatch(context.Background(), "run", "etl", "", task); err != nil {
				t.Fatalf("Dispatch: %v", err)
			}
			got := exec.req.Resources
			if got.Requests == nil || *got.Requests != *c.wantRequests {
				t.Errorf("requests = %+v; want %+v", got.Requests, c.wantRequests)
			}
			if got.Limits == nil || *got.Limits != *c.wantLimits {
				t.Errorf("limits = %+v; want %+v", got.Limits, c.wantLimits)
			}
			if after := fmt.Sprintf("%+v %+v", c.task.Requests, c.task.Limits); after != before {
				t.Errorf("dispatch wrote through to the task's resources: %+v", c.task)
			}
		})
	}
}
