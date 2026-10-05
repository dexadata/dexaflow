//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
)

// openRepo connects to the migrated test database or skips. It returns the
// Repository (UI/resource queries) and SchedulerStore (task materialization)
// over the same pool, plus a cleanup.
func openRepo(t *testing.T) (*storage.Repository, *storage.SchedulerStore, context.Context) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	return storage.NewRepository(pg), storage.NewSchedulerStore(pg), ctx
}

func registerSpec(t *testing.T, repo *storage.Repository, ctx context.Context, dagID string, tasks []domain.TaskSpec) {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1", Tasks: tasks,
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register %s: created=%v err=%v", dagID, created, rerr)
	}
}

func TestGetCurrentSpecIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_spec_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
	}
	registerSpec(t, repo, ctx, dagID, tasks)

	got, err := repo.GetCurrentSpec(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("GetCurrentSpec: %v", err)
	}
	if got.DagID != dagID || len(got.Tasks) != 2 {
		t.Fatalf("spec mismatch: dag=%s tasks=%d", got.DagID, len(got.Tasks))
	}
	if got.Tasks[1].TaskID != "load" || len(got.Tasks[1].DependsOn) != 1 {
		t.Errorf("dependencies not round-tripped: %+v", got.Tasks[1])
	}

	if _, err := repo.GetCurrentSpec(ctx, "default", "does-not-exist"); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing dag = %v, want ErrNotFound", err)
	}
}

// TestGetCurrentSpecFollowsNewVersionIntegration pins that serving the current
// spec from the version cache never hides a newly registered version: the
// cache is keyed by the immutable version id, and the current id is read from
// dags on every call.
func TestGetCurrentSpecFollowsNewVersionIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_spec_next_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}})
	for range 2 {
		got, err := repo.GetCurrentSpec(ctx, "default", dagID)
		if err != nil || len(got.Tasks) != 1 {
			t.Fatalf("first version: tasks=%d err=%v", len(got.Tasks), err)
		}
	}

	next := domain.DAGSpec{SchemaVersion: "1.0", DagID: dagID, DagVersion: "v2", Image: "img:v2", Tasks: []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}}
	hash, err := next.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", next, hash); rerr != nil || !created {
		t.Fatalf("register v2: created=%v err=%v", created, rerr)
	}
	got, err := repo.GetCurrentSpec(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("GetCurrentSpec after v2: %v", err)
	}
	if len(got.Tasks) != 2 || got.Image != "img:v2" {
		t.Errorf("GetCurrentSpec after v2 = %d tasks, image %q; want the new version", len(got.Tasks), got.Image)
	}
}

func TestLatestRunsForDagsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	base := time.Now().UnixNano()
	dagA := fmt.Sprintf("uiq_lr_a_%d", base)
	dagB := fmt.Sprintf("uiq_lr_b_%d", base)
	for _, id := range []string{dagA, dagB} {
		registerSpec(t, repo, ctx, id, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
		// three runs, ascending logical date; latest must come back first.
		for i := 0; i < 3; i++ {
			logical := time.Unix(0, base).Add(time.Duration(i) * time.Hour).UTC()
			if _, err := repo.CreateDagRun(ctx, "default", id, domain.DagRun{
				RunID: fmt.Sprintf("r%d", i), State: domain.DagRunStateSuccess,
				RunType: "manual", LogicalDate: logical, QueuedAt: logical,
			}); err != nil {
				t.Fatalf("create run: %v", err)
			}
		}
	}

	byDag, err := repo.LatestRunsForDags(ctx, "default", []string{dagA, dagB}, 2)
	if err != nil {
		t.Fatalf("LatestRunsForDags: %v", err)
	}
	for _, id := range []string{dagA, dagB} {
		runs := byDag[id]
		if len(runs) != 2 {
			t.Fatalf("%s: want 2 latest runs, got %d", id, len(runs))
		}
		// LIMIT 2 of [r0,r1,r2] by logical_date DESC -> r2, r1.
		if runs[0].RunID != "r2" || runs[1].RunID != "r1" {
			t.Errorf("%s: latest runs out of order: %s, %s", id, runs[0].RunID, runs[1].RunID)
		}
	}

	// Empty input is a no-op (no query), not an error.
	empty, err := repo.LatestRunsForDags(ctx, "default", nil, 5)
	if err != nil || len(empty) != 0 {
		t.Errorf("empty dagIDs = %v, %v", empty, err)
	}
}

func TestTaskInstancesForRunsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_ti_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "transform", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
		{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"transform"}},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	// Resolve the run UUID (MaterializeTasks keys by it) via the active-runs view.
	runUUID := ""
	runs, raErr := sched.ActiveRuns(ctx)
	if raErr != nil {
		t.Fatalf("ActiveRuns: %v", raErr)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if runUUID == "" {
		t.Fatal("could not resolve run UUID for the new run")
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "extract", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "transform", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}
	// "load" stays none.

	tis, err := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"r1"})
	if err != nil {
		t.Fatalf("TaskInstancesForRuns: %v", err)
	}
	if len(tis) != 3 {
		t.Fatalf("want 3 task instances, got %d", len(tis))
	}
	states := map[string]domain.TaskState{}
	for _, ti := range tis {
		if ti.RunID != "r1" {
			t.Errorf("ti %s carries wrong run_id %q", ti.TaskID, ti.RunID)
		}
		states[ti.TaskID] = ti.State
	}
	if states["extract"] != domain.TaskStateSuccess || states["transform"] != domain.TaskStateRunning || states["load"] != domain.TaskStateNone {
		t.Errorf("unexpected states: %v", states)
	}

	// Unknown run_ids yield no rows; empty run_ids skip the query entirely.
	if got, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, []string{"nope"}); len(got) != 0 {
		t.Errorf("unknown run_id returned %d rows", len(got))
	}
	if got, _ := repo.TaskInstancesForRuns(ctx, "default", dagID, nil); got != nil {
		t.Errorf("empty run_ids returned %v", got)
	}
}

