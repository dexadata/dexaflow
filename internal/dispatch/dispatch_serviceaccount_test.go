package dispatch

import (
	"context"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
)

// The task pod's ServiceAccount defaults to the operator-configured one when a DAG
// does not set execution.service_account, so keyless works without every DAG
// opting in (the execution.service_account trap, #2). An explicit per-task value
// wins, and with no default configured the SA stays empty (today's behavior).
func TestDispatchDefaultsTaskServiceAccount(t *testing.T) {
	newD := func(exec executor.Executor) *Dispatcher {
		return newDispatcher(
			&fakeResolver{resolved: Resolved{TaskInstanceID: "ti", Image: "etl:v1"}},
			&fakeIssuer{token: "t"}, exec)
	}

	t.Run("default applied when the task sets no service_account", func(t *testing.T) {
		exec := &fakeExecutor{}
		d := newD(exec)
		d.SetDefaultTaskServiceAccount("leoflow-task")
		if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if exec.req.Execution.ServiceAccount != "leoflow-task" {
			t.Errorf("default SA not applied; got %q", exec.req.Execution.ServiceAccount)
		}
	})

	t.Run("explicit per-task service_account wins", func(t *testing.T) {
		exec := &fakeExecutor{}
		d := newD(exec)
		d.SetDefaultTaskServiceAccount("leoflow-task")
		task := pythonTask()
		task.Execution = &domain.Execution{ServiceAccount: "custom-sa"}
		if _, err := d.Dispatch(context.Background(), "run", "etl", "", task); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if exec.req.Execution.ServiceAccount != "custom-sa" {
			t.Errorf("explicit SA must win over the default; got %q", exec.req.Execution.ServiceAccount)
		}
	})

	t.Run("no default leaves the SA empty", func(t *testing.T) {
		exec := &fakeExecutor{}
		d := newD(exec)
		if _, err := d.Dispatch(context.Background(), "run", "etl", "", pythonTask()); err != nil {
			t.Fatalf("Dispatch: %v", err)
		}
		if exec.req.Execution.ServiceAccount != "" {
			t.Errorf("no default configured → empty SA (today's behavior); got %q", exec.req.Execution.ServiceAccount)
		}
	})
}
