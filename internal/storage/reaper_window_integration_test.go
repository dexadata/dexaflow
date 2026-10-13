//go:build integration

// Package storage_test: the reaper candidate lists page past live rows (R-07,
// #1597). A fixed "oldest 100" let 100 live attempts hide a lost one forever.
package storage_test

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/storage"
)

// windowReaper is the production Reaper over a fake cluster.
func windowReaper(sched executor.ReaperStore, cs *fake.Clientset) *executor.Reaper {
	podExec := executor.NewKubernetesExecutor(cs, "ns")
	warm := executor.NewKubernetesWarmPods(cs, "ns", nil)
	return executor.NewReaper(sched, podExec, nil, warm, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), executor.DefaultReaperConfig(), nil)
}

// seedWindowRun registers a DAG of executor.ReapPageSize+1 tasks t0..tN and
// starts one running run of it.
func seedWindowRun(t *testing.T, prefix string) (*storage.Postgres, *storage.SchedulerStore, string, context.Context) {
	t.Helper()
	pg, repo, sched, _, ctx := openExecPG(t)
	dagID := fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	var tasks []domain.TaskSpec
	for i := 0; i <= executor.ReapPageSize; i++ {
		tasks = append(tasks, domain.TaskSpec{TaskID: "t" + strconv.Itoa(i), Type: domain.TaskTypePython})
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatal(err)
	}
	runUUID := resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatal(err)
	}
	// A page of old active rows left in this shared database would sit ahead
	// of every later test's rows in the reaper lists, so settle them on exit.
	t.Cleanup(func() {
		if _, err := pg.Pool.Exec(context.Background(),
			`UPDATE task_instances SET state = 'success' WHERE dag_run_id = $1::uuid AND state IN ('queued', 'running')`, runUUID); err != nil {
			t.Errorf("settle the window rows: %v", err)
		}
	})
	return pg, sched, runUUID, ctx
}

func windowPod(runUUID, task string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "p-" + task, Namespace: "ns", Labels: map[string]string{
			"leoflow.io/run-id": runUUID, "leoflow.io/task-id": task, "leoflow.io/try-number": "1",
		}},
		Status: corev1.PodStatus{Phase: phase},
	}
}

func taskState(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID, task string) string {
	t.Helper()
	var st string
	if err := pg.Pool.QueryRow(ctx, `SELECT state::text FROM task_instances WHERE dag_run_id = $1::uuid AND task_id = $2`, runUUID, task).Scan(&st); err != nil {
		t.Fatal(err)
	}
	return st
}

// TestPodLostReapsBehindAFullPageOfLiveRowsIntegration: 100 attempts running
// for two hours with live pods, and a newer attempt whose pod vanished before
// its first heartbeat. Before the fix every sweep listed the same 100 oldest
// rows and the lost attempt stayed `running`; now the second sweep reaches it.
func TestPodLostReapsBehindAFullPageOfLiveRowsIntegration(t *testing.T) {
	pg, sched, runUUID, ctx := seedWindowRun(t, "reap_window_running")
	// Running rows left by other tests in this shared database would sit ahead
	// of these; restart their clock so the grace keeps them off the list.
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET started_at = now() WHERE state = 'running' AND dag_run_id <> $1::uuid`, runUUID); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset()
	lost := "t" + strconv.Itoa(executor.ReapPageSize)
	for i := 0; i <= executor.ReapPageSize; i++ {
		id := "t" + strconv.Itoa(i)
		age, hb := "2 hours", "now() - interval '5 seconds'"
		if id == lost {
			age, hb = "10 minutes", "NULL"
		}
		if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state = 'running', queued_at = now() - $3::interval, started_at = now() - $3::interval, last_heartbeat_at = `+hb+` WHERE dag_run_id = $1::uuid AND task_id = $2`, runUUID, id, age); err != nil {
			t.Fatal(err)
		}
		if id != lost {
			if err := cs.Tracker().Add(windowPod(runUUID, id, corev1.PodRunning)); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := windowReaper(sched, cs)
	for sweep := 0; sweep < 2; sweep++ {
		if err := r.ReapOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st := taskState(t, pg, ctx, runUUID, lost); st != "failed" {
		t.Fatalf("the pod-less attempt behind %d live ones must be reaped within two sweeps, state = %s", executor.ReapPageSize, st)
	}
	if st := taskState(t, pg, ctx, runUUID, "t0"); st != "running" {
		t.Errorf("a live attempt must not be reaped, t0 state = %s", st)
	}
}

// TestDispatchLostReapsBehindAFullPageOfPendingRowsIntegration: 100 attempts
// queued for 20 minutes whose pods are Pending (a capacity shortage), and a
// newer attempt queued 10 minutes ago whose create never happened. The second
// sweep reaches and reaps it.
func TestDispatchLostReapsBehindAFullPageOfPendingRowsIntegration(t *testing.T) {
	pg, sched, runUUID, ctx := seedWindowRun(t, "reap_window_queued")
	// Queued rows of other tests restart their clock: the threshold, applied
	// in SQL, keeps them off the list.
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET queued_at = now() WHERE state = 'queued' AND dag_run_id <> $1::uuid`, runUUID); err != nil {
		t.Fatal(err)
	}
	cs := fake.NewClientset()
	lost := "t" + strconv.Itoa(executor.ReapPageSize)
	for i := 0; i <= executor.ReapPageSize; i++ {
		id := "t" + strconv.Itoa(i)
		age := "20 minutes"
		if id == lost {
			age = "10 minutes"
		}
		if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state = 'queued', queued_at = now() - $3::interval WHERE dag_run_id = $1::uuid AND task_id = $2`, runUUID, id, age); err != nil {
			t.Fatal(err)
		}
		if id != lost {
			if err := cs.Tracker().Add(windowPod(runUUID, id, corev1.PodPending)); err != nil {
				t.Fatal(err)
			}
		}
	}
	r := windowReaper(sched, cs)
	for sweep := 0; sweep < 2; sweep++ {
		if err := r.ReapOnce(ctx); err != nil {
			t.Fatal(err)
		}
	}
	if st := taskState(t, pg, ctx, runUUID, lost); st != "failed" {
		t.Fatalf("the lost dispatch behind %d Pending ones must be reaped within two sweeps, state = %s", executor.ReapPageSize, st)
	}
	if st := taskState(t, pg, ctx, runUUID, "t0"); st != "queued" {
		t.Errorf("a queued attempt with a Pending pod must not be reaped, t0 state = %s", st)
	}
}