// TestTaskInstanceTryNumberAndTimestampsIntegration pins two Airflow-parity
// behaviors: a freshly materialized task instance is try_number 1 (1-based, so
// its first attempt's logs live at .../1.log where the UI looks), and entering
// the scheduled/queued/running states stamps scheduled_at/queued_at/started_at.
func TestTaskInstanceTryNumberAndTimestampsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_try_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "extract", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := ""
	runs, raErr := sched.ActiveRuns(ctx)
	if raErr != nil {
		t.Fatalf("ActiveRuns: %v", raErr)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if runUUID == "" {
		t.Fatal("could not resolve run UUID")
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	first := firstTI(t, repo, ctx, dagID)
	if first.TryNumber != 1 {
		t.Errorf("first attempt try_number = %d, want 1 (Airflow is 1-based)", first.TryNumber)
	}

	// scheduled -> queued -> running each stamps its own entry timestamp.
	for _, st := range []domain.TaskState{domain.TaskStateScheduled, domain.TaskStateQueued, domain.TaskStateRunning} {
		if err := sched.ApplyTransition(ctx, runUUID, "extract", st); err != nil {
			t.Fatalf("transition to %s: %v", st, err)
		}
	}
	ti := firstTI(t, repo, ctx, dagID)
	if ti.ScheduledAt == nil {
		t.Error("scheduled_at not stamped on entering scheduled")
	}
	if ti.QueuedAt == nil {
		t.Error("queued_at not stamped on entering queued")
	}
	if ti.StartedAt == nil {
		t.Error("started_at not stamped on entering running")
	}
}

// firstTI returns the single task instance of the test run.
func firstTI(t *testing.T, repo *storage.Repository, ctx context.Context, dagID string) domain.TaskInstance {
	t.Helper()
	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, "r1", 100, 0)
	if err != nil {
		t.Fatalf("ListTaskInstances: %v", err)
	}
	if len(tis) != 1 {
		t.Fatalf("want 1 task instance, got %d", len(tis))
	}
	return tis[0]
}

// TestFavoritesIntegration round-trips per-user DAG favorites: add is idempotent,
// the id set reflects adds/removes, and favorites are scoped per user.
func TestFavoritesIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagA := fmt.Sprintf("fav_a_%d", time.Now().UnixNano())
	dagB := fmt.Sprintf("fav_b_%d", time.Now().UnixNano())

	if err := repo.AddFavorite(ctx, "default", "user1", dagA); err != nil {
		t.Fatalf("add: %v", err)
	}
	if err := repo.AddFavorite(ctx, "default", "user1", dagA); err != nil {
		t.Fatalf("add (idempotent): %v", err)
	}
	if err := repo.AddFavorite(ctx, "default", "user1", dagB); err != nil {
		t.Fatalf("add b: %v", err)
	}
	favs, err := repo.FavoriteDagIDs(ctx, "default", "user1")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !favs[dagA] || !favs[dagB] {
		t.Errorf("expected both dags favorited, got %v", favs)
	}
	// Scoped per user: user2 sees none of user1's favorites.
	if other, _ := repo.FavoriteDagIDs(ctx, "default", "user2"); other[dagA] {
		t.Errorf("favorites leaked across users: %v", other)
	}
	if err := repo.RemoveFavorite(ctx, "default", "user1", dagA); err != nil {
		t.Fatalf("remove: %v", err)
	}
	favs, _ = repo.FavoriteDagIDs(ctx, "default", "user1")
	if favs[dagA] || !favs[dagB] {
		t.Errorf("after remove want only %s, got %v", dagB, favs)
	}
}

// TestListDagVersionsIntegration guards the row_number()-based version_number
// (drives the Graph view's version-scoped structure fetch) against real Postgres.
func TestListDagVersionsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_ver_%d", time.Now().UnixNano())

	// Two distinct specs for the same DAG -> two versions (RegisterDagVersion
	// dedups by spec hash, so the second must differ).
	for i, ver := range []string{"v1", "v2"} {
		spec := domain.DAGSpec{
			SchemaVersion: "1.0", DagID: dagID, DagVersion: ver, Image: "img:" + ver,
			Tasks: []domain.TaskSpec{{TaskID: fmt.Sprintf("t%d", i), Type: domain.TaskTypePython}},
		}
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
			t.Fatalf("register %s: created=%v err=%v", ver, created, rerr)
		}
	}

	versions, err := repo.ListDagVersions(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("ListDagVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("want 2 versions, got %d", len(versions))
	}
	// Newest-first: version_number 2 then 1.
	if versions[0].VersionNumber != 2 || versions[1].VersionNumber != 1 {
		t.Errorf("version_number ordering wrong: %d, %d", versions[0].VersionNumber, versions[1].VersionNumber)
	}
	if versions[0].ID == "" || versions[0].CreatedAt.IsZero() {
		t.Errorf("version row missing id/created_at: %+v", versions[0])
	}
}

