package dispatch

import (
	"context"
	"errors"
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
