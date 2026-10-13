//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// admitAll is the admission check of a rollback that refuses nothing.
func admitAll(domain.DAGSpec) error { return nil }

// seedTwoVersions registers v1 then v2 of a fresh DAG and returns both specs.
func seedTwoVersions(t *testing.T, repo *storage.Repository, prefix string) (dagID string, v1, v2 domain.DAGSpec) {
	t.Helper()
	ctx := t.Context()
	dagID = fmt.Sprintf("%s_%d", prefix, time.Now().UnixNano())
	v1 = registerTxSpec(dagID, "v1", "a", 3, false)
	v2 = registerTxSpec(dagID, "v2", "bb", 7, true)
	registerTx(t, ctx, repo, v1)
	registerTx(t, ctx, repo, v2)
	return dagID, v1, v2
}

// versionByLabel returns the listed version whose label is label.
func versionByLabel(t *testing.T, repo *storage.Repository, tenant, dagID, label string) domain.DagVersion {
	t.Helper()
	versions, err := repo.ListDagVersions(t.Context(), tenant, dagID)
	if err != nil {
		t.Fatalf("list versions: %v", err)
	}
	for _, v := range versions {
		if v.Version == label {
			return v
		}
	}
	t.Fatalf("version %s of %s not listed in %+v", label, dagID, versions)
	return domain.DagVersion{}
}

// TestSetCurrentDagVersionMakesAnOlderVersionLive: rolling back by label makes
// the older version current and applies its schedule and metadata to the dags
// row, in one step (#1702).
func TestSetCurrentDagVersionMakesAnOlderVersionLive(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, v1, v2 := seedTwoVersions(t, repo, "setcur_label")

	change, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll)
	if err != nil {
		t.Fatalf("set current: %v", err)
	}
	if !change.Changed || change.From.Version != v2.DagVersion || change.To.Version != v1.DagVersion {
		t.Fatalf("change = %+v, want v2 -> v1 changed", change)
	}
	if change.From.VersionNumber != 2 || change.To.VersionNumber != 1 {
		t.Errorf("version numbers from %d to %d, want 2 -> 1", change.From.VersionNumber, change.To.VersionNumber)
	}
	if got, want := readDagMetadata(t, ctx, repo, dagID), specMetadata(t, v1); !reflect.DeepEqual(got, want) {
		t.Fatalf("dags row after rollback:\ngot  %+v\nwant %+v", got, want)
	}
	spec, err := repo.GetCurrentSpec(ctx, "default", dagID)
	if err != nil || spec.DagVersion != "v1" {
		t.Fatalf("current spec = %s, %v; want v1", spec.DagVersion, err)
	}
	if !versionByLabel(t, repo, "default", dagID, "v1").IsCurrent || versionByLabel(t, repo, "default", dagID, "v2").IsCurrent {
		t.Error("the versions listing does not mark v1, and only v1, as current")
	}
}

// TestSetCurrentDagVersionByNumber: a version_number selects the same version
// the Airflow UI numbers, and rolling forward again restores the newer spec.
func TestSetCurrentDagVersionByNumber(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, v1, v2 := seedTwoVersions(t, repo, "setcur_number")

	if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Number: 1}, "", admitAll); err != nil {
		t.Fatalf("set current to 1: %v", err)
	}
	if got := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(got, specMetadata(t, v1)) {
		t.Fatalf("after rollback to 1: %+v", got)
	}
	if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Number: 2}, "", admitAll); err != nil {
		t.Fatalf("set current to 2: %v", err)
	}
	if got := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(got, specMetadata(t, v2)) {
		t.Fatalf("after roll forward to 2: %+v", got)
	}
}

// TestSetCurrentDagVersionKeepsPauseState: a rollback is a deploy decision, so
// it never pauses or unpauses the DAG, whichever state the operator left.
func TestSetCurrentDagVersionKeepsPauseState(t *testing.T) {
	repo, _, ctx := openRepo(t)
	for _, paused := range []bool{true, false} {
		dagID, _, _ := seedTwoVersions(t, repo, fmt.Sprintf("setcur_paused_%v", paused))
		if _, err := repo.SetPaused(ctx, "default", dagID, paused); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll); err != nil {
			t.Fatalf("set current: %v", err)
		}
		d, err := repo.GetDag(ctx, "default", dagID)
		if err != nil {
			t.Fatal(err)
		}
		if d.IsPaused != paused {
			t.Errorf("is_paused = %v after a rollback, want %v (unchanged)", d.IsPaused, paused)
		}
	}
}