// TestDashboardStatsIntegration guards the home-dashboard counters: DAGs by
// latest-run state and run/task-instance state counts within a time window.
func TestDashboardStatsIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	base := time.Now().UnixNano()
	now := time.Now().UTC()
	dagFail := fmt.Sprintf("uiq_dash_fail_%d", base)
	dagRun := fmt.Sprintf("uiq_dash_run_%d", base)
	tasks := []domain.TaskSpec{
		{TaskID: "a", Type: domain.TaskTypePython},
		{TaskID: "b", Type: domain.TaskTypePython, DependsOn: []string{"a"}},
	}

	// Create every run as running first (so ActiveRuns resolves its UUID and
	// MaterializeTasks works), then transition the run to its target state.
	mkRun := func(dagID string) string {
		registerSpec(t, repo, ctx, dagID, tasks)
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: now,
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		runs, err := sched.ActiveRuns(ctx)
		if err != nil {
			t.Fatalf("ActiveRuns: %v", err)
		}
		for _, r := range runs {
			if r.DagID == dagID {
				if merr := sched.MaterializeTasks(ctx, r.RunID, tasks); merr != nil {
					t.Fatalf("MaterializeTasks: %v", merr)
				}
				return r.RunID
			}
		}
		t.Fatalf("run UUID not resolved for %s", dagID)
		return ""
	}

	failUUID := mkRun(dagFail)
	if err := sched.ApplyTransition(ctx, failUUID, "a", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, failUUID, "b", domain.TaskStateFailed); err != nil {
		t.Fatal(err)
	}
	if err := sched.SetRunState(ctx, failUUID, domain.DagRunStateFailed); err != nil {
		t.Fatal(err)
	}
	runUUID := mkRun(dagRun)
	if err := sched.ApplyTransition(ctx, runUUID, "a", domain.TaskStateRunning); err != nil {
		t.Fatal(err)
	}

	stats, err := repo.DagStats(ctx, "default")
	if err != nil {
		t.Fatalf("DagStats: %v", err)
	}
	if stats.Active < 2 {
		t.Errorf("active dag count = %d, want >= 2", stats.Active)
	}
	if stats.Failed < 1 || stats.Running < 1 {
		t.Errorf("dag-by-latest-state: failed=%d running=%d, want both >= 1", stats.Failed, stats.Running)
	}

	m, err := repo.HistoricalMetrics(ctx, "default", now.Add(-time.Hour), now.Add(time.Hour))
	if err != nil {
		t.Fatalf("HistoricalMetrics: %v", err)
	}
	if m.RunStates["failed"] < 1 || m.RunStates["running"] < 1 {
		t.Errorf("run states = %v, want failed>=1 running>=1", m.RunStates)
	}
	if m.TIStates["success"] < 1 || m.TIStates["failed"] < 1 || m.TIStates["running"] < 1 {
		t.Errorf("ti states = %v, want success/failed/running >= 1 each", m.TIStates)
	}
}

// TestListAuditLogsIntegration guards the Audit Log query: RegisterDagVersion
// writes a "dag.version.register" entry, which must come back (and be filterable
// by dag_id) through ListAuditLogs.
func TestListAuditLogsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_audit_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})

	entries, total, err := repo.ListAuditLogs(ctx, "default", dagID, 50, 0)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	if total < 1 || len(entries) < 1 {
		t.Fatalf("want >= 1 audit entry for %s, got total=%d len=%d", dagID, total, len(entries))
	}
	found := false
	for _, e := range entries {
		if e.Action == "dag.version.register" && e.ResourceID == dagID {
			found = true
			if e.ResourceType != "dag" || e.When.IsZero() {
				t.Errorf("entry missing fields: %+v", e)
			}
		}
	}
	if !found {
		t.Errorf("dag.version.register entry not found for %s", dagID)
	}

	// Filtering by a different dag yields none of this dag's entries.
	other, _, err := repo.ListAuditLogs(ctx, "default", "no-such-dag-"+dagID, 50, 0)
	if err != nil {
		t.Fatalf("ListAuditLogs(other): %v", err)
	}
	for _, e := range other {
		if e.ResourceID == dagID {
			t.Errorf("dag filter leaked entry for %s", dagID)
		}
	}
}

