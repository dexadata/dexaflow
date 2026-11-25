//go:build integration

package storage_test

// Listing DAGs with the UI's filters, tags and ordering.

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestListDagsWithFilterAndTagsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	prefix := fmt.Sprintf("filt_%d", time.Now().UnixNano())
	dag1 := prefix + "_alpha"
	dag2 := prefix + "_beta"
	dag3 := prefix + "_gamma"

	registerWithMeta := func(dagID, owner string, tags []string) {
		spec := domain.DAGSpec{
			SchemaVersion: "1.0",
			DagID:         dagID,
			DagVersion:    "v1",
			Image:         "img:v1",
			Owner:         owner,
			Tags:          tags,
			Tasks:         []domain.TaskSpec{{TaskID: "t1", Type: domain.TaskTypePython}},
		}
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		if created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash); rerr != nil || !created {
			t.Fatalf("register %s: %v", dagID, rerr)
		}
	}

	registerWithMeta(dag1, "alice", []string{"etl", "daily"})
	registerWithMeta(dag2, "bob", []string{"etl", "hourly"})
	registerWithMeta(dag3, "alice", []string{"ml", "weekly"})

	// Pause dag2 to test paused filter
	if _, err := repo.SetPausedBy(ctx, "default", "u1", dag2, true); err != nil {
		t.Fatalf("SetPausedBy: %v", err)
	}

	// 1. Filter by dag_id_pattern
	dags, total, err := repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
		DagIDPattern: prefix + "_alpha",
		Limit:        10,
	})
	if err != nil {
		t.Fatalf("filter pattern: %v", err)
	}
	if total != 1 || len(dags) != 1 || dags[0].DagID != dag1 {
		t.Errorf("expected dag1, got total=%d, dags=%+v", total, dags)
	}

	// 2. Filter by paused
	isPaused := true
	dags, total, err = repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
		DagIDPrefixPattern: prefix,
		Paused:             &isPaused,
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("filter paused: %v", err)
	}
	if total != 1 || len(dags) != 1 || dags[0].DagID != dag2 {
		t.Errorf("expected paused dag2, got total=%d, dags=%+v", total, dags)
	}

	// 3. Filter by tags - match any
	dags, total, err = repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
		DagIDPrefixPattern: prefix,
		Tags:               []string{"daily", "weekly"},
		TagsMatchMode:      "any",
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("filter tags any: %v", err)
	}
	if total != 2 || len(dags) != 2 {
		t.Errorf("expected 2 dags with daily/weekly, got total=%d, dags=%+v", total, dags)
	}

	// 4. Filter by tags - match all
	dags, total, err = repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
		DagIDPrefixPattern: prefix,
		Tags:               []string{"etl", "daily"},
		TagsMatchMode:      "all",
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("filter tags all: %v", err)
	}
	if total != 1 || len(dags) != 1 || dags[0].DagID != dag1 {
		t.Errorf("expected 1 dag matching both etl and daily, got total=%d, dags=%+v", total, dags)
	}

	// 5. Filter by owner
	dags, total, err = repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
		DagIDPrefixPattern: prefix,
		Owners:             []string{"bob"},
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("filter owners: %v", err)
	}
	if total != 1 || len(dags) != 1 || dags[0].DagID != dag2 {
		t.Errorf("expected dag2 owned by bob, got total=%d, dags=%+v", total, dags)
	}

	// 6. Order by descending dag_id
	dags, total, err = repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
		DagIDPrefixPattern: prefix,
		OrderBy:            "-dag_id",
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("order by: %v", err)
	}
	if total != 3 || len(dags) != 3 || dags[0].DagID != dag3 || dags[2].DagID != dag1 {
		t.Errorf("expected descending order (gamma, beta, alpha), got %+v", dags)
	}

	// 7. ListDagTags
	tags, tagTotal, err := repo.ListDagTags(ctx, "default", "", "", 10, 0)
	if err != nil {
		t.Fatalf("ListDagTags: %v", err)
	}
	if tagTotal < 4 {
		t.Errorf("expected at least 4 tags across dags, got %d tags (%v)", tagTotal, tags)
	}

	// ListDagTags with pattern
	tags, tagTotal, err = repo.ListDagTags(ctx, "default", "hour", "", 10, 0)
	if err != nil {
		t.Fatalf("ListDagTags with pattern: %v", err)
	}
	if tagTotal != 1 || len(tags) != 1 || tags[0] != "hourly" {
		t.Errorf("expected 1 tag 'hourly', got total=%d tags=%v", tagTotal, tags)
	}
	// The UI's tag filter sends tag_name_prefix_pattern: "week" matches
	// "weekly", "eek" (a substring, not a prefix) matches nothing.
	if tags, _, err = repo.ListDagTags(ctx, "default", "", "week", 10, 0); err != nil || len(tags) != 1 || tags[0] != "weekly" {
		t.Errorf("prefix week: tags=%v err=%v", tags, err)
	}
	if tags, _, err = repo.ListDagTags(ctx, "default", "", "eek", 10, 0); err != nil || len(tags) != 0 {
		t.Errorf("prefix eek must not match: tags=%v err=%v", tags, err)
	}
}