// TestSetCurrentDagVersionKeepsInFlightRunsPinned: a run created before the
// rollback keeps the version it started with; a run created after it gets the
// version made current.
func TestSetCurrentDagVersionKeepsInFlightRunsPinned(t *testing.T) {
	repo, _, pg, ctx := openInfra(t)
	dagID, v1, v2 := seedTwoVersions(t, repo, "setcur_inflight")
	running, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "before", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if _, serr := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll); serr != nil {
		t.Fatalf("set current: %v", serr)
	}
	after, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: "after", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC().Add(time.Minute),
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}
	if got := runImageByRunID(t, pg, ctx, dagID, running.RunID); got != v2.Image {
		t.Errorf("in-flight run image = %s, want %s (its pinned version)", got, v2.Image)
	}
	if got := runImageByRunID(t, pg, ctx, dagID, after.RunID); got != v1.Image {
		t.Errorf("new run image = %s, want %s (the version made current)", got, v1.Image)
	}
}

// runImageByRunID reads the image of the version a run is pinned to.
func runImageByRunID(t *testing.T, pg *storage.Postgres, ctx context.Context, dagID, runID string) string {
	t.Helper()
	var image string
	if err := pg.Pool.QueryRow(ctx,
		`SELECT v.image_reference FROM dag_runs r
		   JOIN dags d ON d.id = r.dag_id
		   JOIN dag_versions v ON v.id = r.dag_version_id
		  WHERE d.dag_id = $1 AND r.run_id = $2`, dagID, runID).Scan(&image); err != nil {
		t.Fatalf("select run image: %v", err)
	}
	return image
}

// TestSetCurrentDagVersionAlreadyCurrentIsANoOp: naming the current version
// changes nothing and records no audit entry.
func TestSetCurrentDagVersionAlreadyCurrentIsANoOp(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _, v2 := seedTwoVersions(t, repo, "setcur_noop")
	before := readDagMetadata(t, ctx, repo, dagID)
	_, auditsBefore, err := repo.ListAuditLogs(ctx, "default", dagID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}

	change, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: v2.DagVersion}, "", admitAll)
	if err != nil {
		t.Fatalf("set current: %v", err)
	}
	if change.Changed || change.From.Version != "v2" || change.To.Version != "v2" {
		t.Errorf("change = %+v, want v2 -> v2 unchanged", change)
	}
	if after := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(after, before) {
		t.Errorf("dags row changed by a no-op:\nbefore %+v\nafter  %+v", before, after)
	}
	if _, auditsAfter, _ := repo.ListAuditLogs(ctx, "default", dagID, 100, 0); auditsAfter != auditsBefore {
		t.Errorf("audit entries %d -> %d, want none added by a no-op", auditsBefore, auditsAfter)
	}
}