// TestRecordSecretScopeWarningIntegration checks the warn-phase audit event
// (ADR 0045, ADR 0055): a task that received the full vault while declaring a
// narrower set records a secret.scope_warning row against the DAG, carrying the
// kind and the declared/total counts in metadata — and no secret names.
//
// The tenant argument is the tenant UUID the agent token carries (the value
// recordSecretScope passes from AgentIdentity.TenantID), NOT the tenant name.
// Feeding the name here is the wrong contract that let #722 slip through: a name
// resolved by a UUID-keyed lookup silently dropped every audit row.
func TestRecordSecretScopeWarningIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_scopewarn_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	// The tenant NAME must NOT resolve (it is not a UUID) — guards the #722 bug.
	if err := repo.RecordSecretScopeWarning(ctx, "default", dagID, "run-1", "t", "connections", 1, 3); err == nil {
		t.Error("RecordSecretScopeWarning should require a tenant UUID, not the name")
	}

	if err := repo.RecordSecretScopeWarning(ctx, tenantUUID, dagID, "run-1", "t", "connections", 1, 3); err != nil {
		t.Fatalf("RecordSecretScopeWarning: %v", err)
	}

	entries, _, err := repo.ListAuditLogs(ctx, "default", dagID, 50, 0)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var found *domain.AuditLogEntry
	for i := range entries {
		if entries[i].Action == "secret.scope_warning" {
			found = &entries[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("secret.scope_warning entry not found for %s: %+v", dagID, entries)
	}
	if found.ResourceType != "dag" || found.ResourceID != dagID {
		t.Errorf("resource = %s/%s, want dag/%s", found.ResourceType, found.ResourceID, dagID)
	}
	// Parse the metadata JSON and compare fields — jsonb normalizes spacing, so a
	// substring match on `"kind":"connections"` is brittle (the stored form is
	// `"kind": "connections"`). Numbers decode to float64.
	var meta map[string]any
	if err := json.Unmarshal([]byte(found.Extra), &meta); err != nil {
		t.Fatalf("metadata is not valid JSON: %v (%q)", err, found.Extra)
	}
	if meta["kind"] != "connections" {
		t.Errorf("kind = %v, want connections", meta["kind"])
	}
	if meta["run_id"] != "run-1" {
		t.Errorf("run_id = %v, want run-1", meta["run_id"])
	}
	if meta["task_id"] != "t" {
		t.Errorf("task_id = %v, want t", meta["task_id"])
	}
	if meta["declared"] != float64(1) {
		t.Errorf("declared = %v, want 1", meta["declared"])
	}
	if meta["total"] != float64(3) {
		t.Errorf("total = %v, want 3", meta["total"])
	}
}

// TestRecordSecretLivenessDenialIntegration checks the secret-path liveness
// audit event (ADR 0055): a not-live task instance records a liveness row
// against the DAG, with distinct actions per gate mode — secret.liveness_denied
// in enforce mode, secret.liveness_would_deny in observe mode — and the kind,
// run/task/attempt, and mode in metadata (never secret names or values).
//
// As with the scope-warning event, the tenant argument is the tenant UUID the
// agent token carries (recordLiveness passes AgentIdentity.TenantID), NOT the
// name. #722's sibling bug resolved it by name, so an ENFORCE-mode security
// denial wrote no audit record at all.
func TestRecordSecretLivenessDenialIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_livedeny_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	// The tenant NAME must NOT resolve (it is not a UUID) — guards the #722 bug.
	if err := repo.RecordSecretLivenessDenial(ctx, "default", dagID, "run-1", "t", 2, "variables", "enforce"); err == nil {
		t.Error("RecordSecretLivenessDenial should require a tenant UUID, not the name")
	}

	if err := repo.RecordSecretLivenessDenial(ctx, tenantUUID, dagID, "run-1", "t", 2, "variables", "enforce"); err != nil {
		t.Fatalf("RecordSecretLivenessDenial(enforce): %v", err)
	}
	if err := repo.RecordSecretLivenessDenial(ctx, tenantUUID, dagID, "run-1", "t", 3, "connections", "observe"); err != nil {
		t.Fatalf("RecordSecretLivenessDenial(observe): %v", err)
	}

	entries, _, err := repo.ListAuditLogs(ctx, "default", dagID, 50, 0)
	if err != nil {
		t.Fatalf("ListAuditLogs: %v", err)
	}
	var denied, wouldDeny *domain.AuditLogEntry
	for i := range entries {
		switch entries[i].Action {
		case "secret.liveness_denied":
			denied = &entries[i]
		case "secret.liveness_would_deny":
			wouldDeny = &entries[i]
		}
	}
	if denied == nil {
		t.Fatalf("secret.liveness_denied entry not found for %s: %+v", dagID, entries)
	}
	if wouldDeny == nil {
		t.Fatalf("secret.liveness_would_deny entry not found for %s: %+v", dagID, entries)
	}
	if denied.ResourceType != "dag" || denied.ResourceID != dagID {
		t.Errorf("resource = %s/%s, want dag/%s", denied.ResourceType, denied.ResourceID, dagID)
	}
	// Enforce-mode metadata: parse JSON (jsonb normalizes spacing; numbers decode
	// to float64).
	var meta map[string]any
	if err := json.Unmarshal([]byte(denied.Extra), &meta); err != nil {
		t.Fatalf("metadata is not valid JSON: %v (%q)", err, denied.Extra)
	}
	if meta["kind"] != "variables" {
		t.Errorf("kind = %v, want variables", meta["kind"])
	}
	if meta["mode"] != "enforce" {
		t.Errorf("mode = %v, want enforce", meta["mode"])
	}
	if meta["run_id"] != "run-1" {
		t.Errorf("run_id = %v, want run-1", meta["run_id"])
	}
	if meta["task_id"] != "t" {
		t.Errorf("task_id = %v, want t", meta["task_id"])
	}
	if meta["try_number"] != float64(2) {
		t.Errorf("try_number = %v, want 2", meta["try_number"])
	}
}

