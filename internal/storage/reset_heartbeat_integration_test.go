//go:build integration

// Package storage_test: every reset rail clears last_heartbeat_at (ADR 0051
// amendment, PR A0).
//
// last_heartbeat_at is written only by RecordTaskHeartbeat. A rail that starts
// a new execution of the same row (infra re-place, retry, clear, reschedule
// re-dispatch, warm requeue, dispatch-failure backoff) must clear it, or the
// new attempt inherits the previous attempt's heartbeat. The new attempt
// reports RUNNING one full heartbeat interval before its first beat, and in
// that window ListAgentLostCandidates lists it with the stale value, so the
// agent-lost reaper fails a task that was never lost.
package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// staleHeartbeatFixture is one run with a single task "t" that heartbeated and
// whose heartbeat was then backdated past any reaper threshold.
type staleHeartbeatFixture struct {
	repo    *storage.Repository
	sched   *storage.SchedulerStore
	exec    *storage.ExecutionStore
	pg      *storage.Postgres
	ctx     context.Context
	dagID   string
	runID   string
	runUUID string
	tiID    string
}

// seedStaleHeartbeat drives task "t" to running, records one heartbeat, and
// backdates it by an hour, so any rail that leaves the column alone hands the
// next attempt a value the reaper reads as lost.
func seedStaleHeartbeat(t *testing.T, prefix string) *staleHeartbeatFixture {
	t.Helper()
	repo, sched, pg, ctx := openInfra(t)
	f := &staleHeartbeatFixture{
		repo: repo, sched: sched, exec: storage.NewExecutionStore(pg), pg: pg, ctx: ctx,
		dagID: fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano()), runID: "r1",
	}
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, f.dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", f.dagID, domain.DagRun{
		RunID: f.runID, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	f.runUUID = resolveRunUUID(t, sched, ctx, f.dagID)
	if err := sched.MaterializeTasks(ctx, f.runUUID, tasks); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	f.transition(t, domain.TaskStateRunning)
	if err := f.exec.RecordHeartbeat(ctx, auth.AgentIdentity{RunID: f.runUUID, TaskID: "t", TryNumber: 1}); err != nil {
		t.Fatalf("RecordHeartbeat: %v", err)
	}
	if _, err := pg.Pool.Exec(ctx,
		"UPDATE task_instances SET last_heartbeat_at = now() - interval '1 hour' WHERE dag_run_id=$1::uuid AND task_id='t'",
		f.runUUID); err != nil {
		t.Fatalf("backdate heartbeat: %v", err)
	}
	if err := pg.Pool.QueryRow(ctx,
		"SELECT id::text FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", f.runUUID).Scan(&f.tiID); err != nil {
		t.Fatalf("select ti id: %v", err)
	}
	return f
}

func (f *staleHeartbeatFixture) transition(t *testing.T, to domain.TaskState) {
	t.Helper()
	if err := f.sched.ApplyTransition(f.ctx, f.runUUID, "t", to); err != nil {
		t.Fatalf("transition %s: %v", to, err)
	}
}

func (f *staleHeartbeatFixture) setState(t *testing.T, state string) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(f.ctx,
		"UPDATE task_instances SET state=$2::task_state WHERE dag_run_id=$1::uuid AND task_id='t'",
		f.runUUID, state); err != nil {
		t.Fatalf("set state %s: %v", state, err)
	}
}

func (f *staleHeartbeatFixture) tryNumber(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT try_number FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", f.runUUID).Scan(&n); err != nil {
		t.Fatalf("select try_number: %v", err)
	}
	return n
}