// TestSetCurrentDagVersionUnknownTargets: an unknown version, an unknown DAG
// and a DAG of another tenant all answer not found and change nothing.
func TestSetCurrentDagVersionUnknownTargets(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _, _ := seedTwoVersions(t, repo, "setcur_unknown")
	other := limitedTenant(ctx, t, repo, "setcur-other", domain.TenantLimitsUpdate{})
	before := readDagMetadata(t, ctx, repo, dagID)

	cases := []struct {
		name, tenant, dag string
		sel               domain.DagVersionSelector
	}{
		{"unknown label", "default", dagID, domain.DagVersionSelector{Version: "v9"}},
		{"unknown number", "default", dagID, domain.DagVersionSelector{Number: 9}},
		{"unknown dag", "default", dagID + "_missing", domain.DagVersionSelector{Version: "v1"}},
		{"other tenant", other, dagID, domain.DagVersionSelector{Version: "v1"}},
	}
	for _, tc := range cases {
		if _, err := repo.SetCurrentDagVersion(ctx, tc.tenant, tc.dag, tc.sel, "", admitAll); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("%s: err = %v, want ErrNotFound", tc.name, err)
		}
	}
	if after := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(after, before) {
		t.Errorf("dags row changed by a refused rollback:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestSetCurrentDagVersionAdmissionRefusal: a version the caller's admission
// check refuses (an execution policy tightened since it was registered) is not
// made current, and the error is the check's.
func TestSetCurrentDagVersionAdmissionRefusal(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _, _ := seedTwoVersions(t, repo, "setcur_admit")
	before := readDagMetadata(t, ctx, repo, dagID)
	refusal := errors.New("refused by policy")
	var seen string
	_, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "",
		func(spec domain.DAGSpec) error { seen = spec.DagVersion; return refusal })
	if !errors.Is(err, refusal) {
		t.Fatalf("err = %v, want the admission refusal", err)
	}
	if seen != "v1" {
		t.Errorf("admission saw spec %q, want the target v1", seen)
	}
	if after := readDagMetadata(t, ctx, repo, dagID); !reflect.DeepEqual(after, before) {
		t.Errorf("dags row changed by a refused rollback:\nbefore %+v\nafter  %+v", before, after)
	}
}

// TestSetCurrentDagVersionAppliesTenantLimits: a version whose schedule is
// below the tenant's min_schedule_interval_seconds, set after it registered,
// is refused like a register of it would be.
func TestSetCurrentDagVersionAppliesTenantLimits(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "setcur-limits", domain.TenantLimitsUpdate{})
	dagID := fmt.Sprintf("setcur_limits_%d", time.Now().UnixNano())
	fast, slow := "*/5 * * * *", "@daily"
	if err := registerIn(ctx, t, repo, tenant, dagID, "v1", &fast); err != nil {
		t.Fatal(err)
	}
	if err := registerIn(ctx, t, repo, tenant, dagID, "v2", &slow); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.EnsureTenant(ctx, tenant, "", 0, domain.TenantLimitsUpdate{MinScheduleIntervalSeconds: limit(3600)}); err != nil {
		t.Fatal(err)
	}
	_, err := repo.SetCurrentDagVersion(ctx, tenant, dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll)
	wantLimitError(t, "rollback to a too frequent schedule", err, "min_schedule_interval_seconds")
	if spec, _ := repo.GetCurrentSpec(ctx, tenant, dagID); spec.DagVersion != "v2" {
		t.Errorf("current = %s after a refused rollback, want v2", spec.DagVersion)
	}
}

// TestSetCurrentDagVersionAudits: the rollback is audited with the acting user
// and the from and to versions.
func TestSetCurrentDagVersionAudits(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID, _, v2 := seedTwoVersions(t, repo, "setcur_audit")
	user, err := repo.CreateUser(ctx, "default", fmt.Sprintf("rollback-%d@example.com", time.Now().UnixNano()), "pw-123456789", nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, user.ID, admitAll); err != nil {
		t.Fatal(err)
	}
	entry := latestAudit(t, repo, dagID, "dag.version.set_current")
	if entry.Owner != user.Email {
		t.Errorf("audit owner = %q, want %q", entry.Owner, user.Email)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(entry.Extra), &meta); err != nil {
		t.Fatalf("audit metadata %q: %v", entry.Extra, err)
	}
	v1 := versionByLabel(t, repo, "default", dagID, "v1")
	want := map[string]any{
		"from_version": v2.DagVersion, "from_version_number": float64(2),
		"to_version": "v1", "to_version_number": float64(1),
		"image": v1.ImageReference, "spec_hash": v1.SpecHash,
	}
	for k, w := range want {
		if meta[k] != w {
			t.Errorf("audit metadata %s = %v, want %v (all: %s)", k, meta[k], w, entry.Extra)
		}
	}
}

// latestAudit returns the newest audit entry of action on dagID.
func latestAudit(t *testing.T, repo *storage.Repository, dagID, action string) domain.AuditLogEntry {
	t.Helper()
	entries, _, err := repo.ListAuditLogs(t.Context(), "default", dagID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Action == action {
			return e
		}
	}
	t.Fatalf("no %s audit entry for %s in %+v", action, dagID, entries)
	return domain.AuditLogEntry{}
}

