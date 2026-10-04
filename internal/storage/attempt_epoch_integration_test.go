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
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/migrations"
	"github.com/golang-migrate/migrate/v4"
	_ "github.com/golang-migrate/migrate/v4/database/pgx/v5" // registers the "pgx5" migrate scheme
	"github.com/golang-migrate/migrate/v4/source/iofs"
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
		{"buffered dispatch requeue", func(t *testing.T, f *staleHeartbeatFixture) {
			f.setState(t, "queued")
			if ok, err := f.sched.RequeueDispatch(f.ctx, f.runUUID, "t", true, time.Now()); err != nil || !ok {
				t.Fatalf("RequeueDispatch ok=%v err=%v", ok, err)
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
	if ok, err := f.sched.RequeueDispatch(f.ctx, f.runUUID, "t", true, time.Now()); err != nil || ok {
		t.Fatalf("RequeueDispatch on a running row: ok=%v err=%v", ok, err)
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

// TestAttemptEpochMigrationUpDownUp applies every migration to a scratch
// database, rolls the attempt-epoch migration back and forward again, and
// checks the columns at each step. It also runs the previous release's archive
// statement against the migrated schema: the migration hook runs before the
// old pods are replaced, and a rollback does not run the down migration, so the
// old binary must keep working on this schema (expand only).
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
	assertPreviousReleaseArchiveRuns(ctx, t, pg)

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
	if def != "UNIQUE (task_instance_id, try_number)" {
		t.Errorf("the history unique key must stay (task_instance_id, try_number) for the previous release, got %q", def)
	}
}

// assertPreviousReleaseArchiveRuns seeds one task instance and runs the
// previous release's ResetTaskInstanceToNone text (ON CONFLICT on the old key,
// no attempt_epoch anywhere) twice against the migrated schema.
func assertPreviousReleaseArchiveRuns(ctx context.Context, t *testing.T, pg *storage.Postgres) {
	t.Helper()
	var runID string
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
), ti AS (
    INSERT INTO task_instances (tenant_id, dag_run_id, task_id, try_number, operator)
    SELECT d.tenant_id, r.id, 't', 1, 'python' FROM d, r RETURNING dag_run_id
)
SELECT dag_run_id::text FROM ti`).Scan(&runID); err != nil {
		t.Fatalf("seed scratch task instance: %v", err)
	}
	const previousReleaseReset = `
WITH archived AS (
    INSERT INTO task_instance_history (
        task_instance_id, try_number, state,
        queued_at, scheduled_at, started_at, ended_at, duration_seconds,
        exit_code, error_message, hostname, pod_name, node_name, note
    )
    SELECT
        src.id, src.try_number, src.state,
        src.queued_at, src.scheduled_at, src.started_at, src.ended_at, src.duration_seconds,
        src.exit_code, src.error_message, src.hostname, src.pod_name, src.node_name, src.note
    FROM task_instances src
    WHERE src.dag_run_id = $1 AND src.task_id = $2
    ON CONFLICT (task_instance_id, try_number) DO NOTHING
    RETURNING task_instance_id
)
UPDATE task_instances ti
SET state = 'none', started_at = NULL, ended_at = NULL, queued_at = NULL,
    scheduled_at = NULL, dispatch_attempts = 0, next_dispatch_at = NULL,
    reschedule_at = NULL, first_reschedule_at = NULL, last_failure_kind = NULL,
    warm_worker_id = NULL, try_number = ti.try_number + 1
WHERE ti.dag_run_id = $1 AND ti.task_id = $2`
	for i := 0; i < 2; i++ {
		if _, err := pg.Pool.Exec(ctx, previousReleaseReset, runID, "t"); err != nil {
			t.Fatalf("the previous release's archive statement must run on the migrated schema (#%d): %v", i+1, err)
		}
	}
}