// assertNotAgentLostCandidate is the A0 contract: after the rail ran and the
// new attempt reported RUNNING, but before its first heartbeat, the row has no
// heartbeat and the agent-lost reaper does not list it.
func (f *staleHeartbeatFixture) assertNotAgentLostCandidate(t *testing.T) {
	t.Helper()
	var hasHeartbeat bool
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT last_heartbeat_at IS NOT NULL FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'",
		f.runUUID).Scan(&hasHeartbeat); err != nil {
		t.Fatalf("select last_heartbeat_at: %v", err)
	}
	if hasHeartbeat {
		t.Errorf("the reset rail must clear last_heartbeat_at; the new attempt inherited the previous attempt's heartbeat")
	}
	cands, err := f.sched.ListAgentLostCandidates(f.ctx)
	if err != nil {
		t.Fatalf("ListAgentLostCandidates: %v", err)
	}
	if c := findAgentLostCandidate(cands, f.runUUID); c != nil {
		t.Errorf("a new attempt before its first heartbeat must not be an agent-lost candidate; got %+v", *c)
	}
}

// TestResetRailsClearLastHeartbeat runs every rail that starts a new execution
// of the same row and asserts the new attempt does not inherit the stale
// heartbeat of the attempt it replaced.
func TestResetRailsClearLastHeartbeat(t *testing.T) {
	rails := []struct {
		name string
		// rail moves the fixture from its running+stale-heartbeat start through
		// the rail under test; the caller then transitions it to running.
		rail func(t *testing.T, f *staleHeartbeatFixture)
	}{
		{"infra re-place after agent_lost", func(t *testing.T, f *staleHeartbeatFixture) {
			ok, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t))
			if err != nil || !ok {
				t.Fatalf("MarkTaskAgentLost ok=%v err=%v", ok, err)
			}
			applied, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t")
			if err != nil || !applied {
				t.Fatalf("ResetForInfraReplace applied=%v err=%v", applied, err)
			}
		}},
		{"retry", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "up_for_retry")
			applied, err := f.sched.ResetForRetry(f.ctx, f.runUUID, "t")
			if err != nil || !applied {
				t.Fatalf("ResetForRetry applied=%v err=%v", applied, err)
			}
		}},
		{"clear task (any state)", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "success")
			if _, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, []string{"t"}, false, domain.ClearOptions{}); err != nil {
				t.Fatalf("ClearTaskInstances: %v", err)
			}
		}},
		{"clear failed task", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "failed")
			n, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, []string{"t"}, true, domain.ClearOptions{})
			if err != nil || n != 1 {
				t.Fatalf("ClearTaskInstances(onlyFailed) n=%d err=%v", n, err)
			}
		}},
		{"clear all failed tasks", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "failed")
			n, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, nil, true, domain.ClearOptions{})
			if err != nil || n != 1 {
				t.Fatalf("ClearTaskInstances(all failed) n=%d err=%v", n, err)
			}
		}},
		{"reschedule re-dispatch", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "up_for_reschedule")
			if err := f.sched.RedispatchReschedule(f.ctx, f.runUUID, "t"); err != nil {
				t.Fatalf("RedispatchReschedule: %v", err)
			}
		}},
		{"warm requeue", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "queued")
			if err := f.exec.RequeueForRedispatch(f.ctx, f.runUUID, "t", f.tryNumber(t), f.attemptEpoch(t)); err != nil {
				t.Fatalf("RequeueForRedispatch: %v", err)
			}
		}},
		{"buffered dispatch requeue", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "queued")
			if ok, err := f.sched.RequeueDispatch(f.ctx, f.runUUID, "t", true, time.Now()); err != nil || !ok {
				t.Fatalf("RequeueDispatch ok=%v err=%v", ok, err)
			}
		}},
		{"dispatch failure backoff", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "scheduled")
			if err := f.sched.RecordDispatchFailure(f.ctx, f.runUUID, "t", time.Now()); err != nil {
				t.Fatalf("RecordDispatchFailure: %v", err)
			}
		}},
	}
	for _, rc := range rails {
		t.Run(rc.name, func(t *testing.T) {
			f := seedStaleHeartbeat(t, "reset_hb")
			rc.rail(t, f)
			// The new attempt reports RUNNING before its first heartbeat.
			f.transition(t, domain.TaskStateRunning)
			f.assertNotAgentLostCandidate(t)
		})
	}
}