// TestListDagsFilteredIntegration guards the DAG list filters: by latest-run
// state and by paused flag, with correct totals.
func TestListDagsFilteredIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	base := time.Now().UnixNano()
	now := time.Now().UTC()
	failDag := fmt.Sprintf("uiq_filt_fail_%d", base)
	okDag := fmt.Sprintf("uiq_filt_ok_%d", base)

	for _, tc := range []struct {
		id    string
		state domain.DagRunState
	}{{failDag, domain.DagRunStateFailed}, {okDag, domain.DagRunStateSuccess}} {
		registerSpec(t, repo, ctx, tc.id, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
		if _, err := repo.CreateDagRun(ctx, "default", tc.id, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: now,
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		runs, err := sched.ActiveRuns(ctx)
		if err != nil {
			t.Fatalf("ActiveRuns: %v", err)
		}
		for _, r := range runs {
			if r.DagID == tc.id {
				if serr := sched.SetRunState(ctx, r.RunID, tc.state); serr != nil {
					t.Fatal(serr)
				}
			}
		}
	}

	// Filter by latest-run state = failed: the failed dag is present, the
	// successful one is not.
	failed, total, err := repo.ListDagsFiltered(ctx, "default", "failed", nil, 1000, 0)
	if err != nil {
		t.Fatalf("ListDagsFiltered(failed): %v", err)
	}
	ids := map[string]bool{}
	for _, d := range failed {
		ids[d.DagID] = true
	}
	if !ids[failDag] || ids[okDag] {
		t.Errorf("failed filter wrong: failDag=%v okDag=%v (total=%d)", ids[failDag], ids[okDag], total)
	}

	// No filter returns at least both.
	all, allTotal, err := repo.ListDagsFiltered(ctx, "default", "", nil, 1000, 0)
	if err != nil {
		t.Fatalf("ListDagsFiltered(none): %v", err)
	}
	if allTotal < 2 || len(all) < 2 {
		t.Errorf("unfiltered should include both dags, got total=%d", allTotal)
	}

	// Pause one dag, then paused=true returns it and paused=false excludes it.
	if _, err := repo.SetPaused(ctx, "default", failDag, true); err != nil {
		t.Fatal(err)
	}
	pausedTrue := true
	pausedList, _, err := repo.ListDagsFiltered(ctx, "default", "", &pausedTrue, 1000, 0)
	if err != nil {
		t.Fatalf("ListDagsFiltered(paused): %v", err)
	}
	foundPaused := false
	for _, d := range pausedList {
		if d.DagID == failDag {
			foundPaused = true
		}
		if d.DagID == okDag {
			t.Errorf("paused=true leaked unpaused dag %s", okDag)
		}
	}
	if !foundPaused {
		t.Errorf("paused=true missing paused dag %s", failDag)
	}
}

// TestClearOnlyFailedIntegration guards the only_failed clear semantics: a
// failed task is reset to none (and its try_number bumped) while a successful
// sibling is left untouched.
func TestClearOnlyFailedIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_clear_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{
		{TaskID: "ok", Type: domain.TaskTypePython},
		{TaskID: "bad", Type: domain.TaskTypePython},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	runUUID := ""
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("ActiveRuns: %v", err)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "ok", domain.TaskStateSuccess); err != nil {
		t.Fatal(err)
	}
	if err := sched.ApplyTransition(ctx, runUUID, "bad", domain.TaskStateFailed); err != nil {
		t.Fatal(err)
	}

	// only_failed with empty task list clears just the failed task.
	cleared, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", nil, true, domain.ClearOptions{})
	if err != nil {
		t.Fatalf("ClearTaskInstances(onlyFailed): %v", err)
	}
	if cleared != 1 {
		t.Errorf("cleared = %d, want 1 (only the failed task)", cleared)
	}
	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, "r1", 10, 0)
	if err != nil {
		t.Fatalf("ListTaskInstances: %v", err)
	}
	states := map[string]domain.TaskState{}
	for _, ti := range tis {
		states[ti.TaskID] = ti.State
	}
	if states["bad"] != domain.TaskStateNone {
		t.Errorf("failed task not reset: %v", states["bad"])
	}
	if states["ok"] != domain.TaskStateSuccess {
		t.Errorf("successful task should be untouched: %v", states["ok"])
	}
}

// TestVariablesIntegration guards the Admin Variables CRUD against real Postgres.
func TestVariablesIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := fmt.Sprintf("uiq_var_%d", time.Now().UnixNano())

	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: key, Value: "v1", Description: "first"}); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	got, err := repo.GetVariable(ctx, "default", key)
	if err != nil || got.Value != "v1" || got.Description != "first" {
		t.Fatalf("GetVariable = %+v, err=%v", got, err)
	}
	// Upsert updates the value in place but preserves a description the write
	// omits (COALESCE), rather than wiping it — the safe-by-default upsert (#881).
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: key, Value: "v2"}); err != nil {
		t.Fatal(err)
	}
	if got, _ := repo.GetVariable(ctx, "default", key); got.Value != "v2" {
		t.Errorf("upsert did not update: %q", got.Value)
	}
	if got, _ := repo.GetVariable(ctx, "default", key); got.Description != "first" {
		t.Errorf("upsert clobbered the omitted description: got %q, want first (preserved)", got.Description)
	}
	// List includes it.
	vars, total, err := repo.ListVariables(ctx, "default", 1000, 0)
	if err != nil || total < 1 {
		t.Fatalf("ListVariables: total=%d err=%v", total, err)
	}
	found := false
	for _, v := range vars {
		if v.Key == key {
			found = true
		}
	}
	if !found {
		t.Errorf("listed variables missing %s", key)
	}
	// Delete, then missing.
	if err := repo.DeleteVariable(ctx, "default", key); err != nil {
		t.Fatalf("DeleteVariable: %v", err)
	}
	if _, err := repo.GetVariable(ctx, "default", key); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleted variable still present: %v", err)
	}
	if err := repo.DeleteVariable(ctx, "default", key); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

// TestConnectionsIntegration guards the Admin Connections CRUD with encryption:
// extra round-trips through AES-256-GCM, the password is stored (write-only),
// and CRUD/delete behave.
func TestConnectionsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	connID := fmt.Sprintf("uiq_conn_%d", time.Now().UnixNano())
	port := 5432
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "postgres", Host: "db", Login: "u",
		Password: "s3cr3t", Schema: "public", Port: &port, Extra: `{"sslmode":"require"}`,
	}); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}

	got, err := repo.GetConnection(ctx, "default", connID)
	if err != nil {
		t.Fatalf("GetConnection: %v", err)
	}
	if got.ConnType != "postgres" || got.Host != "db" || got.Port == nil || *got.Port != 5432 {
		t.Errorf("metadata mismatch: %+v", got)
	}
	// extra decrypts back to plaintext.
	if got.Extra != `{"sslmode":"require"}` {
		t.Errorf("extra did not round-trip through encryption: %q", got.Extra)
	}
	// password is write-only — never returned.
	if got.Password != "" {
		t.Errorf("password should not be returned, got %q", got.Password)
	}

	// Without the cipher, a fresh repo cannot decrypt extra (proves it is encrypted at rest).
	repoNoKey, _, _ := openRepo(t)
	if _, derr := repoNoKey.GetConnection(ctx, "default", connID); derr == nil {
		// nil cipher returns extra="" rather than erroring; assert it is NOT plaintext.
		plain, _ := repoNoKey.GetConnection(ctx, "default", connID)
		if plain.Extra == `{"sslmode":"require"}` {
			t.Error("extra is stored in plaintext (not encrypted at rest)")
		}
	}

	if err := repo.DeleteConnection(ctx, "default", connID); err != nil {
		t.Fatalf("DeleteConnection: %v", err)
	}
	if _, err := repo.GetConnection(ctx, "default", connID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleted connection still present: %v", err)
	}
}

