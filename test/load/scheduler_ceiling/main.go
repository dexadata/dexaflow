// Command scheduler_ceiling is Load Experiment 1: the scheduler ceiling.
//
// It measures how the leader scheduler's per-tick wall-time scales with the
// number of active runs N, so we can find where a tick's cost approaches the
// loop interval (~1s by default). The leader loop calls Scheduler.Step once per
// tick; Step's dominant read is Store.ActiveRuns, which issues 1 + 2N DB queries
// (one list, then GetDagVersionByID + ListTaskInstancesByRun per run) plus N
// spec unmarshals, and then advances every run serially. This harness drives the
// REAL scheduler over a REAL Postgres so those costs are measured, not modeled.
//
// It boots no cluster and no executor: it seeds N synthetic DAGs, each with one
// run pinned in `running` with a `running` task instance. Such a run stays in the
// active set across ticks and Step's advance is a cheap no-op for it (nothing to
// dispatch, nothing to finalize) — so what we time is the steady-state read +
// per-run advance cost, which is exactly Experiment 1's target.
//
// It is harness-only: it imports the storage/scheduler packages the same way the
// integration tests do and touches no product code path.
//
// Usage (see test/load/README.md for the full runbook):
//
//	# 1. bring up Postgres and migrate + seed the `default` tenant
//	docker compose -f docker-compose.dev.yaml up -d
//	go run ./cmd/dexaflow db reset --yes
//	# 2. run the experiment
//	DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable' \
//	  go run ./test/load/scheduler_ceiling --n 50,200,500,1000 --window 5s
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/scheduler"
	"github.com/dexadata/dexaflow/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "scheduler_ceiling: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		dbURL       = flag.String("db", firstEnv("DATABASE_URL", "LEOFLOW_DATABASE_URL"), "Postgres URL of a migrated database with a `default` tenant (falls back to $DATABASE_URL then $LEOFLOW_DATABASE_URL)")
		nCSVFlag    = flag.String("n", "50,200,500,1000", "comma-separated active-run counts to measure, e.g. 50,200,500,1000")
		window      = flag.Duration("window", 5*time.Second, "measurement window per N: Step is called back-to-back until it elapses")
		tasksPerRun = flag.Int("tasks", 1, "task instances per synthetic run (all pinned `running`)")
		warmup      = flag.Int("warmup", 3, "warm-up ticks discarded before timing each N")
		keep        = flag.Bool("keep", false, "do not delete the synthetic DAGs/runs on exit (default cleans up)")
	)
	flag.Parse()

	if *dbURL == "" {
		return fmt.Errorf("no database URL: pass --db or set $DATABASE_URL (see test/load/README.md)")
	}
	targets, err := parseNs(*nCSVFlag)
	if err != nil {
		return err
	}

	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: *dbURL})
	if err != nil {
		return fmt.Errorf("connecting to Postgres (is it up and migrated? `leoflow db reset --yes`): %w", err)
	}
	defer pg.Close()

	repo := storage.NewRepository(pg)
	store := storage.NewSchedulerStore(pg)

	// Errors/warnings from the scheduler go to stderr; its INFO chatter is muted
	// so the results table is the only thing on stdout.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))

	reg := prometheus.NewRegistry()
	metrics := observability.NewMetrics(reg)

	sched := scheduler.NewScheduler(store, logger, time.Second)
	sched.SetRecorder(metrics) // captures step-downs; scraped per N below
	sched.SetLeading(true)     // Step is a no-op for a follower
	// No maintenance loop runs here, so no reaper ever touches our synthetic runs
	// and the active set stays fixed at N for the measurement window (reaping
	// lives in the server's leader maintenance loop, not in the scheduler tick).
	// No dispatcher is wired: a `running` task is never re-dispatched, so the
	// scheduler advances state only and launches nothing.

	prefix := fmt.Sprintf("loadtest%dx%d", os.Getpid(), time.Now().UnixNano())
	if !*keep {
		defer cleanup(ctx, pg, prefix, logger)
	}

	fmt.Printf("# Experiment 1: scheduler ceiling\n")
	fmt.Printf("# db=%s window=%s tasks/run=%d warmup=%d dagPrefix=%s\n",
		redactURL(*dbURL), *window, *tasksPerRun, *warmup, prefix)
	fmt.Printf("# NOTE: the product does not yet Observe leoflow_scheduler_loop_duration_seconds\n")
	fmt.Printf("#       inside the loop, so this harness times Step() directly (see README).\n\n")

	seeded := 0
	var rows []result
	for _, n := range targets {
		// Incremental seeding: nList is ascending, so we only add the delta and
		// the cumulative active set is exactly N when we measure.
		if err := seedRuns(ctx, repo, store, prefix, seeded, n-seeded, *tasksPerRun); err != nil {
			return fmt.Errorf("seeding up to N=%d: %w", n, err)
		}
		seeded = n

		r, err := measure(ctx, sched, metrics, reg, n, *window, *warmup)
		if err != nil {
			return fmt.Errorf("measuring N=%d: %w", n, err)
		}
		rows = append(rows, r)
		fmt.Printf("  measured N=%-5d ticks=%-5d p50=%-9s p95=%-9s\n",
			r.n, r.ticks, fmtDur(r.p50), fmtDur(r.p95))
	}

	printTable(rows, *window)
	return nil
}

