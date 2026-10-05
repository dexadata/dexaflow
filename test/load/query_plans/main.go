// Command query_plans is Load Experiment 6: query plans at scale.
//
// It seeds a synthetic tenant sized like a busy installation (by default 1,000
// DAGs, 1,000,000 DAG runs, 5,000,000 task instances and 1,000,000 audit rows)
// and runs EXPLAIN (ANALYZE, BUFFERS) on the hot queries the UI, the API and
// the scheduler's maintenance loops issue. Each case reports the median, best
// and worst execution time over several runs, the FK trigger time it caused and
// the tables it read with a sequential scan, as a Markdown table ready for
// test/load/BASELINE.md.
//
// The SQL is read from internal/storage/queries at run time, so the experiment
// always measures the queries that ship. Every EXPLAIN runs inside a
// transaction that is rolled back, so mutating cases (DeleteDag) leave the
// dataset intact.
//
// The dataset belongs to a dedicated tenant and is kept between runs, since
// seeding takes minutes. --reseed rebuilds it and --drop removes it. Point the
// experiment at a throwaway database: seeding adds about 2 GB.
//
// Usage (see test/load/README.md for the full runbook):
//
//	DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow?sslmode=disable' \
//	  go run ./test/load/query_plans
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "query_plans: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	dbURL, queryDir, only string
	repeat                int
	plans, reseed, drop   bool
	data                  dataset
}

func parseFlags() (options, error) {
	var o options
	flag.StringVar(&o.dbURL, "db", firstEnv("DATABASE_URL", "DEXAFLOW_DATABASE_URL", "LEOFLOW_DATABASE_URL"), "Postgres URL of a migrated database (falls back to $DATABASE_URL, $DEXAFLOW_DATABASE_URL, $LEOFLOW_DATABASE_URL)")
	flag.StringVar(&o.queryDir, "queries", "internal/storage/queries", "directory holding the sqlc query files")
	flag.IntVar(&o.repeat, "repeat", 5, "EXPLAIN ANALYZE runs per case; the first warms the cache and is discarded")
	flag.StringVar(&o.only, "only", "", "comma-separated case names to run (default: all)")
	flag.BoolVar(&o.plans, "plans", false, "print the text plan of each case after the table")
	flag.BoolVar(&o.reseed, "reseed", false, "drop the existing dataset and seed a new one")
	flag.BoolVar(&o.drop, "drop", false, "drop the dataset and exit")
	flag.IntVar(&o.data.dags, "dags", 1000, "DAGs to seed")
	flag.IntVar(&o.data.runsPerDag, "runs-per-dag", 1000, "DAG runs per DAG")
	flag.IntVar(&o.data.tasksPerRun, "tasks-per-run", 5, "task instances per DAG run")
	flag.IntVar(&o.data.auditRows, "audit", 1000000, "audit_log rows to seed")
	flag.IntVar(&o.data.versions, "versions", 51, "versions of the DAG deleted by the DeleteDag case")
	flag.IntVar(&o.data.stagingRows, "staging", 200, "active staging volumes to seed")
	flag.IntVar(&o.data.runningEvery, "running-every", 7, "every Nth DAG has a running newest run")
	flag.Parse()
	if o.dbURL == "" {
		return o, fmt.Errorf("no database: pass --db or set DATABASE_URL")
	}
	if o.repeat < 2 {
		return o, fmt.Errorf("--repeat must be at least 2 (the first run is a discarded warm up)")
	}
	return o, nil
}

func run() error {
	o, err := parseFlags()
	if err != nil {
		return err
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, o.dbURL)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()

	tenant, err := prepareDataset(ctx, pool, o)
	if err != nil || tenant == "" {
		return err
	}
	queries, err := loadQueries(o.queryDir)
	if err != nil {
		return err
	}
	if sizeErr := printDatasetSize(ctx, pool, tenant); sizeErr != nil {
		return sizeErr
	}
	results, err := runCases(ctx, pool, tenant, queries, o)
	if err != nil {
		return err
	}
	printTable(results, o.repeat-1)
	if o.plans {
		for _, r := range results {
			fmt.Printf("\n### %s\n\n```\n%s\n```\n", r.name, r.textPlan)
		}
	}
	return nil
}