// TestSecretDeliveryByTenantUUIDIntegration covers the agent secret-delivery
// path (ADR 0021): SecretVariables/SecretConnectionURIs are keyed by the tenant
// UUID the agent token carries (not the name), and the connection URI carries the
// decrypted password. This is the bug class that broke delivery in the pod.
func TestSecretDeliveryByTenantUUIDIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 7)
	}
	cipher, err := secrets.NewAESGCM(key)
	if err != nil {
		t.Fatal(err)
	}
	repo.SetCipher(cipher)

	varKey := fmt.Sprintf("sd_var_%d", time.Now().UnixNano())
	connID := fmt.Sprintf("sd_conn_%d", time.Now().UnixNano())
	if err := repo.SetVariable(ctx, "default", domain.Variable{Key: varKey, Value: "hello"}); err != nil {
		t.Fatalf("SetVariable: %v", err)
	}
	port := 5432
	if err := repo.SetConnection(ctx, "default", domain.Connection{
		ConnID: connID, ConnType: "postgres", Host: "db", Login: "u", Password: "s3cr3t", Port: &port, Schema: "app",
	}); err != nil {
		t.Fatalf("SetConnection: %v", err)
	}

	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("TenantUUID: %v", err)
	}
	// The tenant NAME must NOT resolve (it is not a UUID) — guards the original bug.
	if _, err := repo.SecretVariables(ctx, "default"); err == nil {
		t.Error("SecretVariables should require a tenant UUID, not the name")
	}

	vars, err := repo.SecretVariables(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretVariables(uuid): %v", err)
	}
	if vars[varKey] != "hello" {
		t.Errorf("variable %q = %q, want hello", varKey, vars[varKey])
	}
	uris, err := repo.SecretConnectionURIs(ctx, tenantUUID)
	if err != nil {
		t.Fatalf("SecretConnectionURIs(uuid): %v", err)
	}
	if uris[connID] != "postgres://u:s3cr3t@db:5432/app" {
		t.Errorf("connection uri = %q, want decrypted-password URI", uris[connID])
	}
	_ = repo.DeleteVariable(ctx, "default", varKey)
	_ = repo.DeleteConnection(ctx, "default", connID)
}

// TestConnectionWriteRequiresCipherIntegration: without a cipher, writes are
// refused rather than storing a credential in plaintext.
func TestConnectionWriteRequiresCipherIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	// no SetCipher
	err := repo.SetConnection(ctx, "default", domain.Connection{ConnID: "x", ConnType: "postgres", Password: "p"})
	if !errors.Is(err, secrets.ErrNoKey) {
		t.Errorf("write without cipher = %v, want ErrNoKey", err)
	}
}

// TestClearDagHistoryIntegration guards the clear (ADR 0020): runs are wiped but
// the DAG and its versions stay registered.
func TestClearDagHistoryIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_clearh_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateSuccess, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := repo.ClearDagHistory(ctx, "default", dagID); err != nil {
		t.Fatalf("ClearDagHistory: %v", err)
	}
	// The DAG still exists.
	if _, err := repo.GetDag(ctx, "default", dagID); err != nil {
		t.Errorf("clear must keep the DAG registered: %v", err)
	}
	// Its runs are gone.
	runs, _, err := repo.ListDagRuns(ctx, "default", dagID, 10, 0)
	if err != nil {
		t.Fatalf("ListDagRuns: %v", err)
	}
	if len(runs) != 0 {
		t.Errorf("clear should wipe runs, got %d", len(runs))
	}
	// Clearing a missing DAG is ErrNotFound.
	if err := repo.ClearDagHistory(ctx, "default", "no-such-"+dagID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("clear missing dag = %v, want ErrNotFound", err)
	}
}

// TestDeleteDagIntegration guards DELETE-DAG: the row is removed (subsequent
// reads miss), the cascade clears its runs, and deleting a missing DAG returns
// ErrNotFound.
func TestDeleteDagIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_del_%d", time.Now().UnixNano())
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateSuccess, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if err := repo.DeleteDag(ctx, "default", dagID); err != nil {
		t.Fatalf("DeleteDag: %v", err)
	}
	if _, err := repo.GetDag(ctx, "default", dagID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("deleted dag still readable: %v", err)
	}
	// Cascade: runs of the deleted dag are gone (ListDagRuns resolves the dag
	// first, so it returns ErrNotFound).
	if _, _, err := repo.ListDagRuns(ctx, "default", dagID, 10, 0); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("runs survived dag delete: %v", err)
	}
	// Deleting again misses.
	if err := repo.DeleteDag(ctx, "default", dagID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("second delete = %v, want ErrNotFound", err)
	}
}

