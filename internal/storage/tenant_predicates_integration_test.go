//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// tenantPredicateFixture is one run of a DAG in the default tenant, with one
// task instance (on its third execution) and one XCom value, plus the id of a
// second, empty tenant that the cross-tenant reads are made as.
type tenantPredicateFixture struct {
	pg       *storage.Postgres
	q        *queries.Queries
	owner    pgtype.UUID // the tenant that owns the run
	other    pgtype.UUID // a second tenant with no runs
	otherNm  string
	dagUUID  pgtype.UUID
	runUUID  pgtype.UUID
	runID    string
	taskID   string
	xcomKey  string
	dagIDTxt string
}

func newTenantPredicateFixture(t *testing.T) (tenantPredicateFixture, *storage.Repository, context.Context) {
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
	repo := storage.NewRepository(pg)

	otherName := uniqueTenant("pred-b")
	if _, err = pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, otherName); err != nil {
		t.Fatalf("inserting tenant %s: %v", otherName, err)
	}
	f := tenantPredicateFixture{
		pg:       pg,
		q:        pg.Queries,
		runID:    "r1",
		taskID:   "extract",
		xcomKey:  "return_value",
		dagIDTxt: fmt.Sprintf("pred_%d", time.Now().UnixNano()),
	}
	owner, err := pg.Queries.GetTenantByName(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	other, err := pg.Queries.GetTenantByName(ctx, otherName)
	if err != nil {
		t.Fatal(err)
	}
	f.owner, f.other, f.otherNm = owner.ID, other.ID, otherName

	registerSpec(t, repo, ctx, f.dagIDTxt, []domain.TaskSpec{{TaskID: f.taskID, Type: domain.TaskTypePython}})
	if _, err = repo.CreateDagRun(ctx, "default", f.dagIDTxt, domain.DagRun{
		RunID: f.runID, LogicalDate: time.Now().UTC(), State: domain.DagRunStateQueued, RunType: "manual",
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}
	dag, err := pg.Queries.GetDagByDagID(ctx, queries.GetDagByDagIDParams{TenantID: f.owner, DagID: f.dagIDTxt})
	if err != nil {
		t.Fatal(err)
	}
	f.dagUUID = dag.ID
	run, err := pg.Queries.GetDagRun(ctx, queries.GetDagRunParams{TenantID: f.owner, DagID: f.dagUUID, RunID: f.runID})
	if err != nil {
		t.Fatalf("owner reads its own run: %v", err)
	}
	f.runUUID = run.ID
	if _, err = pg.Queries.CreateTaskInstance(ctx, queries.CreateTaskInstanceParams{
		TenantID: f.owner, DagRunID: f.runUUID, TaskID: f.taskID, Operator: "python",
		MaxTries: 1, State: queries.TaskStateRunning, PoolSlots: 1,
	}); err != nil {
		t.Fatalf("create task instance: %v", err)
	}
	// A third execution of the try, so the owner's epoch bounds are not the
	// zero a foreign tenant gets back.
	if _, err = pg.Pool.Exec(ctx, `UPDATE task_instances SET attempt_epoch = 3 WHERE dag_run_id = $1 AND task_id = $2`,
		f.runUUID, f.taskID); err != nil {
		t.Fatal(err)
	}
	if err = pg.Queries.RecordXCom(ctx, queries.RecordXComParams{
		TenantID: f.owner, DagRunID: f.runUUID, TaskID: f.taskID, Key: f.xcomKey,
		RedisKey: "xcom:pred:" + f.dagIDTxt, SizeBytes: 2, ContentType: "application/json",
		ExpiresAt: pgtype.Timestamptz{Time: time.Now().Add(time.Hour), Valid: true},
	}); err != nil {
		t.Fatalf("record xcom: %v", err)
	}
	return f, repo, ctx
}

// TestLeafQueriesCarryTheTenantPredicateIntegration covers #508: the read
// queries keyed by internal ids (dag, run) check tenant_id themselves, so the
// internal ids of one tenant's DAG and run, sent with another tenant's id,
// find nothing. Each case also reads as the owner, the single-tenant path,
// which must keep returning the rows.
func TestLeafQueriesCarryTheTenantPredicateIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)
	q := f.q

	notFound := func(t *testing.T, name string, err error) {
		t.Helper()
		if !errors.Is(err, pgx.ErrNoRows) {
			t.Errorf("%s as another tenant: err = %v, want no rows", name, err)
		}
	}
	for _, tc := range []struct {
		name string
		as   pgtype.UUID
		want bool // a row comes back
	}{
		{"owner", f.owner, true},
		{"other tenant", f.other, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: tc.as, DagID: f.dagUUID, RunID: f.runID})
			if tc.want && err != nil {
				t.Errorf("GetDagRun: %v", err)
			} else if !tc.want {
				notFound(t, "GetDagRun", err)
			}

			_, err = q.GetDagRunWithVersion(ctx, queries.GetDagRunWithVersionParams{TenantID: tc.as, DagID: f.dagUUID, RunID: f.runID})
			if tc.want && err != nil {
				t.Errorf("GetDagRunWithVersion: %v", err)
			} else if !tc.want {
				notFound(t, "GetDagRunWithVersion", err)
			}

			_, err = q.GetXComEntry(ctx, queries.GetXComEntryParams{TenantID: tc.as, DagRunID: f.runUUID, TaskID: f.taskID, Key: f.xcomKey})
			if tc.want && err != nil {
				t.Errorf("GetXComEntry: %v", err)
			} else if !tc.want {
				notFound(t, "GetXComEntry", err)
			}

			wantN := 0
			if tc.want {
				wantN = 1
			}
			counts := map[string]func() (int64, error){
				"CountDagRunsByDag": func() (int64, error) {
					return q.CountDagRunsByDag(ctx, queries.CountDagRunsByDagParams{DagID: f.dagUUID, TenantID: tc.as})
				},
				"CountDagRunsByDagStates": func() (int64, error) {
					return q.CountDagRunsByDagStates(ctx, queries.CountDagRunsByDagStatesParams{TenantID: tc.as, DagID: f.dagUUID, States: []string{"queued"}})
				},
				"CountActiveDagRunsByDagID": func() (int64, error) {
					return q.CountActiveDagRunsByDagID(ctx, queries.CountActiveDagRunsByDagIDParams{TenantID: tc.as, DagID: f.dagUUID})
				},
			}
			for name, count := range counts {
				n, cerr := count()
				if cerr != nil || int(n) != wantN {
					t.Errorf("%s = %d, %v; want %d", name, n, cerr, wantN)
				}
			}

			lists := map[string]func() (int, error){
				"ListDagRunsByDag": func() (int, error) {
					rows, lerr := q.ListDagRunsByDag(ctx, queries.ListDagRunsByDagParams{TenantID: tc.as, DagID: f.dagUUID, Limit: 10})
					return len(rows), lerr
				},
				"ListDagRunsByDagWithVersion": func() (int, error) {
					rows, lerr := q.ListDagRunsByDagWithVersion(ctx, queries.ListDagRunsByDagWithVersionParams{TenantID: tc.as, DagID: f.dagUUID, Limit: 10})
					return len(rows), lerr
				},
				"ListDagRunsByDagAfter": func() (int, error) {
					rows, lerr := q.ListDagRunsByDagAfter(ctx, queries.ListDagRunsByDagAfterParams{
						TenantID: tc.as, DagID: f.dagUUID, States: []string{},
						AfterLogicalDate: pgtype.Timestamptz{Time: time.Now().Add(24 * time.Hour), Valid: true},
						AfterRunID:       "~", RowLimit: 10,
					})
					return len(rows), lerr
				},
				"ListTaskInstancesByRun": func() (int, error) {
					rows, lerr := q.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: tc.as, DagRunID: f.runUUID})
					return len(rows), lerr
				},
				"ListTaskInstanceAttempts": func() (int, error) {
					rows, lerr := q.ListTaskInstanceAttempts(ctx, queries.ListTaskInstanceAttemptsParams{TenantID: tc.as, DagRunID: f.runUUID, TaskID: f.taskID})
					return len(rows), lerr
				},
			}
			for name, list := range lists {
				n, lerr := list()
				if lerr != nil || n != wantN {
					t.Errorf("%s = %d rows, %v; want %d", name, n, lerr, wantN)
				}
			}

			bounds, err := q.TryAttemptEpochBounds(ctx, queries.TryAttemptEpochBoundsParams{TenantID: tc.as, DagRunID: f.runUUID, TaskID: f.taskID, TryNumber: 1})
			wantHigh := int32(0)
			if tc.want {
				wantHigh = 3
			}
			if err != nil || bounds.High != wantHigh {
				t.Errorf("TryAttemptEpochBounds high = %d, %v; want %d", bounds.High, err, wantHigh)
			}
		})
	}
}