// TestRegisterDagVersionByRecordsProvenance: a register by a user records the
// user as created_by, the versions listing shows the user, the image and the
// spec hash, and the audit row carries the version, image and spec hash.
func TestRegisterDagVersionByRecordsProvenance(t *testing.T) {
	repo, _, ctx := openRepo(t)
	user, err := repo.CreateUser(ctx, "default", fmt.Sprintf("deployer-%d@example.com", time.Now().UnixNano()), "pw-123456789", nil)
	if err != nil {
		t.Fatal(err)
	}
	dagID := fmt.Sprintf("provenance_%d", time.Now().UnixNano())
	spec := registerTxSpec(dagID, "v1", "a", 3, false)
	spec.Image = "registry.example/etl@sha256:" + strings.Repeat("ab", 32)
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, err := repo.RegisterDagVersionBy(ctx, "default", user.ID, spec, hash); err != nil || !created {
		t.Fatalf("register: created=%v err=%v", created, err)
	}

	v := versionByLabel(t, repo, "default", dagID, "v1")
	if v.CreatedBy != user.ID || v.ImageReference != spec.Image || v.SpecHash != hash || !v.IsCurrent {
		t.Errorf("listed version = %+v, want created_by %s, image %s, hash %s, current", v, user.ID, spec.Image, hash)
	}
	entry := latestAudit(t, repo, dagID, "dag.version.register")
	if entry.Owner != user.Email {
		t.Errorf("audit owner = %q, want %q", entry.Owner, user.Email)
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(entry.Extra), &meta); err != nil {
		t.Fatalf("audit metadata %q: %v", entry.Extra, err)
	}
	if meta["version"] != "v1" || meta["image"] != spec.Image || meta["spec_hash"] != hash {
		t.Errorf("audit metadata = %s, want version, image and spec_hash", entry.Extra)
	}
}

// TestSetCurrentDagVersionOnAOneConnectionPool: a rollback holds one
// connection at a time, so it completes on a pool of one. Its checks used to
// query the pool while the transaction held the only connection and the DAG's
// row lock, and waited until the deadline.
func TestSetCurrentDagVersionOnAOneConnectionPool(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url, MaxOpenConns: 1})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	repo := storage.NewRepository(pg)
	dagID, v1, _ := seedTwoVersions(t, repo, "setcur_onepool")
	change, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll)
	if err != nil {
		t.Fatalf("rollback on a one-connection pool: %v", err)
	}
	if !change.Changed || change.To.Version != v1.DagVersion {
		t.Errorf("change = %+v, want v2 -> v1", change)
	}
}

// TestSetCurrentDagVersionSerializesWithRegisters: a rollback racing a
// register of the same DAG never leaves the dags row describing a version
// other than the current one, whichever commits last.
func TestSetCurrentDagVersionSerializesWithRegisters(t *testing.T) {
	repo, _, ctx := openRepo(t)
	for round := 0; round < 10; round++ {
		dagID, _, _ := seedTwoVersions(t, repo, fmt.Sprintf("setcur_race_%d", round))
		v3 := registerTxSpec(dagID, "v3", "ccc", 11, false)
		hash, err := v3.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		var rerr, serr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			_, rerr = repo.RegisterDagVersion(ctx, "default", v3, hash)
		}()
		go func() {
			defer wg.Done()
			<-start
			_, serr = repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, "", admitAll)
		}()
		close(start)
		wg.Wait()
		if rerr != nil || serr != nil {
			t.Fatalf("round %d: register %v, rollback %v", round, rerr, serr)
		}
		got := readDagMetadata(t, ctx, repo, dagID)
		want := map[string]domain.DAGSpec{"v1": registerTxSpec(dagID, "v1", "a", 3, false), "v3": v3}[got.CurrentVersion]
		if want.DagID == "" {
			t.Fatalf("round %d: current version %q is neither v1 nor v3", round, got.CurrentVersion)
		}
		if !reflect.DeepEqual(got, specMetadata(t, want)) {
			t.Fatalf("round %d: dags row does not describe current %s:\ngot  %+v\nwant %+v", round, want.DagVersion, got, specMetadata(t, want))
		}
	}
}

// TestRegisterDagVersionByAMissingUser: a register on behalf of a user id
// with no users row in the tenant (a deleted user's still-valid token)
// registers with no creator instead of failing on the foreign key.
func TestRegisterDagVersionByAMissingUser(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("provenance_gone_%d", time.Now().UnixNano())
	spec := registerTxSpec(dagID, "v1", "a", 3, false)
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	const gone = "00000000-0000-4000-8000-000000000001"
	if created, err := repo.RegisterDagVersionBy(ctx, "default", gone, spec, hash); err != nil || !created {
		t.Fatalf("register by a missing user: created=%v err=%v", created, err)
	}
	if v := versionByLabel(t, repo, "default", dagID, "v1"); v.CreatedBy != "" {
		t.Errorf("created_by = %q, want empty", v.CreatedBy)
	}
	registerTx(t, ctx, repo, registerTxSpec(dagID, "v2", "bb", 7, true))
	if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Version: "v1"}, gone, admitAll); err != nil {
		t.Fatalf("rollback by a missing user: %v", err)
	}
}