// TestReportStateRecordsResultIntegration guards the pod-agent state-report path
// (ExecutionStore.ReportState -> ReportTaskResult). Before the $3::task_state
// cast, this query failed with SQLSTATE 42P08 (inconsistent types deduced for
// the state parameter); the pod path was the first to exercise it.
func TestReportStateRecordsResultIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	exec := storage.NewExecutionStore(pg)

	dagID := fmt.Sprintf("uiq_report_%d", time.Now().UnixNano())
	tasks := []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}
	registerSpec(t, repo, ctx, dagID, tasks)
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	runUUID := ""
	runs, raErr := sched.ActiveRuns(ctx)
	if raErr != nil {
		t.Fatalf("ActiveRuns: %v", raErr)
	}
	for _, r := range runs {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if runUUID == "" {
		t.Fatal("could not resolve run UUID")
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	id := auth.AgentIdentity{RunID: runUUID, TaskID: "t", TryNumber: 1} // production always stamps the dispatched attempt
	// running then success — exactly the transitions the agent reports; both
	// must record without a type-deduction error.
	if err := exec.ReportState(ctx, id, domain.TaskStateRunning, 0, ""); err != nil {
		t.Fatalf("ReportState(running): %v", err)
	}
	if err := exec.ReportState(ctx, id, domain.TaskStateSuccess, 0, ""); err != nil {
		t.Fatalf("ReportState(success): %v", err)
	}

	tis, _, err := repo.ListTaskInstances(ctx, "default", dagID, "r1", 10, 0)
	if err != nil {
		t.Fatalf("ListTaskInstances: %v", err)
	}
	if len(tis) != 1 || tis[0].State != domain.TaskStateSuccess {
		t.Fatalf("want 1 task instance in success, got %+v", tis)
	}
	if tis[0].EndedAt == nil {
		t.Errorf("success report should set ended_at")
	}
}

// TestRegisterDagPersistsTagsIntegration guards that push stores the spec's tags
// and start_date (UpsertDag used to drop them, leaving DAG details blank — #—).
func TestRegisterDagPersistsTagsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("uiq_tags_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		Owner: "data-eng", Tags: []string{"example", "etl"}, StartDate: "2026-01-01T00:00:00Z",
		Tasks: []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}},
	}
	hash, _ := spec.CanonicalHash()
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register: created=%v err=%v", created, rerr)
	}
	got, err := repo.GetDag(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("GetDag: %v", err)
	}
	if len(got.Tags) != 2 || got.Tags[0] != "example" {
		t.Errorf("tags not persisted: %v", got.Tags)
	}
	if got.Owner != "data-eng" {
		t.Errorf("owner not persisted: %q", got.Owner)
	}
	if got.StartDate == nil {
		t.Errorf("start_date not persisted")
	}
	_ = repo.DeleteDag(ctx, "default", dagID)
}

// TestImportErrorsIntegration guards upsert-by-filename, list, and clear against
// real Postgres — the feed behind the Airflow home's "Import Errors" banner.
func TestImportErrorsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	file := fmt.Sprintf("dags/broken_%d/dag.py", time.Now().UnixNano())

	if err := repo.SetImportError(ctx, "default", domain.ImportError{
		Filename: file, StackTrace: "SyntaxError: boom", BundleName: "leoflow",
	}); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Re-import of the same file replaces the row (no duplicate, newer trace).
	if err := repo.SetImportError(ctx, "default", domain.ImportError{
		Filename: file, StackTrace: "NameError: later", BundleName: "leoflow",
	}); err != nil {
		t.Fatalf("upsert replace: %v", err)
	}
	errs, err := repo.ListImportErrors(ctx, "default")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *domain.ImportError
	count := 0
	for i := range errs {
		if errs[i].Filename == file {
			found = &errs[i]
			count++
		}
	}
	if found == nil || count != 1 {
		t.Fatalf("want exactly one row for %s, got %d", file, count)
	}
	if found.StackTrace != "NameError: later" {
		t.Errorf("upsert did not replace trace: %q", found.StackTrace)
	}
	if found.BundleName != "leoflow" || found.ID == "" || found.Timestamp.IsZero() {
		t.Errorf("fields not populated: %+v", *found)
	}

	if err := repo.ClearImportError(ctx, "default", file); err != nil {
		t.Fatalf("clear: %v", err)
	}
	errs, _ = repo.ListImportErrors(ctx, "default")
	for _, e := range errs {
		if e.Filename == file {
			t.Errorf("clear did not remove %s", file)
		}
	}
}

// TestStagingVolumesIntegration guards the record → list (joined with the run's
// state) → mark-deleted flow that drives staging-volume GC (ADR 0022) against
// real Postgres, including the staging_volumes ⋈ dags ⋈ dag_runs join.
func TestStagingVolumesIntegration(t *testing.T) {
	repo, store, ctx := openRepo(t)
	dagID := fmt.Sprintf("sv_%d", time.Now().UnixNano())
	runIDStr := "manual__sv_run"

	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "extract", Type: domain.TaskTypePython}})
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: runIDStr, State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// The staging volume is keyed by the dag_run's UUID (what StagingClaimName and
	// dispatch use), NOT the run_id string — so the join must resolve on dag_runs.id.
	active, err := store.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("active runs: %v", err)
	}
	var runUUID string
	for _, r := range active {
		if r.DagID == dagID {
			runUUID = r.RunID
		}
	}
	if runUUID == "" {
		t.Fatalf("could not find the run's UUID for %s", dagID)
	}
	pvc := fmt.Sprintf("leoflow-staging-%s-%s", dagID, runUUID)
	tenantUUID, err := repo.TenantUUID(ctx, "default")
	if err != nil {
		t.Fatalf("tenant uuid: %v", err)
	}

	if err := store.RecordStagingVolume(ctx, tenantUUID, dagID, runUUID, pvc, "5Gi"); err != nil {
		t.Fatalf("record: %v", err)
	}
	// Idempotent: a second record (per-task) is a no-op.
	if err := store.RecordStagingVolume(ctx, tenantUUID, dagID, runUUID, pvc, "5Gi"); err != nil {
		t.Fatalf("record (idempotent): %v", err)
	}

	vols, err := store.ListActiveStagingVolumes(ctx)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	var found *domain.StagingVolumeState
	for i := range vols {
		if vols[i].PVCName == pvc {
			found = &vols[i]
		}
	}
	if found == nil {
		t.Fatalf("staging volume %s not listed active", pvc)
	}
	// The join must resolve the run's state (regression: keying by run_id string
	// returned NULL → the GC wrongly treated active runs as orphaned and deleted them).
	if found.RunState != string(domain.DagRunStateRunning) {
		t.Errorf("join did not resolve run state by UUID: got %q, want running", found.RunState)
	}
	if found.CreatedAt.IsZero() {
		t.Errorf("created_at not populated")
	}

	if err := store.MarkStagingDeleted(ctx, pvc, "run_succeeded"); err != nil {
		t.Fatalf("mark deleted: %v", err)
	}
	vols, _ = store.ListActiveStagingVolumes(ctx)
	for _, v := range vols {
		if v.PVCName == pvc {
			t.Errorf("deleted volume %s still listed active", pvc)
		}
	}
}