// TestDeleteDagRunNeedsTheOwningTenantIntegration covers the one write of the
// slice (#508): deleting by the run's internal dag id as another tenant
// removes nothing, and the owner still can.
func TestDeleteDagRunNeedsTheOwningTenantIntegration(t *testing.T) {
	f, _, ctx := newTenantPredicateFixture(t)

	n, err := f.q.DeleteDagRun(ctx, queries.DeleteDagRunParams{TenantID: f.other, DagID: f.dagUUID, RunID: f.runID})
	if err != nil || n != 0 {
		t.Fatalf("DeleteDagRun as another tenant = %d rows, %v; want 0", n, err)
	}
	if _, err = f.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: f.owner, DagID: f.dagUUID, RunID: f.runID}); err != nil {
		t.Fatalf("the run is gone after a foreign delete: %v", err)
	}
	n, err = f.q.DeleteDagRun(ctx, queries.DeleteDagRunParams{TenantID: f.owner, DagID: f.dagUUID, RunID: f.runID})
	if err != nil || n != 1 {
		t.Fatalf("DeleteDagRun as the owner = %d rows, %v; want 1", n, err)
	}
}

// TestRepositoryRunReadsStayInTheCallersTenantIntegration covers the
// repository surface the API calls (#508): a second tenant that registers a
// DAG with the same dag_id sees none of the first tenant's runs, task
// instances or attempts, and the first tenant reads them as before.
func TestRepositoryRunReadsStayInTheCallersTenantIntegration(t *testing.T) {
	f, repo, ctx := newTenantPredicateFixture(t)
	other := f.otherNm
	spec := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: f.dagIDTxt, DagVersion: "v1", Image: "img:v1",
		Tasks: []domain.TaskSpec{{TaskID: f.taskID, Type: domain.TaskTypePython}},
	}
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RegisterDagVersion(ctx, other, spec, hash); err != nil {
		t.Fatalf("register in %s: %v", other, err)
	}

	if _, err = repo.GetDagRun(ctx, other, f.dagIDTxt, f.runID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("GetDagRun as %s: err = %v, want not found", other, err)
	}
	if _, _, err = repo.ListTaskInstances(ctx, other, f.dagIDTxt, f.runID, 0, 0); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ListTaskInstances as %s: err = %v, want not found", other, err)
	}
	if _, err = repo.ListTaskInstanceAttempts(ctx, other, f.dagIDTxt, f.runID, f.taskID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("ListTaskInstanceAttempts as %s: err = %v, want not found", other, err)
	}
	if err = repo.DeleteDagRun(ctx, other, f.dagIDTxt, f.runID); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("DeleteDagRun as %s: err = %v, want not found", other, err)
	}
	runs, total, err := repo.ListDagRuns(ctx, other, f.dagIDTxt, 10, 0)
	if err != nil || len(runs) != 0 || total != 0 {
		t.Errorf("ListDagRuns as %s = %d runs, total %d, %v; want none", other, len(runs), total, err)
	}

	if _, err = repo.GetDagRun(ctx, "default", f.dagIDTxt, f.runID); err != nil {
		t.Errorf("GetDagRun as the owner: %v", err)
	}
	tis, _, err := repo.ListTaskInstances(ctx, "default", f.dagIDTxt, f.runID, 0, 0)
	if err != nil || len(tis) != 1 {
		t.Errorf("ListTaskInstances as the owner = %d, %v; want 1", len(tis), err)
	}
	runs, total, err = repo.ListDagRuns(ctx, "default", f.dagIDTxt, 10, 0)
	if err != nil || len(runs) != 1 || total != 1 {
		t.Errorf("ListDagRuns as the owner = %d runs, total %d, %v; want 1", len(runs), total, err)
	}
}
