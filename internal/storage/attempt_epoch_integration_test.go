//go:build integration

// Package storage_test: the attempt epoch (ADR 0051 amendment, PR A1).
//
// task_instances.attempt_epoch identifies one execution attempt of a row,
// separately from the user-facing try_number. It must strictly increase on
// every rail that can start a new execution and on every dispatch claim, so no
// two executions of one row ever share (try_number, attempt_epoch).
package storage_test

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
	"github.com/jackc/pgx/v5"
)

func (f *staleHeartbeatFixture) attemptEpoch(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.pg.Pool.QueryRow(f.ctx,
		"SELECT attempt_epoch FROM task_instances WHERE dag_run_id=$1::uuid AND task_id='t'", f.runUUID).Scan(&n); err != nil {
		t.Fatalf("select attempt_epoch: %v", err)
	}
	return n
}

// epochRails is every rail that can start a new execution of a row, keyed by
// a readable name, each moving the fixture from running through the rail.
func epochRails() []struct {
	name string
	rail func(t *testing.T, f *staleHeartbeatFixture)
} {
	return []struct {
		name string
		rail func(t *testing.T, f *staleHeartbeatFixture)
	}{
		{"infra re-place", func(t *testing.T, f *staleHeartbeatFixture) {
			if ok, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID); err != nil || !ok {
				t.Fatalf("MarkTaskAgentLost ok=%v err=%v", ok, err)
			}
			if applied, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !applied {
				t.Fatalf("ResetForInfraReplace applied=%v err=%v", applied, err)
			}
		}},
		{"retry", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "up_for_retry")
			if applied, err := f.sched.ResetForRetry(f.ctx, f.runUUID, "t"); err != nil || !applied {
				t.Fatalf("ResetForRetry applied=%v err=%v", applied, err)
			}
		}},
		{"clear task", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "success")
			if _, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, []string{"t"}, false, domain.ClearOptions{}); err != nil {
				t.Fatalf("ClearTaskInstances: %v", err)
			}
		}},
		{"clear failed task", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "failed")
			if n, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, []string{"t"}, true, domain.ClearOptions{}); err != nil || n != 1 {
				t.Fatalf("ClearTaskInstances(onlyFailed) n=%d err=%v", n, err)
			}
		}},
		{"clear all failed tasks", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "failed")
			if n, err := f.repo.ClearTaskInstances(f.ctx, "default", f.dagID, f.runID, nil, true, domain.ClearOptions{}); err != nil || n != 1 {
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
			if err := f.exec.RequeueForRedispatch(f.ctx, f.runUUID, "t", f.tryNumber(t)); err != nil {
				t.Fatalf("RequeueForRedispatch: %v", err)
			}
		}},
		{"dispatch failure", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "scheduled")
			if err := f.sched.RecordDispatchFailure(f.ctx, f.runUUID, "t", time.Now()); err != nil {
				t.Fatalf("RecordDispatchFailure: %v", err)
			}
		}},
	}
}

// TestAttemptEpochBumpedByEveryRail: each rail that starts a new execution of
// the row strictly increases attempt_epoch, so the attempt it superseded is
// fenced before the next dispatch runs.
func TestAttemptEpochBumpedByEveryRail(t *testing.T) {
	for _, rc := range epochRails() {
		t.Run(rc.name, func(t *testing.T) {
			f := seedStaleHeartbeat(t, "epoch_rail")
			before := f.attemptEpoch(t)
			rc.rail(t, f)
			if after := f.attemptEpoch(t); after <= before {
				t.Errorf("attempt_epoch must strictly increase on the %s rail: before=%d after=%d", rc.name, before, after)
			}
		})
	}
}

