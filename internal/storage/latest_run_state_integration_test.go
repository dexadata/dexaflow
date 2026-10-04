//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// latestRunFixture is a tenant of its own, so the counts asserted against it
// are exact no matter what other tests left in the shared database.
type latestRunFixture struct {
	repo   *storage.Repository
	pool   *pgxpool.Pool
	tenant string
	tid    string
	dags   int
}

// seedLatestRunFixture creates a tenant whose DAGs cover every case the
// latest-run-state queries must keep:
//
//	a_failed    newest run failed, an older one succeeded
//	b_success   newest run succeeded, an older one failed
//	c_no_runs   no runs at all
//	d_paused    paused, newest run failed
//	e_inactive  soft-deleted (is_active = false), newest run failed
//	f_running   newest run running, an older one queued
//
// A second tenant gets a DAG whose newest run failed, which must never leak
// into the first tenant's lists or counters.
func seedLatestRunFixture(t *testing.T) (*latestRunFixture, context.Context) {
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
	f := &latestRunFixture{repo: storage.NewRepository(pg), pool: pg.Pool}

	suffix := time.Now().UnixNano()
	f.tenant = fmt.Sprintf("latest_run_%d", suffix)
	f.tid = insertTenant(t, ctx, f.pool, f.tenant)
	other := insertTenant(t, ctx, f.pool, fmt.Sprintf("latest_run_other_%d", suffix))
	t.Cleanup(func() {
		_, _ = f.pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = ANY($1::uuid[])`, []string{f.tid, other})
	})

	now := time.Now().UTC()
	type run struct {
		state string
		age   time.Duration
	}
	seed := []struct {
		tid, dagID       string
		paused, inactive bool
		runs             []run
	}{
		{f.tid, "a_failed", false, false, []run{{"success", 2 * time.Hour}, {"failed", time.Hour}}},
		{f.tid, "b_success", false, false, []run{{"failed", 2 * time.Hour}, {"success", time.Hour}}},
		{f.tid, "c_no_runs", false, false, nil},
		{f.tid, "d_paused", true, false, []run{{"success", 2 * time.Hour}, {"failed", time.Hour}}},
		{f.tid, "e_inactive", false, true, []run{{"failed", time.Hour}}},
		{f.tid, "f_running", false, false, []run{{"queued", 2 * time.Hour}, {"running", time.Hour}}},
		{other, "a_failed", false, false, []run{{"failed", time.Hour}}},
	}
	for _, d := range seed {
		if d.tid == f.tid {
			f.dags++
		}
		var dagUUID, versionUUID string
		if err := f.pool.QueryRow(ctx, `
INSERT INTO dags (tenant_id, dag_id, is_paused, is_active) VALUES ($1, $2, $3, $4) RETURNING id`,
			d.tid, d.dagID, d.paused, !d.inactive).Scan(&dagUUID); err != nil {
			t.Fatalf("insert dag %s: %v", d.dagID, err)
		}
		if err := f.pool.QueryRow(ctx, `
INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash)
VALUES ($1, 'v1', 'img:v1', '{}', 'h') RETURNING id`, dagUUID).Scan(&versionUUID); err != nil {
			t.Fatalf("insert version %s: %v", d.dagID, err)
		}
		for i, r := range d.runs {
			if _, err := f.pool.Exec(ctx, `
INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
VALUES ($1, $2, $3, $4, $5, $6, 'manual')`,
				d.tid, dagUUID, versionUUID, fmt.Sprintf("r%d", i), now.Add(-r.age), r.state); err != nil {
				t.Fatalf("insert run %s/%d: %v", d.dagID, i, err)
			}
		}
	}
	return f, ctx
}

func insertTenant(t *testing.T, ctx context.Context, pool *pgxpool.Pool, name string) string {
	t.Helper()
	var id string
	if err := pool.QueryRow(ctx, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, name).Scan(&id); err != nil {
		t.Fatalf("insert tenant %s: %v", name, err)
	}
	return id
}

// TestListDagsFilteredLatestRunSemantics pins what the DAG list returns for
// each latest-run-state filter: only the newest run of each DAG counts, a DAG
// without runs is listed unfiltered but matches no state, inactive DAGs and
// other tenants never appear, and the total ignores LIMIT/OFFSET.
func TestListDagsFilteredLatestRunSemantics(t *testing.T) {
	f, ctx := seedLatestRunFixture(t)
	no := false
	for _, tc := range []struct {
		name          string
		state         string
		paused        *bool
		limit, offset int
		want          []string
		total         int
	}{
		{"unfiltered", "", nil, 100, 0, []string{"a_failed", "b_success", "c_no_runs", "d_paused", "f_running"}, 5},
		{"failed", "failed", nil, 100, 0, []string{"a_failed", "d_paused"}, 2},
		{"failed and not paused", "failed", &no, 100, 0, []string{"a_failed"}, 1},
		{"success", "success", nil, 100, 0, []string{"b_success"}, 1},
		{"running", "running", nil, 100, 0, []string{"f_running"}, 1},
		{"queued only on an older run", "queued", nil, 100, 0, []string{}, 0},
		{"not paused", "", &no, 100, 0, []string{"a_failed", "b_success", "c_no_runs", "f_running"}, 4},
		{"second page", "failed", nil, 1, 1, []string{"d_paused"}, 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, total, err := f.repo.ListDagsFiltered(ctx, f.tenant, tc.state, tc.paused, tc.limit, tc.offset)
			if err != nil {
				t.Fatalf("ListDagsFiltered: %v", err)
			}
			ids := make([]string, 0, len(got))
			for _, d := range got {
				ids = append(ids, d.DagID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") || total != tc.total {
				t.Errorf("got %v (total %d), want %v (total %d)", ids, total, tc.want, tc.total)
			}
		})
	}
}

// TestDagStatsLatestRunSemantics pins the dashboard counters: one DAG per
// newest-run state, DAGs without runs uncounted, other tenants excluded. The
// latest-run counters include soft-deleted DAGs (e_inactive) today, unlike the
// Active counter; this test records that rather than endorsing it.
func TestDagStatsLatestRunSemantics(t *testing.T) {
	f, ctx := seedLatestRunFixture(t)
	stats, err := f.repo.DagStats(ctx, f.tenant)
	if err != nil {
		t.Fatalf("DagStats: %v", err)
	}
	if stats.Active != 5 || stats.Failed != 3 || stats.Running != 1 || stats.Queued != 0 {
		t.Errorf("DagStats = %+v, want Active 5, Failed 3, Running 1, Queued 0", stats)
	}
}

// TestLatestRunQueriesReadOneRunPerDag guards the cost of the latest-run-state
// queries: they must read at most one dag_runs row per DAG of the tenant, not
// every run in the table. The old DISTINCT ON form walked all runs of every
// tenant, which took 320 to 450 ms at 1M runs. Sequential and bitmap scans are
// disabled so the planner's choice on a small test table cannot hide the
// difference.
func TestLatestRunQueriesReadOneRunPerDag(t *testing.T) {
	f, ctx := seedLatestRunFixture(t)
	for _, tc := range []struct {
		file, query string
		args        []any
	}{
		{"dags.sql", "ListDagsFiltered", []any{f.tid, 100, 0, nil, "failed"}},
		{"dags.sql", "CountDagsFiltered", []any{f.tid, nil, "failed"}},
		{"runs.sql", "CountDagsByLatestRunState", []any{f.tid}},
	} {
		t.Run(tc.query, func(t *testing.T) {
			sql := readNamedQuery(t, tc.file, tc.query)
			read := dagRunRowsRead(t, ctx, f.pool, sql, tc.args)
			if read > f.dags {
				t.Errorf("%s read %d dag_runs rows for a tenant with %d DAGs, want at most one per DAG", tc.query, read, f.dags)
			}
		})
	}
}

var sqlcNargRe = regexp.MustCompile(`sqlc\.n?arg\('([a-z_]+)'\)`)

// readNamedQuery returns the body of one sqlc query from queries/<file>, with
// its sqlc.narg placeholders numbered after the positional ones the way sqlc
// numbers them.
func readNamedQuery(t *testing.T, file, name string) string {
	t.Helper()
	b, err := os.ReadFile("queries/" + file)
	if err != nil {
		t.Fatal(err)
	}
	_, rest, ok := strings.Cut(string(b), "-- name: "+name+" ")
	if !ok {
		t.Fatalf("query %s not found in %s", name, file)
	}
	_, body, _ := strings.Cut(rest, "\n")
	body, _, _ = strings.Cut(body, "-- name: ")
	body = strings.TrimSuffix(strings.TrimSpace(body), ";")
	next := 1
	for i := 1; strings.Contains(body, fmt.Sprintf("$%d", i)); i++ {
		next = i + 1
	}
	index := map[string]int{}
	return sqlcNargRe.ReplaceAllStringFunc(body, func(m string) string {
		arg := sqlcNargRe.FindStringSubmatch(m)[1]
		n, seen := index[arg]
		if !seen {
			n = next
			index[arg] = n
			next++
		}
		return fmt.Sprintf("$%d", n)
	})
}

// dagRunRowsRead runs EXPLAIN ANALYZE on sql and returns how many rows its
// dag_runs scans produced in total, across every loop.
func dagRunRowsRead(t *testing.T, ctx context.Context, pool *pgxpool.Pool, sql string, args []any) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off; SET LOCAL enable_bitmapscan = off`); err != nil {
		t.Fatal(err)
	}
	var out string
	if err := tx.QueryRow(ctx, "EXPLAIN (ANALYZE, FORMAT JSON) "+sql, args...).Scan(&out); err != nil {
		t.Fatalf("explain: %v", err)
	}
	var doc []struct {
		Plan map[string]any `json:"Plan"` //nolint:tagliatelle // Postgres EXPLAIN JSON key
	}
	if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc) == 0 {
		t.Fatalf("parse plan: %v", err)
	}
	var walk func(map[string]any) int
	walk = func(n map[string]any) int {
		total := 0
		if n["Relation Name"] == "dag_runs" {
			rows, _ := n["Actual Rows"].(float64)
			loops, _ := n["Actual Loops"].(float64)
			total += int(rows * loops)
		}
		children, _ := n["Plans"].([]any)
		for _, c := range children {
			if cm, ok := c.(map[string]any); ok {
				total += walk(cm)
			}
		}
		return total
	}
	return walk(doc[0].Plan)
}
