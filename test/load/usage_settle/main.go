// Command usage_settle is Load Experiment 8: what the usage event trigger of
// ADR 0096 adds to the writes that finish task attempts.
//
// The trigger on task_instances (migration 055) inserts one usage_events row
// in the transaction of every write that moves a task instance into an
// attempt-ending state. This harness times those writes through the real
// SchedulerStore, with the switch off and on: a single settle (one task to
// success) and a cascade (every task of a wide run to upstream_failed in one
// statement, the shape of an upstream failure). With the switch off the
// trigger is not even called for writes into other states, and returns at once
// for these.
//
// Each case resets the tasks to running between samples, which writes nothing:
// running is not attempt-ending. The repeated settles of one attempt get a new
// revision each time, so the revision lookup runs on every sample.
//
// Usage (see test/load/README.md):
//
//	DATABASE_URL='postgres://leoflow:leoflow@localhost:5432/leoflow_dev?sslmode=disable' \
//	  go run ./test/load/usage_settle --tasks 1000 --samples 20
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "usage_settle: %v\n", err)
		os.Exit(1)
	}
}

// fixture is one run of a fresh DAG with every task running.
type fixture struct {
	sched  *storage.SchedulerStore
	repo   *storage.Repository
	dagID  string
	runID  string
	taskID []string
}

func run() error {
	var (
		dbURL   = flag.String("db", os.Getenv("DATABASE_URL"), "Postgres URL of a migrated database with a `default` tenant (falls back to $DATABASE_URL)")
		tasks   = flag.Int("tasks", 1000, "task instances in the cascade case")
		samples = flag.Int("samples", 20, "timed samples per case; the table shows the median and the best")
	)
	flag.Parse()
	if *dbURL == "" {
		return fmt.Errorf("no database URL: pass --db or set $DATABASE_URL")
	}
	if *tasks < 1 || *samples < 1 {
		return fmt.Errorf("--tasks and --samples must be at least 1")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: *dbURL})
	if err != nil {
		return fmt.Errorf("connecting to Postgres: %w", err)
	}
	defer pg.Close()
	usage := storage.NewUsageStore(pg)
	f, err := seed(ctx, storage.NewRepository(pg), storage.NewSchedulerStore(pg), *tasks)
	if err != nil {
		return err
	}
	defer f.cleanup(usage, pg)

	fmt.Printf("| Case | Switch | Median | Best |\n|---|---|---:|---:|\n")
	for _, on := range []bool{false, true} {
		if _, err := usage.SetEventsEnabled(ctx, on); err != nil {
			return fmt.Errorf("writing the switch: %w", err)
		}
		single, err := f.measure(ctx, *samples, f.taskID[:1], domain.TaskStateSuccess)
		if err != nil {
			return err
		}
		cascade, err := f.measure(ctx, *samples, f.taskID, domain.TaskStateUpstreamFailed)
		if err != nil {
			return err
		}
		label := map[bool]string{false: "off", true: "on"}[on]
		fmt.Printf("| single settle | %s | %s | %s |\n", label, median(single), best(single))
		fmt.Printf("| cascade of %d | %s | %s | %s |\n", len(f.taskID), label, median(cascade), best(cascade))
	}
	return nil
}

// cleanup leaves the switch off and removes the DAG and its events. A failure
// is reported, not fatal: the timings are already printed.
func (f *fixture) cleanup(usage *storage.UsageStore, pg *storage.Postgres) {
	ctx := context.Background()
	if _, err := usage.SetEventsEnabled(ctx, false); err != nil {
		fmt.Fprintf(os.Stderr, "usage_settle: turning the switch off: %v\n", err)
	}
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM usage_events WHERE dag_run_id = $1::uuid`, f.runID); err != nil {
		fmt.Fprintf(os.Stderr, "usage_settle: deleting the events: %v\n", err)
	}
	if err := f.repo.DeleteDag(ctx, "default", f.dagID); err != nil {
		fmt.Fprintf(os.Stderr, "usage_settle: deleting the DAG: %v\n", err)
	}
}

// seed registers a DAG of n independent tasks and leaves one run with every
// task running.
func seed(ctx context.Context, repo *storage.Repository, sched *storage.SchedulerStore, n int) (*fixture, error) {
	f := &fixture{sched: sched, repo: repo, dagID: fmt.Sprintf("load_usage_settle_%d", time.Now().UnixNano())}
	specs := make([]domain.TaskSpec, n)
	for i := range specs {
		specs[i] = domain.TaskSpec{TaskID: fmt.Sprintf("t%05d", i), Type: domain.TaskTypePython}
		f.taskID = append(f.taskID, specs[i].TaskID)
	}
	spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: f.dagID, DagVersion: "v1", Image: "img:v1", Tasks: specs}
	hash, err := spec.CanonicalHash()
	if err != nil {
		return nil, err
	}
	if _, err = repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
		return nil, fmt.Errorf("registering the DAG: %w", err)
	}
	if _, err = repo.CreateDagRun(ctx, "default", f.dagID, domain.DagRun{
		RunID: "r1", State: domain.DagRunStateRunning, RunType: "manual", LogicalDate: time.Now().UTC(),
	}); err != nil {
		return nil, fmt.Errorf("creating the run: %w", err)
	}
	runs, err := sched.ActiveRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	for _, r := range runs {
		if r.DagID == f.dagID {
			f.runID = r.RunID
		}
	}
	if f.runID == "" {
		return nil, fmt.Errorf("the run of %s is not active", f.dagID)
	}
	if err = sched.MaterializeTasks(ctx, f.runID, specs); err != nil {
		return nil, fmt.Errorf("materializing the tasks: %w", err)
	}
	if _, err = sched.ApplyTransitions(ctx, f.runID, f.taskID, domain.TaskStateNone, domain.TaskStateRunning); err != nil {
		return nil, fmt.Errorf("starting the tasks: %w", err)
	}
	return f, nil
}

// measure times moving the tasks from running to the attempt-ending state, in
// one statement, and puts them back to running after each sample.
func (f *fixture) measure(ctx context.Context, samples int, tasks []string, to domain.TaskState) ([]time.Duration, error) {
	out := make([]time.Duration, 0, samples)
	for range samples {
		start := time.Now()
		moved, err := f.sched.ApplyTransitions(ctx, f.runID, tasks, domain.TaskStateRunning, to)
		elapsed := time.Since(start)
		if err != nil {
			return nil, err
		}
		if len(moved) != len(tasks) {
			return nil, fmt.Errorf("moved %d of %d tasks to %s", len(moved), len(tasks), to)
		}
		out = append(out, elapsed)
		if _, err := f.sched.ApplyTransitions(ctx, f.runID, tasks, to, domain.TaskStateRunning); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// median returns the middle sample, rounded for the table.
func median(d []time.Duration) time.Duration {
	s := append([]time.Duration(nil), d...)
	sort.Slice(s, func(i, j int) bool { return s[i] < s[j] })
	return s[len(s)/2].Round(time.Microsecond)
}

// best returns the fastest sample, rounded for the table.
func best(d []time.Duration) time.Duration {
	b := d[0]
	for _, x := range d[1:] {
		b = min(b, x)
	}
	return b.Round(time.Microsecond)
}
