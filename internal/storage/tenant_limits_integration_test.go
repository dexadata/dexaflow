//go:build integration

package storage_test

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

func limit(n int) *int { return &n }

// registerIn registers a one-task DAG in tenant with the given schedule and
// version label, returning the repository error.
func registerIn(ctx context.Context, t *testing.T, repo *storage.Repository, tenant, dagID, version string, schedule *string) error {
	t.Helper()
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: version, Image: "img:" + version, Schedule: schedule,
		Tasks: []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypeBash, Entrypoint: "true"}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	_, err = repo.RegisterDagVersion(ctx, tenant, spec, hash)
	return err
}

// limitedTenant creates a fresh tenant with the given limits.
func limitedTenant(ctx context.Context, t *testing.T, repo *storage.Repository, prefix string, limits domain.TenantLimitsUpdate) string {
	t.Helper()
	name := uniqueTenant(prefix)
	if _, err := repo.EnsureTenant(ctx, name, "", 0, limits); err != nil {
		t.Fatalf("EnsureTenant(%s): %v", name, err)
	}
	return name
}

func wantLimitError(t *testing.T, what string, err error, phrase string) {
	t.Helper()
	if !errors.Is(err, domain.ErrLimitExceeded) {
		t.Fatalf("%s: err = %v, want ErrLimitExceeded", what, err)
	}
	if !strings.Contains(err.Error(), phrase) {
		t.Errorf("%s: error %q does not name the limit (%q)", what, err, phrase)
	}
}

// TestEnsureTenantStoresLimits: limits given are stored, a later call that
// leaves a limit out keeps it, and 0 clears it back to unlimited. A tenant
// created without limits has none.
func TestEnsureTenantStoresLimits(t *testing.T) {
	repo, _, ctx := openRepo(t)
	name := limitedTenant(ctx, t, repo, "limits", domain.TenantLimitsUpdate{
		MaxDags: limit(10), MaxRunsPerDay: limit(50), MinScheduleIntervalSeconds: limit(900),
	})

	got, err := repo.TenantLimits(ctx, name)
	if err != nil || got != (domain.TenantLimits{MaxDags: 10, MaxRunsPerDay: 50, MinScheduleIntervalSeconds: 900}) {
		t.Fatalf("TenantLimits = %+v, %v; want 10/50/900", got, err)
	}

	if _, err := repo.EnsureTenant(ctx, name, "", 0, domain.TenantLimitsUpdate{MaxDags: limit(0)}); err != nil {
		t.Fatal(err)
	}
	got, err = repo.TenantLimits(ctx, name)
	if err != nil || got != (domain.TenantLimits{MaxDags: 0, MaxRunsPerDay: 50, MinScheduleIntervalSeconds: 900}) {
		t.Errorf("after clearing max_dags, TenantLimits = %+v, %v; want 0/50/900", got, err)
	}

	plain := limitedTenant(ctx, t, repo, "nolimits", domain.TenantLimitsUpdate{})
	got, err = repo.TenantLimits(ctx, plain)
	if err != nil || got != (domain.TenantLimits{}) {
		t.Errorf("tenant without limits: TenantLimits = %+v, %v; want all zero", got, err)
	}
}

// TestRegisterDagVersionEnforcesMaxDags: a tenant at max_dags cannot register a
// new DAG, but can still push new versions of the DAGs it has; another tenant
// is not affected.
func TestRegisterDagVersionEnforcesMaxDags(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "maxdags", domain.TenantLimitsUpdate{MaxDags: limit(2)})
	for _, id := range []string{"a", "b"} {
		if err := registerIn(ctx, t, repo, tenant, id, "v1", nil); err != nil {
			t.Fatalf("register %s under the cap: %v", id, err)
		}
	}

	err := registerIn(ctx, t, repo, tenant, "c", "v1", nil)

	wantLimitError(t, "third DAG", err, "max_dags of 2")
	if err := registerIn(ctx, t, repo, tenant, "a", "v2", nil); err != nil {
		t.Errorf("new version of an existing DAG at the cap: %v", err)
	}
	other := limitedTenant(ctx, t, repo, "maxdags-other", domain.TenantLimitsUpdate{})
	if err := registerIn(ctx, t, repo, other, "c", "v1", nil); err != nil {
		t.Errorf("unlimited tenant: %v", err)
	}
}