// TestAttemptEpochUnchangedWhenRailGuardMisses: a rail whose source-state
// guard does not match leaves the epoch alone, so a stale decision cannot
// fence the live attempt.
func TestAttemptEpochUnchangedWhenRailGuardMisses(t *testing.T) {
	f := seedStaleHeartbeat(t, "epoch_guard")
	before := f.attemptEpoch(t)
	// The row is running: none of these guarded rails applies to it.
	if applied, err := f.sched.ResetForRetry(f.ctx, f.runUUID, "t"); err != nil || applied {
		t.Fatalf("ResetForRetry on a running row: applied=%v err=%v", applied, err)
	}
	if applied, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || applied {
		t.Fatalf("ResetForInfraReplace on a running row: applied=%v err=%v", applied, err)
	}
	if err := f.sched.RedispatchReschedule(f.ctx, f.runUUID, "t"); err != nil {
		t.Fatalf("RedispatchReschedule: %v", err)
	}
	if err := f.exec.RequeueForRedispatch(f.ctx, f.runUUID, "t", f.tryNumber(t)); err != nil {
		t.Fatalf("RequeueForRedispatch: %v", err)
	}
	if err := f.sched.RecordDispatchFailure(f.ctx, f.runUUID, "t", time.Now()); err != nil {
		t.Fatalf("RecordDispatchFailure: %v", err)
	}
	if after := f.attemptEpoch(t); after != before {
		t.Errorf("a rail whose guard missed must not move attempt_epoch: before=%d after=%d", before, after)
	}
}

// TestResolveTaskClaimsAttemptEpoch: the dispatcher claims a fresh epoch every
// time it resolves a row, including two dispatches of one row with no reset
// between them (the queued write of the first dispatch failed, so the next
// tick dispatches again). Each dispatch gets its own epoch, and the resolved
// value is the one now on the row.
func TestResolveTaskClaimsAttemptEpoch(t *testing.T) {
	f := seedStaleHeartbeat(t, "epoch_claim")
	f.setState(t, "scheduled")
	if got := f.attemptEpoch(t); got != 0 {
		t.Fatalf("precondition: a new row starts at epoch 0, got %d", got)
	}

	first, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t")
	if err != nil {
		t.Fatalf("first ResolveTask: %v", err)
	}
	if first.AttemptEpoch != 1 || f.attemptEpoch(t) != 1 {
		t.Fatalf("the first dispatch after the upgrade must claim epoch 1: resolved=%d row=%d", first.AttemptEpoch, f.attemptEpoch(t))
	}
	second, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t")
	if err != nil {
		t.Fatalf("second ResolveTask: %v", err)
	}
	if second.AttemptEpoch <= first.AttemptEpoch {
		t.Errorf("two dispatches of one row must not share an epoch: first=%d second=%d", first.AttemptEpoch, second.AttemptEpoch)
	}
	if row := f.attemptEpoch(t); row != second.AttemptEpoch {
		t.Errorf("the resolved epoch must be the row's claimed value: resolved=%d row=%d", second.AttemptEpoch, row)
	}
	if second.TryNumber != first.TryNumber || second.TaskInstanceID != f.tiID {
		t.Errorf("the claim must not change the attempt's try or row: %+v", second)
	}

	// Buffered dispatch resolves after the scheduler already recorded queued.
	f.setState(t, "queued")
	third, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t")
	if err != nil {
		t.Fatalf("ResolveTask on a queued row (buffered dispatch): %v", err)
	}
	if third.AttemptEpoch <= second.AttemptEpoch {
		t.Errorf("a buffered dispatch must claim a fresh epoch: second=%d third=%d", second.AttemptEpoch, third.AttemptEpoch)
	}
}

// TestResolveTaskRefusesRowPastDispatch: the claim is guarded to the
// pre-dispatch states. A row that is already running or settled is not
// dispatched again and keeps its epoch, so a late or duplicate dispatch can
// never fence the live attempt.
func TestResolveTaskRefusesRowPastDispatch(t *testing.T) {
	for _, state := range []string{"running", "success", "failed", "up_for_retry", "up_for_reschedule"} {
		t.Run(state, func(t *testing.T) {
			f := seedStaleHeartbeat(t, "epoch_claim_guard")
			f.setState(t, state)
			before := f.attemptEpoch(t)
			if _, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t"); err == nil {
				t.Errorf("ResolveTask must refuse a %s row", state)
			}
			if after := f.attemptEpoch(t); after != before {
				t.Errorf("a refused claim must not move attempt_epoch: before=%d after=%d", before, after)
			}
		})
	}
}

