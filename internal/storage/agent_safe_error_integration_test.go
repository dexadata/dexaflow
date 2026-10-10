//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestExecutionStoreProducesSafeErrorsForTheAgentFacingMessage closes, for the
// agent transport, the hole review found in #961's fix and #1068 repeats.
//
// agentrpc now sends the task pod a fixed phrase per operation and keeps the
// real cause in the control-plane log; only a domain.SafeError — an error
// someone deliberately wrote as client-facing — still contributes text. One
// message was converted on that basis: "task X not found in run Y", which tells
// the operator the pod is running a task its dag_version does not declare (a
// stale image, a mismatched version) rather than that the control plane is
// broken.
//
// Nothing in internal/agentrpc can pin that. Its tests build the SafeError in
// the test or hand one to a fake, so reverting the Safef here would leave them
// green while every real unknown-task failure collapsed to "loading task spec:
// the request could not be completed". The assertion belongs at the layer that
// PRODUCES the error, which is this one.
func TestExecutionStoreProducesSafeErrorsForTheAgentFacingMessage(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	dagID := fmt.Sprintf("agent_safe_error_%d", time.Now().UnixNano())
	runUUID := seedRunningTask(t, repo, sched, ctx, dagID, "load")

	id := auth.AgentIdentity{
		TaskInstanceID: "ti-unknown", TenantID: "default", DagID: dagID,
		RunID: runUUID, TaskID: "no_such_task", TryNumber: 1,
	}
	_, err := exec.TaskSpec(ctx, id)
	if err == nil {
		t.Fatal("TaskSpec for a task the dag_version does not declare must fail")
	}
	var safe *domain.SafeError
	if !errors.As(err, &safe) {
		t.Fatalf("the unknown-task error is not a *domain.SafeError, so agentrpc redacts it and the pod is told nothing usable: %v", err)
	}
	if !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("the unknown-task error lost its ErrNotFound class: %v", err)
	}
	for _, want := range []string{"no_such_task", runUUID} {
		if !strings.Contains(safe.ClientMessage(), want) {
			t.Errorf("the message the agent receives lost %q: %q", want, safe.ClientMessage())
		}
	}
}

// TestExecutionStoreLeavesDriverErrorsUnsafe is the other half: converting the
// one message worth passing through must not turn into wrapping everything in
// Safef, which would re-open the leak through the sanctioned door. A malformed
// run id fails inside the driver, and that failure must stay opaque.
func TestExecutionStoreLeavesDriverErrorsUnsafe(t *testing.T) {
	_, _, exec, ctx := openExec(t)

	_, err := exec.TaskSpec(ctx, auth.AgentIdentity{RunID: "not-a-uuid", TaskID: "load", TryNumber: 1})
	if err == nil {
		t.Fatal("TaskSpec with a malformed run id must fail")
	}
	var safe *domain.SafeError
	if errors.As(err, &safe) {
		t.Errorf("a driver-level failure was marked client-facing; its text would reach the task pod verbatim: %q", safe.ClientMessage())
	}
}
