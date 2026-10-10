//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerSecondVersion publishes a new version of an existing DAG and returns
// its image, so a test can tell which version a run is pinned to by reading the
// image the run resolves.
func registerSecondVersion(t *testing.T, repo *storage.Repository, ctx context.Context, dagID string, tasks []domain.TaskSpec) string {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v2", Image: "img:v2", Tasks: tasks,
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash)
	if rerr != nil || !created {
		t.Fatalf("register v2: created=%v err=%v", created, rerr)
	}
	return spec.Image
}

func runImage(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID string) string {
	t.Helper()
	var image string
	if err := pg.Pool.QueryRow(ctx,
		`SELECT v.image_reference FROM dag_runs r
		   JOIN dag_versions v ON v.id = r.dag_version_id
		  WHERE r.id = $1::uuid`, runUUID).Scan(&image); err != nil {
		t.Fatalf("select run image: %v", err)
	}
	return image
}

func runState(t *testing.T, pg *storage.Postgres, ctx context.Context, runUUID string) string {
	t.Helper()
	var state string
	if err := pg.Pool.QueryRow(ctx,
		`SELECT state::text FROM dag_runs WHERE id = $1::uuid`, runUUID).Scan(&state); err != nil {
		t.Fatalf("select run state: %v", err)
	}
	return state
}

// seedClearedRun creates a DAG at v1 with a failed task, publishes v2, and
// returns everything a clear test needs. The run is pinned to v1.
func seedClearedRun(t *testing.T, dagID string) (repo *storage.Repository, pg *storage.Postgres, ctx context.Context, runID, runUUID, v2Image string) {
	t.Helper()
	repo, sched, pg, ctx := openInfra(t)
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	runID = "r1"
	// Created running so ActiveRuns can resolve its UUID, then driven to failed —
	// the state a user actually clears from.
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runID, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID = resolveRunUUID(t, sched, ctx, dagID)
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "t", domain.TaskStateFailed); err != nil {
		t.Fatalf("transition failed: %v", err)
	}
	if _, err := pg.Pool.Exec(ctx, `UPDATE dag_runs SET state='failed', ended_at=now() WHERE id=$1::uuid`, runUUID); err != nil {
		t.Fatalf("fail run: %v", err)
	}
	if got := runState(t, pg, ctx, runUUID); got != "failed" {
		t.Fatalf("precondition: the run must be terminal before the clear, got %q", got)
	}
	v2Image = registerSecondVersion(t, repo, ctx, dagID, tasks)
	if got := runImage(t, pg, ctx, runUUID); got == v2Image {
		t.Fatalf("precondition: the run must still be pinned to v1, got %q", got)
	}
	return repo, pg, ctx, runID, runUUID, v2Image
}

// TestClearRunOnLatestVersionFalsePinsTheRun: with run_on_latest_version=false
// the cleared run is re-opened but keeps the version it was created with, so the
// re-run executes the image that produced the original attempt.
//
// This is what "clear last week's task" needs and what was impossible before:
// re-opening a run and re-binding it to the current version were a single
// boolean, so every clear moved the run forward.
func TestClearRunOnLatestVersionFalsePinsTheRun(t *testing.T) {
	dagID := fmt.Sprintf("clear_pin_%d", time.Now().UnixNano())
	repo, pg, ctx, runID, runUUID, v2Image := seedClearedRun(t, dagID)

	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, runID, []string{"t"}, false,
		domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: false}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	if got := runImage(t, pg, ctx, runUUID); got == v2Image {
		t.Errorf("run moved to %q; run_on_latest_version=false must keep the pinned version", got)
	}
	// Re-opening is the other half and must still happen, or the cleared task
	// sits at 'none' in a terminal run the scheduler never looks at again.
	if got := runState(t, pg, ctx, runUUID); got != "queued" {
		t.Errorf("run state = %q, want queued — the run must be re-opened even when pinned", got)
	}
}

// TestClearRunOnLatestVersionTrueRebinds locks the documented ADR 0020 behaviour:
// a clear after a fix re-runs the newest image.
func TestClearRunOnLatestVersionTrueRebinds(t *testing.T) {
	dagID := fmt.Sprintf("clear_rebind_%d", time.Now().UnixNano())
	repo, pg, ctx, runID, runUUID, v2Image := seedClearedRun(t, dagID)

	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, runID, []string{"t"}, false,
		domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}); err != nil {
		t.Fatalf("ClearTaskInstances: %v", err)
	}
	if got := runImage(t, pg, ctx, runUUID); got != v2Image {
		t.Errorf("run image = %q, want the current version %q", got, v2Image)
	}
	if got := runState(t, pg, ctx, runUUID); got != "queued" {
		t.Errorf("run state = %q, want queued", got)
	}
}

// TestRunCarriesItsPinnedVersionLabel: the version label must survive the join
// into domain.DagRun, and must track the run's PINNED version rather than the
// DAG's current one.
//
// The DTO tests prove the API serializes the field. Only this proves the field
// is populated at all: with the label empty the SPA hides the clear dialog's
// version control, and the operator cannot ask for the current version through
// any interface.
func TestRunCarriesItsPinnedVersionLabel(t *testing.T) {
	dagID := fmt.Sprintf("run_label_%d", time.Now().UnixNano())
	repo, _, ctx, runID, _, _ := seedClearedRun(t, dagID)

	run, err := repo.GetDagRun(ctx, "default", dagID, runID)
	if err != nil {
		t.Fatalf("GetDagRun: %v", err)
	}
	if run.Version == "" {
		t.Fatal("the run carries no version label; the UI's version control stays hidden")
	}
	// seedClearedRun registers v1, creates the run, then registers v2. The run is
	// pinned to v1, so the label must be v1's — not the DAG's current v2.
	if run.Version != "v1" {
		t.Errorf("run.Version = %q, want the pinned v1 (v2 would mean the join followed the DAG, not the run)", run.Version)
	}

	dag, derr := repo.GetDag(ctx, "default", dagID)
	if derr != nil {
		t.Fatalf("GetDag: %v", derr)
	}
	if dag.CurrentVersion != "v2" {
		t.Errorf("dag.CurrentVersion = %q, want v2", dag.CurrentVersion)
	}
	// The pair is the point: the SPA offers "run with latest" only when they differ.
	if run.Version == dag.CurrentVersion {
		t.Error("run and DAG report the same version; the clear dialog would never offer the toggle")
	}

	runs, _, lerr := repo.ListDagRuns(ctx, "default", dagID, 10, 0)
	if lerr != nil {
		t.Fatalf("ListDagRuns: %v", lerr)
	}
	if len(runs) == 0 {
		t.Fatal("no runs listed; the assertion below would be vacuous")
	}
	if runs[0].Version != "v1" {
		t.Errorf("listed run.Version = %q, want v1 — the list path joins too", runs[0].Version)
	}
}