// TestReaperListsPageByKeysetIntegration is the query contract: a page is at
// most executor.ReapPageSize rows in (time, id) order, the next page starts
// strictly after the cursor, and the threshold or grace keeps rows inside it
// off the list.
func TestReaperListsPageByKeysetIntegration(t *testing.T) {
	pg, sched, runUUID, ctx := seedWindowRun(t, "reap_window_keyset")
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET started_at = now() WHERE state = 'running' AND dag_run_id <> $1::uuid`, runUUID); err != nil {
		t.Fatal(err)
	}
	// Every row starts at the same instant, so only the id orders them.
	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state = 'running', started_at = date_trunc('second', now()) - interval '1 hour' WHERE dag_run_id = $1::uuid`, runUUID); err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	after := executor.ReapCursor{}
	for page := 0; page < 3; page++ {
		cands, err := sched.ListRunningTasks(ctx, time.Minute, after)
		if err != nil {
			t.Fatal(err)
		}
		if len(cands) > executor.ReapPageSize {
			t.Fatalf("page %d has %d rows, more than the page size", page, len(cands))
		}
		for _, c := range cands {
			if c.DagRunID != runUUID {
				continue
			}
			if seen[c.TaskID] {
				t.Fatalf("page %d listed %s again", page, c.TaskID)
			}
			seen[c.TaskID] = true
		}
		if len(cands) < executor.ReapPageSize {
			break
		}
		last := cands[len(cands)-1]
		after = executor.ReapCursor{At: last.RunningSince, ID: last.TaskInstanceID}
	}
	if len(seen) != executor.ReapPageSize+1 {
		t.Fatalf("paging must list every running row once, saw %d of %d", len(seen), executor.ReapPageSize+1)
	}
	if cands, err := sched.ListRunningTasks(ctx, 2*time.Hour, executor.ReapCursor{}); err != nil {
		t.Fatal(err)
	} else if findPodLostCandidate(cands, runUUID, "t0") != nil {
		t.Errorf("an attempt inside the grace must not be listed")
	}

	if _, err := pg.Pool.Exec(ctx, `UPDATE task_instances SET state = 'queued', queued_at = now() - interval '1 minute' WHERE dag_run_id = $1::uuid AND task_id = 't0'`, runUUID); err != nil {
		t.Fatal(err)
	}
	queued, err := sched.ListStaleQueuedCandidates(ctx, 3*time.Minute, executor.ReapCursor{})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range queued {
		if c.DagRunID == runUUID && c.TaskID == "t0" {
			t.Errorf("an attempt queued inside the dispatch-lost threshold must not be listed")
		}
	}
}

// allStaleQueued pages through the whole dispatch-lost list, so a test that
// looks for its own row does not depend on how many older rows other tests
// left in this shared database.
func allStaleQueued(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, threshold time.Duration) []executor.StaleQueuedCandidate {
	t.Helper()
	var out []executor.StaleQueuedCandidate
	after := executor.ReapCursor{}
	for {
		page, err := sched.ListStaleQueuedCandidates(ctx, threshold, after)
		if err != nil {
			t.Fatalf("ListStaleQueuedCandidates: %v", err)
		}
		out = append(out, page...)
		if len(page) < executor.ReapPageSize {
			return out
		}
		last := page[len(page)-1]
		after = executor.ReapCursor{At: last.QueuedAt, ID: last.TaskInstanceID}
	}
}

// allRunning is allStaleQueued for the running list.
func allRunning(t *testing.T, sched *storage.SchedulerStore, ctx context.Context, grace time.Duration) []executor.PodLostCandidate {
	t.Helper()
	var out []executor.PodLostCandidate
	after := executor.ReapCursor{}
	for {
		page, err := sched.ListRunningTasks(ctx, grace, after)
		if err != nil {
			t.Fatalf("ListRunningTasks: %v", err)
		}
		out = append(out, page...)
		if len(page) < executor.ReapPageSize {
			return out
		}
		last := page[len(page)-1]
		after = executor.ReapCursor{At: last.RunningSince, ID: last.TaskInstanceID}
	}
}