// prepareDataset reuses, rebuilds or drops the experiment tenant as the flags
// ask and returns its id, or "" after --drop.
func prepareDataset(ctx context.Context, pool *pgxpool.Pool, o options) (string, error) {
	tenant, err := findTenant(ctx, pool)
	if err != nil {
		return "", fmt.Errorf("look up tenant: %w", err)
	}
	if tenant != "" && (o.reseed || o.drop) {
		if err := drop(ctx, pool, tenant); err != nil {
			return "", err
		}
		tenant = ""
	}
	if o.drop {
		return "", nil
	}
	if tenant != "" {
		slog.Info("reusing the existing dataset (pass --reseed to rebuild it)", "tenant", tenantName)
		return tenant, nil
	}
	return seed(ctx, pool, o.data)
}

func runCases(ctx context.Context, pool *pgxpool.Pool, tenant string, queries map[string]string, o options) ([]result, error) {
	selected := map[string]bool{}
	for _, n := range strings.Split(o.only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			selected[n] = true
		}
	}
	var results []result
	for _, c := range cases(tenant) {
		if len(selected) > 0 && !selected[c.name] {
			continue
		}
		sql, ok := queries[c.query]
		if !ok {
			return nil, fmt.Errorf("case %s: query %s not found in %s", c.name, c.query, o.queryDir)
		}
		r, err := measure(ctx, pool, c, sql, o.repeat)
		if err != nil {
			return nil, fmt.Errorf("case %s: %w", c.name, err)
		}
		results = append(results, r)
	}
	return results, nil
}

// queryCase is one measured call: a sqlc query with the arguments a real
// caller would pass. args lists the positional ($N) arguments first, then one
// value per sqlc.arg/sqlc.narg name in order of first appearance.
type queryCase struct {
	name   string
	query  string
	caller string
	args   []any
}

func cases(tenant string) []queryCase {
	now := time.Now()
	weekAgo := now.Add(-7 * 24 * time.Hour)
	return []queryCase{
		{"ListDagsFiltered (state)", "ListDagsFiltered", "/ui/dags state chip", []any{tenant, 50, 0, nil, "failed"}},
		{"ListDagsFiltered", "ListDagsFiltered", "/ui/dags", []any{tenant, 50, 0, nil, nil}},
		{"CountDagsFiltered (state)", "CountDagsFiltered", "/ui/dags state chip", []any{tenant, nil, "failed"}},
		{"CountDagsByLatestRunState", "CountDagsByLatestRunState", "dashboard", []any{tenant}},
		{"CountDagRunStatesInWindow", "CountDagRunStatesInWindow", "dashboard history", []any{tenant, weekAgo, now}},
		{"CountTaskInstanceStatesInWindow", "CountTaskInstanceStatesInWindow", "dashboard history", []any{tenant, weekAgo, now}},
		{"ListActiveDagRuns", "ListActiveDagRuns", "scheduler tick", nil},
		{"ListRunningTasks", "ListRunningTasks", "pod-lost reaper", []any{60.0}},
		{"ListActiveStagingVolumes", "ListActiveStagingVolumes", "staging GC", nil},
		{"PoolSlotUsage", "PoolSlotUsage", "/api/v2/pools", []any{tenant}},
		{"CountAuditLogs (dag)", "CountAuditLogs", "audit page", []any{tenant, "qp_dag_00002"}},
		{"ListAuditLogs (dag)", "ListAuditLogs", "audit page", []any{tenant, 50, 0, "qp_dag_00002"}},
		{"DeleteDag", "DeleteDag", "DAG deletion", []any{tenant, deleteTarget}},
	}
}

type result struct {
	name, caller string
	execMs       []float64 // measured runs, sorted
	triggerMs    float64   // FK and other trigger time of the median run
	seqScans     []string
	textPlan     string
}