// TestLegacyRowClaimsEpochOneOnFirstDispatch is the mixed-version upgrade case:
// an old binary reset the row without bumping (the column is ignored by it, so
// the row stays at the migration's default 0). The first dispatch by a new
// binary claims epoch 1, so the legacy attempt (no epoch, read as 0) can never
// alias the new one.
func TestLegacyRowClaimsEpochOneOnFirstDispatch(t *testing.T) {
	f := seedStaleHeartbeat(t, "epoch_legacy")
	// What an old binary's ResetTaskInstanceInfraReplace writes: no epoch bump.
	if _, err := f.pg.Pool.Exec(f.ctx,
		"UPDATE task_instances SET state='scheduled', infra_attempts=infra_attempts+1, attempt_epoch=0 WHERE dag_run_id=$1::uuid AND task_id='t'",
		f.runUUID); err != nil {
		t.Fatalf("legacy reset: %v", err)
	}
	r, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t")
	if err != nil {
		t.Fatalf("ResolveTask: %v", err)
	}
	if r.AttemptEpoch < 1 {
		t.Errorf("the first post-upgrade dispatch must claim an epoch of at least 1, got %d", r.AttemptEpoch)
	}
}

// TestHistoryKeepsEveryInfraReplaceOfOneTry: two infra re-places of one try
// archive two history rows (one per epoch). Before the epoch the unique key
// was (task_instance_id, try_number) and ON CONFLICT DO NOTHING dropped the
// second one (#863).
func TestHistoryKeepsEveryInfraReplaceOfOneTry(t *testing.T) {
	f := seedStaleHeartbeat(t, "epoch_history")
	for i := 0; i < 2; i++ {
		f.setState(t, "scheduled")
		if _, err := f.exec.ResolveTask(f.ctx, f.runUUID, "t"); err != nil {
			t.Fatalf("ResolveTask #%d: %v", i+1, err)
		}
		f.transition(t, domain.TaskStateRunning)
		if ok, err := f.sched.MarkTaskAgentLost(f.ctx, f.tiID); err != nil || !ok {
			t.Fatalf("MarkTaskAgentLost #%d ok=%v err=%v", i+1, ok, err)
		}
		if applied, err := f.sched.ResetForInfraReplace(f.ctx, f.runUUID, "t"); err != nil || !applied {
			t.Fatalf("ResetForInfraReplace #%d applied=%v err=%v", i+1, applied, err)
		}
	}
	rows, err := f.pg.Pool.Query(f.ctx,
		"SELECT try_number, attempt_epoch FROM task_instance_history WHERE task_instance_id=$1::uuid ORDER BY attempt_epoch", f.tiID)
	if err != nil {
		t.Fatalf("select history: %v", err)
	}
	type attempt struct{ try, epoch int }
	got, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (attempt, error) {
		var a attempt
		return a, r.Scan(&a.try, &a.epoch)
	})
	if err != nil {
		t.Fatalf("scan history: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("two infra re-places of one try must archive two history rows, got %+v", got)
	}
	if got[0].try != got[1].try || got[0].epoch == got[1].epoch {
		t.Errorf("the two archived attempts must share the try and differ in epoch, got %+v", got)
	}
}

