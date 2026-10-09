//go:build integration

// Package storage_test: a durable SUCCESS overrides a provisional infra mark
// (ADR 0052 amendment, part 1, PR B1, #1124, #900).
package storage_test

import (
	"testing"
)

// overrideFixture is a run whose task "t" was marked agent_lost on
// Kubernetes (provisional) while it was running.
func overrideFixture(t *testing.T, prefix string) *staleHeartbeatFixture {
	t.Helper()
	f := seedStaleHeartbeat(t, prefix)
	f.sched.SetProvisionalInfraMarks(true)
	f.markAgentLost(t)
	return f
}

func (f *staleHeartbeatFixture) failureKind(t *testing.T) *string {
	t.Helper()
	var kind *string
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT last_failure_kind FROM task_instances WHERE id=$1::uuid", f.tiID).Scan(&kind); err != nil {
		t.Fatalf("select last_failure_kind: %v", err)
	}
	return kind
}

func (f *staleHeartbeatFixture) execSQL(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := f.pg.Pool.Exec(f.ctx, sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// TestSuccessRecordAfterAgentLostSettlesSuccess is the #1124 contract: a task
// that finished during a control plane outage was marked agent_lost; its
// durable SUCCESS record then reaches the reconciler. The active settle finds
// no active row, and the override turns the guess into success.
func TestSuccessRecordAfterAgentLostSettlesSuccess(t *testing.T) {
	f := overrideFixture(t, "override_agent_lost")
	try, epoch := f.tryNumber(t), f.attemptEpoch(t)

	settled, err := f.exec.SucceedTaskIfActive(f.ctx, f.tiID, try, epoch)
	if err != nil || settled {
		t.Fatalf("the active settle must change no row on a reaped task: settled=%v err=%v", settled, err)
	}
	o, ok, err := f.exec.SucceedTaskOverInfraMark(f.ctx, f.tiID, try, epoch)
	if err != nil || !ok {
		t.Fatalf("SucceedTaskOverInfraMark ok=%v err=%v", ok, err)
	}
	f.wantState(t, "after the override", "success")
	if k := f.failureKind(t); k != nil {
		t.Errorf("the override must clear last_failure_kind, got %q", *k)
	}
	if o.Mark != "agent_lost" || o.TaskID != "t" || o.DagRunID != f.runUUID || o.DagID != f.dagID || o.TenantID == "" {
		t.Errorf("the override must name the mark and the attempt's log location, got %+v", o)
	}
	if _, ok, err := f.exec.SucceedTaskOverInfraMark(f.ctx, f.tiID, try, epoch); err != nil || ok {
		t.Fatalf("a second override is a no-op: ok=%v err=%v", ok, err)
	}
}

// TestInfraOverrideIsANoOpOutsideItsWindow: the override applies only to the
// exact attempt's provisional infra mark, inside the confirmation window, on
// a running run.
func TestInfraOverrideIsANoOpOutsideItsWindow(t *testing.T) {
	cases := map[string]struct {
		prepare func(t *testing.T, f *staleHeartbeatFixture)
		epoch   func(f *staleHeartbeatFixture, epoch int) int
		want    string
	}{
		"another epoch": {
			epoch: func(_ *staleHeartbeatFixture, e int) int { return e + 1 },
			want:  "failed",
		},
		"an application failure": {
			prepare: func(t *testing.T, f *staleHeartbeatFixture) {
				f.execSQL(t, "UPDATE task_instances SET last_failure_kind = 'app' WHERE id=$1::uuid", f.tiID)
			},
			want: "failed",
		},
		"a failure of no kind": {
			prepare: func(t *testing.T, f *staleHeartbeatFixture) {
				f.execSQL(t, "UPDATE task_instances SET last_failure_kind = NULL WHERE id=$1::uuid", f.tiID)
			},
			want: "failed",
		},
		"a finalized run": {
			prepare: func(t *testing.T, f *staleHeartbeatFixture) {
				f.execSQL(t, "UPDATE dag_runs SET state = 'failed' WHERE id=$1::uuid", f.runUUID)
			},
			want: "failed",
		},
		"a confirmed mark": {
			prepare: func(t *testing.T, f *staleHeartbeatFixture) {
				if ok, err := f.sched.ConfirmInfraFailure(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || !ok {
					t.Fatalf("ConfirmInfraFailure ok=%v err=%v", ok, err)
				}
			},
			want: "failed",
		},
		"a mark past the confirmation valve": {
			prepare: func(t *testing.T, f *staleHeartbeatFixture) {
				f.execSQL(t, "UPDATE task_instances SET ended_at = now() - interval '3 minutes' WHERE id=$1::uuid", f.tiID)
			},
			want: "failed",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			f := overrideFixture(t, "override_noop")
			if tc.prepare != nil {
				tc.prepare(t, f)
			}
			epoch := f.attemptEpoch(t)
			if tc.epoch != nil {
				epoch = tc.epoch(f, epoch)
			}
			if _, ok, err := f.exec.SucceedTaskOverInfraMark(f.ctx, f.tiID, f.tryNumber(t), epoch); err != nil || ok {
				t.Fatalf("the override must be a no-op: ok=%v err=%v", ok, err)
			}
			f.wantState(t, name, tc.want)
		})
	}
}

// TestSucceedTaskIfActiveReportsTheSettle: the active settle tells a settle
// that changed the row apart from one that found nothing active.
func TestSucceedTaskIfActiveReportsTheSettle(t *testing.T) {
	f := seedStaleHeartbeat(t, "succeed_active")
	settled, err := f.exec.SucceedTaskIfActive(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t))
	if err != nil || !settled {
		t.Fatalf("a running attempt settles: settled=%v err=%v", settled, err)
	}
	f.wantState(t, "after the settle", "success")
}

// TestUserMarkFailedIsNeverOverridden: a user who marks a reaped task failed
// gives a verdict, not a guess. The mark-state write clears the infra kind and
// confirms the row, so neither the re-place nor the override touches it.
func TestUserMarkFailedIsNeverOverridden(t *testing.T) {
	f := overrideFixture(t, "override_user_failed")
	if err := f.repo.SetTaskInstanceState(f.ctx, "default", f.dagID, f.runID, "t", "failed"); err != nil {
		t.Fatalf("SetTaskInstanceState: %v", err)
	}
	if k := f.failureKind(t); k != nil {
		t.Fatalf("a user's failed must not keep the infra kind, got %q", *k)
	}
	if f.confirmedAt(t) == nil {
		t.Errorf("a user's verdict must be confirmed")
	}
	if _, ok, err := f.exec.SucceedTaskOverInfraMark(f.ctx, f.tiID, f.tryNumber(t), f.attemptEpoch(t)); err != nil || ok {
		t.Fatalf("a user's failed must never be overridden: ok=%v err=%v", ok, err)
	}
	if ok, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || ok {
		t.Fatalf("a user's failed must never be re-placed: ok=%v err=%v", ok, err)
	}
	f.wantState(t, "after the user's verdict", "failed")
}