// seedRuns registers `count` synthetic DAGs (ids prefixed with `prefix`, numbered
// from `startIdx`) and, for each, creates one run pinned in `running` with
// `tasksPerRun` task instances in the `running` state. Such a run stays in the
// active set and makes Step's advance a no-op — steady state for measuring the
// per-tick read cost.
func seedRuns(ctx context.Context, repo *storage.Repository, store *storage.SchedulerStore, prefix string, startIdx, count, tasksPerRun int) error {
	if count <= 0 {
		return nil
	}
	tasks := make([]domain.TaskSpec, tasksPerRun)
	for i := range tasks {
		tasks[i] = domain.TaskSpec{TaskID: fmt.Sprintf("t%d", i), Type: domain.TaskTypePython}
	}
	now := time.Now().UTC()

	newDAGs := make(map[string]bool, count)
	for i := 0; i < count; i++ {
		dagID := fmt.Sprintf("%s_%d", prefix, startIdx+i)
		spec := domain.DAGSpec{
			SchemaVersion: "1.0",
			DagID:         dagID,
			DagVersion:    "v1",
			Image:         "img:v1",
			// Large cap so the per-DAG max_active_runs guard never rejects a run;
			// no schedule so createDueRuns never fabricates extra runs.
			MaxActiveRuns: 1_000_000,
			Tasks:         tasks,
		}
		hash, herr := spec.CanonicalHash()
		if herr != nil {
			return fmt.Errorf("hashing spec %s: %w", dagID, herr)
		}
		created, rerr := repo.RegisterDagVersion(ctx, "default", spec, hash)
		if rerr != nil {
			return fmt.Errorf("registering %s (is the `default` tenant seeded? `leoflow db reset --yes`): %w", dagID, rerr)
		}
		if !created {
			return fmt.Errorf("registering %s: version already existed (stale data? try `leoflow db reset --yes`)", dagID)
		}
		if _, cerr := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: now,
		}); cerr != nil {
			return fmt.Errorf("creating run for %s: %w", dagID, cerr)
		}
		newDAGs[dagID] = true
	}

	// Resolve the internal run UUIDs (MaterializeTasks keys by them) via one
	// ActiveRuns read, then materialize + pin each new run's tasks `running`.
	runs, err := store.ActiveRuns(ctx)
	if err != nil {
		return fmt.Errorf("resolving run UUIDs: %w", err)
	}
	for _, r := range runs {
		if !newDAGs[r.DagID] {
			continue
		}
		if merr := store.MaterializeTasks(ctx, r.RunID, tasks); merr != nil {
			return fmt.Errorf("materializing %s: %w", r.DagID, merr)
		}
		for _, t := range tasks {
			if aerr := store.ApplyTransition(ctx, r.RunID, t.TaskID, domain.TaskStateRunning); aerr != nil {
				return fmt.Errorf("pinning %s/%s running: %w", r.DagID, t.TaskID, aerr)
			}
		}
	}
	return nil
}

// result holds one N's measured per-tick distribution.
type result struct {
	n         int
	ticks     int
	p50       time.Duration
	p95       time.Duration
	p99       time.Duration
	max       time.Duration
	mean      time.Duration
	stepDowns float64
}

// measure calls Step back-to-back for `window`, timing each call, and returns the
// per-tick distribution plus the step-down delta observed during the window.
func measure(ctx context.Context, sched *scheduler.Scheduler, metrics *observability.Metrics, reg *prometheus.Registry, n int, window time.Duration, warmup int) (result, error) {
	for i := 0; i < warmup; i++ {
		if err := sched.Step(ctx); err != nil {
			return result{}, fmt.Errorf("warm-up step: %w", err)
		}
	}

	stepDownsBefore := counterTotal(reg, "dexaflow_scheduler_step_downs_total")

	var samples []time.Duration
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		start := time.Now()
		err := sched.Step(ctx)
		d := time.Since(start)
		if err != nil {
			return result{}, fmt.Errorf("step: %w", err)
		}
		samples = append(samples, d)
		// Feed the real histogram too, so a future /metrics scrape (or a follow-up
		// that wires Observe into the loop) sees the same distribution.
		metrics.SchedulerLoopDuration.Observe(d.Seconds())
	}
	if len(samples) == 0 {
		return result{}, fmt.Errorf("no ticks completed in %s", window)
	}

	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	var sum time.Duration
	for _, d := range samples {
		sum += d
	}
	return result{
		n:         n,
		ticks:     len(samples),
		p50:       percentile(samples, 0.50),
		p95:       percentile(samples, 0.95),
		p99:       percentile(samples, 0.99),
		max:       samples[len(samples)-1],
		mean:      sum / time.Duration(len(samples)),
		stepDowns: counterTotal(reg, "dexaflow_scheduler_step_downs_total") - stepDownsBefore,
	}, nil
}

