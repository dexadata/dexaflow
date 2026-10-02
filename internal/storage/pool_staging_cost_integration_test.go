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
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// slotFixture is a tenant of its own with one DAG run that holds many settled
// task instances and a few active ones, plus staging volumes covering every
// shape of run_id the staging GC can meet.
type slotFixture struct {
	pg       *storage.Postgres
	tenant   string
	tid      string
	runUUID  string
	pvcByKey map[string]string
}

func seedSlotFixture(t *testing.T) (*slotFixture, context.Context) {
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
	suffix := time.Now().UnixNano()
	f := &slotFixture{pg: pg, tenant: fmt.Sprintf("slot_usage_%d", suffix), pvcByKey: map[string]string{}}
	exec := func(sql string, args ...any) {
		t.Helper()
		if _, err := pg.Pool.Exec(ctx, sql, args...); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	scan := func(dst *string, sql string, args ...any) {
		t.Helper()
		if err := pg.Pool.QueryRow(ctx, sql, args...).Scan(dst); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	scan(&f.tid, `INSERT INTO tenants (name) VALUES ($1) RETURNING id`, f.tenant)
	t.Cleanup(func() { _, _ = pg.Pool.Exec(context.Background(), `DELETE FROM tenants WHERE id = $1`, f.tid) })

	var dagUUID, versionUUID string
	scan(&dagUUID, `INSERT INTO dags (tenant_id, dag_id) VALUES ($1, 'slots') RETURNING id`, f.tid)
	scan(&versionUUID, `INSERT INTO dag_versions (dag_id, version, image_reference, spec, spec_hash)
VALUES ($1, 'v1', 'img:v1', '{}', 'h') RETURNING id`, dagUUID)
	// Enough settled runs that reading every dag_runs row is distinguishable
	// from one primary key probe per staging volume.
	for i := 0; i < 25; i++ {
		exec(`INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
VALUES ($1, $2, $3, $4, now() - make_interval(hours => $5), 'success', 'manual')`,
			f.tid, dagUUID, versionUUID, fmt.Sprintf("old_%d", i), i+1)
	}
	scan(&f.runUUID, `INSERT INTO dag_runs (tenant_id, dag_id, dag_version_id, run_id, logical_date, state, trigger)
VALUES ($1, $2, $3, 'live', now(), 'running', 'manual') RETURNING id`, f.tid, dagUUID, versionUUID)

	tis := []struct {
		state string
		pool  *string
		n     int
	}{
		{"success", nil, 30},
		{"failed", strPtr("p1"), 10},
		{"scheduled", nil, 2},
		{"queued", strPtr("p1"), 1},
		{"running", strPtr("p1"), 2},
		{"deferred", nil, 1},
	}
	for _, ti := range tis {
		for i := 0; i < ti.n; i++ {
			exec(`INSERT INTO task_instances (tenant_id, dag_run_id, task_id, state, pool, operator)
VALUES ($1, $2, $3, $4, $5, 'python')`, f.tid, f.runUUID, fmt.Sprintf("%s_%d", ti.state, i), ti.state, ti.pool)
		}
	}

	// Staging volumes keyed by: the run's canonical UUID (resolves), a legacy
	// run_id string (must not error, must not resolve), the UUID in upper case
	// (never matched dag_runs.id::text, so it must not resolve now either), and a
	// well formed UUID with no run behind it. A deleted row is never listed.
	for key, runID := range map[string]string{
		"uuid":    f.runUUID,
		"legacy":  "scheduled__2026-01-01T00:00:00+00:00",
		"upper":   strings.ToUpper(f.runUUID),
		"missing": "00000000-0000-4000-8000-000000000000",
	} {
		f.pvcByKey[key] = fmt.Sprintf("slot-staging-%s-%d", key, suffix)
		exec(`INSERT INTO staging_volumes (tenant_id, dag_id, run_id, pvc_name) VALUES ($1, 'slots', $2, $3)`,
			f.tid, runID, f.pvcByKey[key])
	}
	f.pvcByKey["deleted"] = fmt.Sprintf("slot-staging-deleted-%d", suffix)
	exec(`INSERT INTO staging_volumes (tenant_id, dag_id, run_id, pvc_name, state) VALUES ($1, 'slots', $2, $3, 'deleted')`,
		f.tid, f.runUUID, f.pvcByKey["deleted"])
	return f, ctx
}

func strPtr(s string) *string { return &s }

// TestPoolSlotUsageSemantics pins the per-pool occupancy the pools API
// reports: active states only, a NULL pool counted as default_pool, settled
// task instances ignored.
func TestPoolSlotUsageSemantics(t *testing.T) {
	f, ctx := seedSlotFixture(t)
	got, err := storage.NewRepository(f.pg).PoolSlotUsage(ctx, f.tenant)
	if err != nil {
		t.Fatalf("PoolSlotUsage: %v", err)
	}
	want := map[string]domain.PoolUsage{
		"default_pool": {Scheduled: 2, Deferred: 1},
		"p1":           {Queued: 1, Running: 2},
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("PoolSlotUsage = %v, want %v", got, want)
	}
}

// TestListActiveStagingVolumesRunIDShapes pins how the staging GC resolves a
// volume's run: only the canonical text form of an existing dag_runs.id
// resolves, and any other run_id (a legacy run_id string, an upper case UUID,
// a UUID with no run) lists with no run state instead of failing the query.
func TestListActiveStagingVolumesRunIDShapes(t *testing.T) {
	f, ctx := seedSlotFixture(t)
	vols, err := storage.NewSchedulerStore(f.pg).ListActiveStagingVolumes(ctx)
	if err != nil {
		t.Fatalf("ListActiveStagingVolumes: %v", err)
	}
	byPVC := map[string]domain.StagingVolumeState{}
	for _, v := range vols {
		byPVC[v.PVCName] = v
	}
	for key, want := range map[string]string{"uuid": "running", "legacy": "", "upper": "", "missing": ""} {
		v, ok := byPVC[f.pvcByKey[key]]
		if !ok {
			t.Errorf("%s volume not listed", key)
			continue
		}
		if v.RunState != want {
			t.Errorf("%s volume run state = %q, want %q", key, v.RunState, want)
		}
	}
	if _, ok := byPVC[f.pvcByKey["deleted"]]; ok {
		t.Error("deleted volume listed as active")
	}
}

// TestPoolSlotUsageHasAnActiveTenantIndex guards PoolSlotUsage behind
// /api/v2/pools: it must be answerable from an index holding only the
// tenant's task instances in its four states, with pool alongside, instead of
// filtering every task instance the tenant ever ran (296 ms at 5M rows).
// idx_ti_state cannot serve it because it leaves out deferred. On a test table
// this small the planner rightly ignores such an index either way, so this
// checks the index itself; the PR records the plan at 5M rows.
func TestPoolSlotUsageHasAnActiveTenantIndex(t *testing.T) {
	f, ctx := seedSlotFixture(t)
	rows, err := f.pg.Pool.Query(ctx, `
SELECT c.relname
FROM pg_index i
JOIN pg_class c ON c.oid = i.indexrelid
WHERE i.indrelid = 'task_instances'::regclass
  AND i.indisvalid
  AND i.indkey[0] = (SELECT attnum FROM pg_attribute WHERE attrelid = i.indrelid AND attname = 'tenant_id')
  AND (SELECT array_agg(a.attname::text ORDER BY a.attname) FROM pg_attribute a
       WHERE a.attrelid = i.indrelid AND a.attnum = ANY (i.indkey) AND a.attname IN ('state', 'pool'))
      = ARRAY['pool', 'state']
  AND pg_get_expr(i.indpred, i.indrelid) = $1`,
		"(state = ANY (ARRAY['scheduled'::task_state, 'queued'::task_state, 'running'::task_state, 'deferred'::task_state]))")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	if !rows.Next() {
		t.Error("no valid task_instances index leads with tenant_id, carries state and pool, and holds only scheduled, queued, running and deferred rows")
	}
}

// TestStagingVolumeRunLookupUsesThePrimaryKey guards ListActiveStagingVolumes,
// run every minute by the staging GC: it must find each volume's run by
// primary key, not compare every dag_runs.id cast to text (404 ms at 1M runs).
// The planner is left only index scans in nested loops, so the test asks
// whether that index path exists rather than which plan wins on a small table.
func TestStagingVolumeRunLookupUsesThePrimaryKey(t *testing.T) {
	f, ctx := seedSlotFixture(t)
	var staging int
	if err := f.pg.Pool.QueryRow(ctx, `SELECT count(*) FROM staging_volumes WHERE state = 'active'`).Scan(&staging); err != nil {
		t.Fatal(err)
	}
	stagingSQL := loadNamedQuery(t, "staging_volumes.sql", "ListActiveStagingVolumes")
	if read := relationRowsRead(t, ctx, f.pg.Pool, "dag_runs", stagingSQL, nil); read > staging {
		t.Errorf("ListActiveStagingVolumes read %d dag_runs rows for %d active volumes", read, staging)
	}
}

var namedArgRe = regexp.MustCompile(`sqlc\.n?arg\('?([a-z_]+)'?\)`)

// loadNamedQuery returns the body of one sqlc query from queries/<file>, with
// its sqlc.arg placeholders numbered after the positional ones as sqlc does.
func loadNamedQuery(t *testing.T, file, name string) string {
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
	for strings.Contains(body, fmt.Sprintf("$%d", next)) {
		next++
	}
	index := map[string]int{}
	return namedArgRe.ReplaceAllStringFunc(body, func(m string) string {
		arg := namedArgRe.FindStringSubmatch(m)[1]
		n, seen := index[arg]
		if !seen {
			n = next
			index[arg] = n
			next++
		}
		return fmt.Sprintf("$%d", n)
	})
}

// relationRowsRead runs EXPLAIN ANALYZE on sql with only index scans and
// plain nested loops enabled and returns how many rows its scans of relation
// touched across every loop: the rows they returned plus the rows their
// filters discarded. A disabled node type is still used when nothing else can
// answer the query, which is what makes a missing index path show up as a
// full read.
func relationRowsRead(t *testing.T, ctx context.Context, pool *pgxpool.Pool, relation, sql string, args []any) int {
	t.Helper()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SET LOCAL enable_seqscan = off; SET LOCAL enable_bitmapscan = off;
SET LOCAL enable_hashjoin = off; SET LOCAL enable_mergejoin = off; SET LOCAL enable_material = off`); err != nil {
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
		if n["Relation Name"] == relation {
			rows, _ := n["Actual Rows"].(float64)
			removed, _ := n["Rows Removed by Filter"].(float64)
			loops, _ := n["Actual Loops"].(float64)
			total += int((rows + removed) * loops)
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