// TestAttemptEpochMigrationUpDownUp applies every migration to a scratch
// database, rolls the attempt-epoch migration back and forward again, and
// checks the columns and the history unique key at each step. The down
// migration must succeed even when history holds two epochs of one try.
func TestAttemptEpochMigrationUpDownUp(t *testing.T) {
	raw := os.Getenv("DATABASE_URL")
	if raw == "" {
		t.Skip("DATABASE_URL must point at a Postgres server for integration tests")
	}
	ctx := context.Background()
	admin, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: raw})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(admin.Close)
	dbName := fmt.Sprintf("epoch_mig_%d", time.Now().UnixNano())
	if _, err := admin.Pool.Exec(ctx, "CREATE DATABASE "+dbName); err != nil {
		skipOrFatal(t, "creating scratch database", err)
	}
	t.Cleanup(func() {
		if _, err := admin.Pool.Exec(context.Background(), "DROP DATABASE IF EXISTS "+dbName+" WITH (FORCE)"); err != nil {
			t.Errorf("dropping scratch database: %v", err)
		}
	})
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("DATABASE_URL is not a URL: %v", err)
	}
	u.Path = "/" + dbName
	scratchURL := u.String()
	u.Scheme = "pgx5"
	src, err := iofs.New(migrations.Files, ".")
	if err != nil {
		t.Fatalf("loading migrations: %v", err)
	}
	m, err := migrate.NewWithSourceInstance("iofs", src, u.String())
	if err != nil {
		t.Fatalf("migrator: %v", err)
	}
	defer func() { _, _ = m.Close() }()
	if err := m.Up(); err != nil {
		t.Fatalf("up: %v", err)
	}
	latest, _, err := m.Version()
	if err != nil {
		t.Fatalf("version: %v", err)
	}
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: scratchURL})
	if err != nil {
		t.Fatalf("connect scratch: %v", err)
	}
	defer pg.Close()

	assertEpochSchema(ctx, t, pg, true)
	seedTwoEpochsOfOneTry(ctx, t, pg)

	if err := m.Steps(-1); err != nil {
		t.Fatalf("down one step from v%d: %v", latest, err)
	}
	assertEpochSchema(ctx, t, pg, false)
	if err := m.Up(); err != nil {
		t.Fatalf("up again: %v", err)
	}
	assertEpochSchema(ctx, t, pg, true)
}

func assertEpochSchema(ctx context.Context, t *testing.T, pg *storage.Postgres, want bool) {
	t.Helper()
	for _, table := range []string{"task_instances", "task_instance_history"} {
		var present bool
		if err := pg.Pool.QueryRow(ctx,
			"SELECT EXISTS (SELECT 1 FROM information_schema.columns WHERE table_name=$1 AND column_name='attempt_epoch')",
			table).Scan(&present); err != nil {
			t.Fatalf("inspect %s: %v", table, err)
		}
		if present != want {
			t.Errorf("%s.attempt_epoch present=%v, want %v", table, present, want)
		}
	}
	var def string
	if err := pg.Pool.QueryRow(ctx,
		"SELECT pg_get_constraintdef(oid) FROM pg_constraint WHERE conname='task_instance_history_unique'").Scan(&def); err != nil {
		t.Fatalf("inspect history unique key: %v", err)
	}
	if hasEpoch := strings.Contains(def, "attempt_epoch"); hasEpoch != want {
		t.Errorf("history unique key %q: includes attempt_epoch=%v, want %v", def, hasEpoch, want)
	}
}

// seedTwoEpochsOfOneTry writes the shape the new key allows and the old key
// forbids, so the down migration has to handle it.
func seedTwoEpochsOfOneTry(ctx context.Context, t *testing.T, pg *storage.Postgres) {
	t.Helper()
	var tiID string
	if err := pg.Pool.QueryRow(ctx, `
WITH d AS (
    INSERT INTO dags (tenant_id, dag_id) SELECT id, 'epoch_mig' FROM tenants WHERE name = 'default'
    RETURNING id, tenant_id
), v AS (
    INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash)
    SELECT id, 'v1', 'img:v1', '{}'::jsonb, 'h' FROM d RETURNING id, dag_id
), r AS (
    INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
    SELECT d.tenant_id, v.dag_id, v.id, 'r1', now(), 'running', 'manual' FROM d, v RETURNING id
)
INSERT INTO task_instances (tenant_id, dag_run_id, task_id, try_number, operator)
SELECT d.tenant_id, r.id, 't', 1, 'python' FROM d, r RETURNING id::text`).Scan(&tiID); err != nil {
		t.Fatalf("seed scratch task instance: %v", err)
	}
	for epoch := 0; epoch < 2; epoch++ {
		if _, err := pg.Pool.Exec(ctx,
			"INSERT INTO task_instance_history (task_instance_id, try_number, state, attempt_epoch) VALUES ($1::uuid, 1, 'failed', $2)",
			tiID, epoch); err != nil {
			t.Fatalf("seed history epoch %d: %v", epoch, err)
		}
	}
}