// TestListDagsOrderByLatestRunIntegration covers the UI's sort options that
// read the latest run (#1927): a DAG with no run sorts last both ways.
func TestListDagsOrderByLatestRunIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pool, err := pgxpool.New(ctx, os.Getenv("DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	prefix := fmt.Sprintf("ordrun_%d", time.Now().UnixNano())
	early, late, never := prefix+"_a", prefix+"_b", prefix+"_c"
	now := time.Now().UTC()
	for _, id := range []string{early, late, never} {
		registerSpec(t, repo, ctx, id, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
	}
	for i, tc := range []struct {
		id    string
		state string
	}{{early, "success"}, {late, "failed"}} {
		if _, err := repo.CreateDagRun(ctx, "default", tc.id, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: now,
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		if _, err := pool.Exec(ctx, `UPDATE dag_runs SET state=$2::dag_run_state, started_at=$3 WHERE dag_id IN (SELECT id FROM dags WHERE dag_id=$1)`,
			tc.id, tc.state, now.Add(time.Duration(i)*time.Minute)); err != nil {
			t.Fatal(err)
		}
	}

	for _, tc := range []struct{ orderBy, want string }{
		{"last_run_start_date", early + "," + late + "," + never},
		{"-last_run_start_date", late + "," + early + "," + never},
		{"last_run_state", late + "," + early + "," + never},
		{"-last_run_state", early + "," + late + "," + never},
		{"-dag_display_name", never + "," + late + "," + early},
	} {
		dags, _, err := repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{
			DagIDPrefixPattern: prefix, OrderBy: tc.orderBy, Limit: 10,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.orderBy, err)
		}
		ids := make([]string, 0, len(dags))
		for _, d := range dags {
			ids = append(ids, d.DagID)
		}
		if got := strings.Join(ids, ","); got != tc.want {
			t.Errorf("order_by=%s: got %s, want %s", tc.orderBy, got, tc.want)
		}
	}
}

func TestFilteredListQueriesIntegration(t *testing.T) {
	repo, sched, ctx := openRepo(t)
	c, err := secrets.NewAESGCM([]byte("12345678901234567890123456789012"))
	if err != nil {
		t.Fatal(err)
	}
	repo.Configure(storage.WithCipher(c))

	prefix := fmt.Sprintf("flq_%d_", time.Now().UnixNano())

	// 1. Connections
	c1 := prefix + "conn_alpha"
	c2 := prefix + "conn_beta"
	c3 := prefix + "other_gamma"
	for _, cid := range []string{c1, c2, c3} {
		if err := repo.SetConnectionPatch(ctx, "default", domain.ConnectionPatch{
			ConnID: cid, ConnType: "http", Host: ptr("localhost"),
		}); err != nil {
			t.Fatalf("set connection %s: %v", cid, err)
		}
		t.Cleanup(func() { _ = repo.DeleteConnection(ctx, "default", cid) })
	}

	conns, total, err := repo.ListConnectionsFiltered(ctx, "default", "", prefix+"conn_", 10, 0)
	if err != nil {
		t.Fatalf("list connections prefix: %v", err)
	}
	if total != 2 || len(conns) != 2 {
		t.Errorf("list connections prefix: got %d (len %d), want 2", total, len(conns))
	}

	conns, total, err = repo.ListConnectionsFiltered(ctx, "default", "beta", "", 10, 0)
	if err != nil {
		t.Fatalf("list connections substring: %v", err)
	}
	if total != 1 || len(conns) != 1 || conns[0].ConnID != c2 {
		t.Errorf("list connections substring: got %d (len %d), want 1 (%s)", total, len(conns), c2)
	}

	// 2. Variables
	v1 := prefix + "var_alpha"
	v2 := prefix + "var_beta"
	v3 := prefix + "other_gamma"
	for _, k := range []string{v1, v2, v3} {
		val := "val_" + k
		if err := repo.SetVariablePatch(ctx, "default", domain.VariablePatch{
			Key: k, Value: &val,
		}); err != nil {
			t.Fatalf("set variable %s: %v", k, err)
		}
		t.Cleanup(func() { _ = repo.DeleteVariable(ctx, "default", k) })
	}

	vars, total, err := repo.ListVariablesFiltered(ctx, "default", "", prefix+"var_", 10, 0)
	if err != nil {
		t.Fatalf("list variables prefix: %v", err)
	}
	if total != 2 || len(vars) != 2 {
		t.Errorf("list variables prefix: got %d (len %d), want 2", total, len(vars))
	}

	vars, total, err = repo.ListVariablesFiltered(ctx, "default", "beta", "", 10, 0)
	if err != nil {
		t.Fatalf("list variables substring: %v", err)
	}
	if total != 1 || len(vars) != 1 || vars[0].Key != v2 {
		t.Errorf("list variables substring: got %d (len %d), want 1 (%s)", total, len(vars), v2)
	}

	// 3. ImportErrors
	f1 := prefix + "dag1.py"
	f2 := prefix + "dag2.py"
	if err := repo.SetImportError(ctx, "default", domain.ImportError{
		Filename: f1, StackTrace: "err1", BundleName: prefix + "bundleA",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.ClearImportError(ctx, "default", f1) })
	if err := repo.SetImportError(ctx, "default", domain.ImportError{
		Filename: f2, StackTrace: "err2", BundleName: prefix + "bundleB",
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = repo.ClearImportError(ctx, "default", f2) })

	errs, total, err := repo.ListImportErrorsFiltered(ctx, "default", f1, "", 10, 0)
	if err != nil {
		t.Fatalf("list import errors filename: %v", err)
	}
	if total != 1 || len(errs) != 1 || errs[0].Filename != f1 {
		t.Errorf("list import errors filename: got %d (len %d), want 1 (%s)", total, len(errs), f1)
	}

	errs, total, err = repo.ListImportErrorsFiltered(ctx, "default", "", prefix+"bundleB", 10, 0)
	if err != nil {
		t.Fatalf("list import errors bundle: %v", err)
	}
	if total != 1 || len(errs) != 1 || errs[0].Filename != f2 {
		t.Errorf("list import errors bundle: got %d (len %d), want 1 (%s)", total, len(errs), f2)
	}

	// 4. DAG Runs and Task Instances
	dagID := prefix + "dag"
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython, Pool: "pool_a"},
		{TaskID: "transform", Type: domain.TaskTypePython, Pool: "pool_b"},
	}
	registerSpec(t, repo, ctx, dagID, tasks)
	t.Cleanup(func() { _ = repo.DeleteDag(ctx, "default", dagID) })

	r1 := "scheduled__1"
	r2 := "manual__2"
	t1 := time.Now().UTC().Add(-time.Hour)
	t2 := time.Now().UTC()

	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: r1, State: domain.DagRunStateRunning, RunType: "scheduled", LogicalDate: t1,
	}); err != nil {
		t.Fatalf("create r1: %v", err)
	}
	runUUID := ""
	runsActive, _ := sched.ActiveRuns(ctx)
	for _, r := range runsActive {
		if r.DagID == dagID {
			runUUID = r.RunID
			break
		}
	}
	if runUUID == "" {
		t.Fatal("could not resolve runUUID for r1")
	}
	if err := sched.MaterializeTasks(ctx, runUUID, tasks); err != nil {
		t.Fatalf("MaterializeTasks: %v", err)
	}

	if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
		RunID: r2, State: domain.DagRunStateFailed, RunType: "manual", LogicalDate: t2,
	}); err != nil {
		t.Fatalf("create r2: %v", err)
	}

	// DagRun filters
	runs, total, err := repo.ListDagRunsWithFilter(ctx, "default", dagID, domain.DagRunFilter{
		RunIDPrefixPattern: "manual__",
		Limit:              10,
	})
	if err != nil {
		t.Fatalf("list dag runs prefix: %v", err)
	}
	if total != 1 || len(runs) != 1 || runs[0].RunID != r2 {
		t.Errorf("list dag runs prefix: got %d (len %d), want 1 (%s)", total, len(runs), r2)
	}

	runs, total, err = repo.ListDagRunsWithFilter(ctx, "default", dagID, domain.DagRunFilter{
		RunType: "scheduled",
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("list dag runs run_type: %v", err)
	}
	if total != 1 || len(runs) != 1 || runs[0].RunID != r1 {
		t.Errorf("list dag runs run_type: got %d (len %d), want 1 (%s)", total, len(runs), r1)
	}

	runs, total, err = repo.ListDagRunsWithFilter(ctx, "default", dagID, domain.DagRunFilter{
		States: []string{"failed"},
		Limit:  10,
	})
	if err != nil {
		t.Fatalf("list dag runs state: %v", err)
	}
	if total != 1 || len(runs) != 1 || runs[0].RunID != r2 {
		t.Errorf("list dag runs state: got %d (len %d), want 1 (%s)", total, len(runs), r2)
	}

	// TaskInstance filters
	tis, total, err := repo.ListTaskInstancesWithFilter(ctx, "default", dagID, r1, domain.TaskInstanceFilter{
		TaskIDPrefixPattern: "ext",
		Limit:               10,
	})
	if err != nil {
		t.Fatalf("list tis prefix: %v", err)
	}
	if total != 1 || len(tis) != 1 || tis[0].TaskID != "extract" {
		t.Errorf("list tis prefix: got %d (len %d), want 1 (extract)", total, len(tis))
	}

	tis, total, err = repo.ListTaskInstancesWithFilter(ctx, "default", dagID, r1, domain.TaskInstanceFilter{
		Pool:  "pool_b",
		Limit: 10,
	})
	if err != nil {
		t.Fatalf("list tis pool: %v", err)
	}
	if total != 1 || len(tis) != 1 || tis[0].TaskID != "transform" {
		t.Errorf("list tis pool: got %d (len %d), want 1 (transform)", total, len(tis))
	}
}

// An unknown last_dag_run_state is the client's mistake: it must come back as
// a validation error (400), not reach Postgres and fail the enum cast (500,
// #2202). A known state still filters.
func TestListDagsWithFilterRefusesAnUnknownRunState(t *testing.T) {
	repo, _, ctx := openRepo(t)
	_, _, err := repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{LastDagRunState: "bogus", Limit: 10})
	if !errors.Is(err, domain.ErrValidation) {
		t.Fatalf("unknown state: err = %v, want ErrValidation", err)
	}
	if _, _, err := repo.ListDagsWithFilter(ctx, "default", domain.DagFilter{LastDagRunState: "success", Limit: 10}); err != nil {
		t.Fatalf("known state: %v", err)
	}
}
