//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

type queueWaitSample struct {
	tenant  string
	seconds float64
}

// The first running report of an attempt hands its wait from queued_at to the
// observer once; a repeated running report and a terminal report do not, so
// dexaflow_task_queue_wait_seconds holds one sample per attempt (#2300).
func TestReportStateObservesTheQueueWaitOncePerAttemptIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	var got []queueWaitSample
	exec.SetQueueWaitObserver(func(tenant string, seconds float64) {
		got = append(got, queueWaitSample{tenant, seconds})
	})
	dagID := fmt.Sprintf("queue_wait_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}
	time.Sleep(50 * time.Millisecond)

	id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	for _, st := range []domain.TaskState{domain.TaskStateRunning, domain.TaskStateRunning, domain.TaskStateSuccess} {
		if err := exec.ReportState(ctx, id, st, 0, ""); err != nil {
			t.Fatalf("ReportState(%s): %v", st, err)
		}
	}
	if len(got) != 1 {
		t.Fatalf("observer called %d times (%v), want once for the attempt", len(got), got)
	}
	if got[0].tenant != "default" {
		t.Errorf("tenant = %q, want default", got[0].tenant)
	}
	if got[0].seconds < 0.04 || got[0].seconds > 60 {
		t.Errorf("queue wait = %vs, want the ~50ms between queued and the first running report", got[0].seconds)
	}
}

// A running report on a row the scheduler has not marked queued yet (the agent
// beat launchQueued) has no queued_at to measure from and records nothing.
func TestReportStateWithoutQueuedAtRecordsNoQueueWaitIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	calls := 0
	exec.SetQueueWaitObserver(func(string, float64) { calls++ })
	dagID := fmt.Sprintf("queue_wait_none_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	id := auth.AgentIdentity{TenantID: defaultTenantID(), DagID: dagID, RunID: runUUID, TaskID: "load", TryNumber: 1}
	if err := exec.ReportState(ctx, id, domain.TaskStateRunning, 0, ""); err != nil {
		t.Fatalf("ReportState(running): %v", err)
	}
	if calls != 0 {
		t.Fatalf("observer called %d times, want 0 without a queued_at", calls)
	}
}

// OldestWaiting returns the age of the oldest queued attempt of the tenant.
func TestOldestWaitingReportsTheQueuedAttemptIntegration(t *testing.T) {
	pg, repo, sched, _, ctx := openExecPG(t)
	dagID := fmt.Sprintf("oldest_waiting_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "load", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateDagRun: %v", err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "load", domain.TaskStateQueued); err != nil {
		t.Fatalf("ApplyTransition to queued: %v", err)
	}
	ages, err := storage.NewTaskWaitStore(pg).OldestWaiting(ctx)
	if err != nil {
		t.Fatalf("OldestWaiting: %v", err)
	}
	for _, a := range ages {
		if a.Tenant == "default" && a.State == "queued" {
			if a.AgeSeconds < 0 || a.AgeSeconds > 3600 {
				t.Fatalf("queued age = %v, want a small non-negative age", a.AgeSeconds)
			}
			return
		}
	}
	t.Fatalf("OldestWaiting = %v, want a queued row for tenant default", ages)
}