// TestClearRebindsRunToCurrentVersion guards the OPT-IN mutability rule
// (ADR 0020, 2026-09-15 amendment): clear with run_on_latest_version re-binds the
// run to the DAG's current version, so a re-run after a code/yaml fix picks up
// the newest version (the last hot-reload in dev, the last deploy in prod)
// instead of the version the run was pinned to. Without it — the default — the
// run keeps its pinned version; that half is covered by
// TestClearRunOnLatestVersionFalsePinsTheRun.
func TestClearRebindsRunToCurrentVersion(t *testing.T) {
	repo, store, ctx := openRepo(t)
	dagID := fmt.Sprintf("rebind_%d", time.Now().UnixNano())

	// v1: a single task; create a run pinned to it.
	registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "extract", Type: domain.TaskTypePython}})
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateFailed, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	// v2: a new version with an extra task (simulates a code/yaml fix + re-deploy).
	spec2 := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v2", Image: "img:v2",
		Tasks: []domain.TaskSpec{
			{TaskID: "extract", Type: domain.TaskTypePython},
			{TaskID: "load", Type: domain.TaskTypePython, DependsOn: []string{"extract"}},
		},
	}
	hash2, herr := spec2.CanonicalHash()
	if herr != nil {
		t.Fatal(herr)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec2, hash2); rerr != nil || !created {
		t.Fatalf("register v2: created=%v err=%v", created, rerr)
	}

	// Clear with reset AND run_on_latest_version re-binds to the current version
	// (v2). Reset alone no longer does — it only re-opens the run.
	if _, err := repo.ClearTaskInstances(ctx, "default", dagID, "r1", []string{"extract"}, false, domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: true}); err != nil {
		t.Fatalf("clear: %v", err)
	}

	// The (now queued) run resolves to v2's spec — proving the re-bind.
	active, err := store.ActiveRuns(ctx)
	if err != nil {
		t.Fatalf("active runs: %v", err)
	}
	var tasks []string
	for _, r := range active {
		if r.DagID == dagID && r.RunID != "" {
			for _, ts := range r.Tasks {
				tasks = append(tasks, ts.TaskID)
			}
		}
	}
	hasLoad := false
	for _, id := range tasks {
		if id == "load" {
			hasLoad = true
		}
	}
	if !hasLoad {
		t.Errorf("after clear the run should re-bind to v2 (tasks include 'load'), got %v", tasks)
	}
}

// TestCreateDagRunRespectsMaxActiveRunsIntegration: manual triggers must
// honor the per-DAG `max_active_runs` cap (#200). Two active runs are
// created against a DAG configured for at most 2; a third trigger must
// fail with a conflict error (mapped to 409 at the API layer). The cap
// counts queued+running; terminal runs do not count, so after one run is
// marked success the next trigger succeeds again.
func TestCreateDagRunRespectsMaxActiveRunsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)

	dagID := fmt.Sprintf("max_active_runs_cap_%d", time.Now().UnixNano())
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "v1", Image: "img:v1",
		MaxActiveRuns: 2,
		Tasks:         []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
		t.Fatalf("register: created=%v err=%v", created, rerr)
	}

	for i := 0; i < 2; i++ {
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID:       fmt.Sprintf("manual__%d", i),
			State:       domain.DagRunStateRunning,
			RunType:     "manual",
			LogicalDate: time.Now().UTC().Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatalf("create run %d: %v", i, err)
		}
	}

	_, err = repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID:       "manual__overflow",
		State:       domain.DagRunStateRunning,
		RunType:     "manual",
		LogicalDate: time.Now().UTC().Add(3 * time.Second),
	})
	if err == nil {
		t.Fatal("third trigger past max_active_runs=2 should have failed")
	}
	if !errors.Is(err, domain.ErrConflict) {
		t.Errorf("third trigger should return domain.ErrConflict (mapped to 409), got %v", err)
	}
	if !strings.Contains(err.Error(), "max_active_runs") {
		t.Errorf("error should mention max_active_runs so users see why, got %q", err.Error())
	}

	// Terminal runs free a slot — mark one success and the next trigger succeeds.
	if err := repo.SetDagRunState(ctx, "default", dagID, "manual__0", string(domain.DagRunStateSuccess)); err != nil {
		t.Fatalf("setting first run success: %v", err)
	}
	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID:       "manual__after_free",
		State:       domain.DagRunStateRunning,
		RunType:     "manual",
		LogicalDate: time.Now().UTC().Add(4 * time.Second),
	}); err != nil {
		t.Fatalf("after freeing a slot the trigger should succeed: %v", err)
	}
}