// TestRegisterDagVersionEnforcesMinScheduleInterval: a schedule that fires more
// often than the tenant allows is refused; one at the minimum, a manual DAG and
// @once are accepted.
func TestRegisterDagVersionEnforcesMinScheduleInterval(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "interval", domain.TenantLimitsUpdate{MinScheduleIntervalSeconds: limit(900)})
	str := func(s string) *string { return &s }

	err := registerIn(ctx, t, repo, tenant, "fast", "v1", str("*/5 * * * *"))

	wantLimitError(t, "every 5 minutes", err, "min_schedule_interval_seconds of 900")
	for id, schedule := range map[string]*string{"quarter": str("*/15 * * * *"), "manual": nil, "once": str("@once")} {
		if err := registerIn(ctx, t, repo, tenant, id, "v1", schedule); err != nil {
			t.Errorf("%s: %v", id, err)
		}
	}
}

// TestRunsPerDayCountsManualAndScheduledRuns: manual and scheduled runs share
// the tenant's daily cap; a refused or duplicate creation does not use it up;
// the count starts again on the next UTC day.
func TestRunsPerDayCountsManualAndScheduledRuns(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "runsperday", domain.TenantLimitsUpdate{MaxRunsPerDay: limit(2)})
	if err := registerIn(ctx, t, repo, tenant, "etl", "v1", nil); err != nil {
		t.Fatal(err)
	}
	tenantID := tenantUUID(ctx, t, tenant)
	manual := func(runID string) error {
		_, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
			RunID: runID, LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
		})
		return err
	}
	slot := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

	if err := manual("m1"); err != nil {
		t.Fatalf("first run: %v", err)
	}
	if err := manual("m1"); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("duplicate run id: err = %v, want ErrConflict", err)
	}
	if err := sched.CreateScheduledRun(ctx, tenantID, "etl", slot); err != nil {
		t.Fatalf("second run (scheduled): %v", err)
	}
	if err := sched.CreateScheduledRun(ctx, tenantID, "etl", slot); err != nil {
		t.Fatalf("an existing scheduled slot is a no-op, got %v", err)
	}
	wantLimitError(t, "third run (manual)", manual("m2"), "max_runs_per_day of 2")
	wantLimitError(t, "third run (scheduled)", sched.CreateScheduledRun(ctx, tenantID, "etl", slot.Add(time.Hour)), "max_runs_per_day of 2")
	if n := countRuns(ctx, t, tenant); n != 2 {
		t.Errorf("runs stored = %d, want 2 (a refused run leaves nothing behind)", n)
	}

	pretendYesterday(ctx, t, tenant)
	if err := manual("m3"); err != nil {
		t.Errorf("first run of a new UTC day: %v", err)
	}
}

// TestRunsPerDayIsExactUnderConcurrency: many triggers racing for the last
// slots of the day create exactly the cap, never more.
func TestRunsPerDayIsExactUnderConcurrency(t *testing.T) {
	repo, _, ctx := openRepo(t)
	tenant := limitedTenant(ctx, t, repo, "runsrace", domain.TenantLimitsUpdate{MaxRunsPerDay: limit(3)})
	if err := registerIn(ctx, t, repo, tenant, "etl", "v1", nil); err != nil {
		t.Fatal(err)
	}
	const callers = 12
	errs := make(chan error, callers)
	for i := range callers {
		go func() {
			_, err := repo.CreateDagRun(ctx, tenant, "etl", domain.DagRun{
				RunID: "r" + string(rune('a'+i)), LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
			})
			errs <- err
		}()
	}

	ok, refused := 0, 0
	for range callers {
		switch err := <-errs; {
		case err == nil:
			ok++
		case errors.Is(err, domain.ErrLimitExceeded):
			refused++
		default:
			t.Errorf("unexpected error: %v", err)
		}
	}

	if ok != 3 || refused != callers-3 {
		t.Errorf("created %d, refused %d; want exactly 3 created", ok, refused)
	}
}

func adminPool(ctx context.Context, t *testing.T) *pgxpool.Pool {
	t.Helper()
	pg, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pg.Close)
	return pg
}

func tenantUUID(ctx context.Context, t *testing.T, tenant string) string {
	t.Helper()
	var id string
	if err := adminPool(ctx, t).QueryRow(ctx, `SELECT id::text FROM tenants WHERE name = $1`, tenant).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func countRuns(ctx context.Context, t *testing.T, tenant string) int {
	t.Helper()
	var n int
	if err := adminPool(ctx, t).QueryRow(ctx,
		`SELECT count(*) FROM dag_runs WHERE tenant_id = (SELECT id FROM tenants WHERE name = $1)`, tenant).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// pretendYesterday moves the tenant's run counter to the previous UTC day, as if
// the day had turned since the last run.
func pretendYesterday(ctx context.Context, t *testing.T, tenant string) {
	t.Helper()
	if _, err := adminPool(ctx, t).Exec(ctx,
		`UPDATE tenants SET runs_day = runs_day - 1 WHERE name = $1`, tenant); err != nil {
		t.Fatal(err)
	}
}