// measure runs EXPLAIN ANALYZE repeat times, each in its own rolled back
// transaction, and discards the first run as a cache warm up.
func measure(ctx context.Context, pool *pgxpool.Pool, c queryCase, raw string, repeat int) (result, error) {
	sql, _ := bindSqlcArgs(raw)
	if want := countPositional(sql); len(c.args) != want {
		return result{}, fmt.Errorf("query takes %d arguments, case passes %d", want, len(c.args))
	}
	r := result{name: c.name, caller: c.caller}
	var triggers []float64
	for i := 0; i < repeat; i++ {
		out, err := explain(ctx, pool, "EXPLAIN (ANALYZE, BUFFERS, FORMAT JSON) "+sql, c.args)
		if err != nil {
			return r, err
		}
		var doc []struct {
			Plan          map[string]any `json:"Plan"`           //nolint:tagliatelle // Postgres EXPLAIN JSON key
			ExecutionTime float64        `json:"Execution Time"` //nolint:tagliatelle // Postgres EXPLAIN JSON key
			Triggers      []struct {
				Time float64 `json:"Time"` //nolint:tagliatelle // Postgres EXPLAIN JSON key
			} `json:"Triggers"` //nolint:tagliatelle // Postgres EXPLAIN JSON key
		}
		if err := json.Unmarshal([]byte(out), &doc); err != nil || len(doc) == 0 {
			return r, fmt.Errorf("parse plan: %w", err)
		}
		if i == 0 {
			r.seqScans = seqScans(doc[0].Plan)
			continue
		}
		var t float64
		for _, tr := range doc[0].Triggers {
			t += tr.Time
		}
		r.execMs = append(r.execMs, doc[0].ExecutionTime)
		triggers = append(triggers, t)
	}
	sort.Float64s(r.execMs)
	sort.Float64s(triggers)
	r.triggerMs = triggers[len(triggers)/2]
	text, err := explain(ctx, pool, "EXPLAIN (ANALYZE, BUFFERS) "+sql, c.args)
	if err != nil {
		return r, err
	}
	r.textPlan = text
	return r, nil
}

// explain runs one EXPLAIN statement inside a transaction that is always rolled
// back and returns its output as one string.
func explain(ctx context.Context, pool *pgxpool.Pool, stmt string, args []any) (string, error) {
	tx, err := pool.Begin(ctx)
	if err != nil {
		return "", err
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // always rolled back; EXPLAIN ANALYZE must not keep its writes
	rows, err := tx.Query(ctx, stmt, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()
	var lines []string
	for rows.Next() {
		var line string
		if err := rows.Scan(&line); err != nil {
			return "", err
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n"), rows.Err()
}

// countPositional returns the highest $N in sql, which is the number of
// parameters the statement takes once sqlc names are bound.
func countPositional(sql string) int {
	highest := 0
	for _, m := range positionalRe.FindAllStringSubmatch(sql, -1) {
		var n int
		if _, err := fmt.Sscanf(m[1], "%d", &n); err == nil && n > highest {
			highest = n
		}
	}
	return highest
}

func printDatasetSize(ctx context.Context, pool *pgxpool.Pool, tenant string) error {
	var dags, runs, tis, audit int64
	err := pool.QueryRow(ctx, `
SELECT (SELECT count(*) FROM dags WHERE tenant_id = $1::uuid),
       (SELECT count(*) FROM dag_runs WHERE tenant_id = $1::uuid),
       (SELECT count(*) FROM task_instances WHERE tenant_id = $1::uuid),
       (SELECT count(*) FROM audit_log WHERE tenant_id = $1::uuid)`, tenant).Scan(&dags, &runs, &tis, &audit)
	if err != nil {
		return fmt.Errorf("count dataset: %w", err)
	}
	var version string
	if err := pool.QueryRow(ctx, `SHOW server_version`).Scan(&version); err != nil {
		return err
	}
	fmt.Printf("Postgres %s; dataset: %d DAGs, %d DAG runs, %d task instances, %d audit rows\n\n", version, dags, runs, tis, audit)
	return nil
}

func printTable(results []result, measured int) {
	fmt.Printf("| Query | Caller | Median ms | Best ms | Worst ms | Trigger ms | Seq scans |\n")
	fmt.Printf("|---|---|---:|---:|---:|---:|---|\n")
	for _, r := range results {
		scans := strings.Join(r.seqScans, ", ")
		if scans == "" {
			scans = "none"
		}
		fmt.Printf("| %s | %s | %.1f | %.1f | %.1f | %.1f | %s |\n",
			r.name, r.caller, r.execMs[len(r.execMs)/2], r.execMs[0], r.execMs[len(r.execMs)-1], r.triggerMs, scans)
	}
	fmt.Printf("\nTimes are Postgres execution time from EXPLAIN ANALYZE over %d runs after one warm up run. Trigger time (FK checks and cascades) is part of the execution time.\n", measured)
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}