// percentile returns the p-quantile (0..1) of an ascending-sorted slice using
// nearest-rank.
func percentile(sorted []time.Duration, p float64) time.Duration {
	if len(sorted) == 0 {
		return 0
	}
	rank := int(p*float64(len(sorted)-1) + 0.5)
	if rank < 0 {
		rank = 0
	}
	if rank >= len(sorted) {
		rank = len(sorted) - 1
	}
	return sorted[rank]
}

// counterTotal sums every sample of a counter family in the registry (our
// step-down counter is labeled by reason, so we sum across reasons).
func counterTotal(reg *prometheus.Registry, name string) float64 {
	families, err := reg.Gather()
	if err != nil {
		return 0
	}
	var total float64
	for _, f := range families {
		if f.GetName() != name {
			continue
		}
		for _, m := range f.GetMetric() {
			if c := m.GetCounter(); c != nil {
				total += c.GetValue()
			}
		}
	}
	return total
}

func printTable(rows []result, window time.Duration) {
	fmt.Printf("\n%-7s %-7s %-10s %-10s %-10s %-10s %-10s %-10s\n",
		"N", "ticks", "p50", "p95", "p99", "max", "mean", "step_downs")
	fmt.Printf("%s\n", strings.Repeat("-", 78))
	for _, r := range rows {
		fmt.Printf("%-7d %-7d %-10s %-10s %-10s %-10s %-10s %-10.0f\n",
			r.n, r.ticks, fmtDur(r.p50), fmtDur(r.p95), fmtDur(r.p99),
			fmtDur(r.max), fmtDur(r.mean), r.stepDowns)
	}
	fmt.Printf("\n# The loop interval is ~1s by default. The ceiling is the N at which p95\n")
	fmt.Printf("# approaches that interval; extrapolate from the growth across rows.\n")
}

// cleanup removes every DAG (and its cascaded versions/runs/task instances) this
// run created. Best-effort: a failure is logged, not fatal — `leoflow db reset`
// is always the clean slate.
func cleanup(ctx context.Context, pg *storage.Postgres, prefix string, logger *slog.Logger) {
	like := prefix + "%"
	// Delete runs first: dag_runs.dag_version_id → dag_versions is RESTRICT, and
	// dropping the DAG cascades versions, so runs must go before their versions.
	if _, err := pg.Pool.Exec(ctx,
		`DELETE FROM dag_runs WHERE dag_id IN (SELECT id FROM dags WHERE dag_id LIKE $1)`, like); err != nil {
		logger.Warn("load cleanup: deleting dag_runs failed; run `leoflow db reset --yes`", "error", err)
	}
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM dags WHERE dag_id LIKE $1`, like); err != nil {
		logger.Warn("load cleanup: deleting dags failed; run `leoflow db reset --yes`", "error", err)
	}
}

// --- small helpers ---

func parseNs(csv string) ([]int, error) {
	fields := strings.Split(csv, ",")
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		n, err := strconv.Atoi(f)
		if err != nil || n <= 0 {
			return nil, fmt.Errorf("invalid --n value %q (want positive integers)", f)
		}
		out = append(out, n)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("--n produced no values")
	}
	// Ascending so incremental seeding grows the active set to each target.
	sort.Ints(out)
	return out, nil
}

func firstEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// redactURL hides any password in the DSN before printing it.
func redactURL(url string) string {
	at := strings.LastIndex(url, "@")
	slashes := strings.Index(url, "//")
	if at < 0 || slashes < 0 || slashes+2 > at {
		return url
	}
	creds := url[slashes+2 : at]
	if colon := strings.Index(creds, ":"); colon >= 0 {
		return url[:slashes+2] + creds[:colon] + ":***" + url[at:]
	}
	return url
}

// fmtDur renders a duration with microsecond-ish precision for the table.
func fmtDur(d time.Duration) string {
	switch {
	case d >= time.Second:
		return fmt.Sprintf("%.3fs", d.Seconds())
	case d >= time.Millisecond:
		return fmt.Sprintf("%.2fms", float64(d)/float64(time.Millisecond))
	default:
		return fmt.Sprintf("%.0fµs", float64(d)/float64(time.Microsecond))
	}
}
