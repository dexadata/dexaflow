package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// CheckClearVersion answers whether a clear of the runs with opts may apply,
// without writing anything: nil, or the conflict ClearTaskInstances would
// return for one of them (see checkRebindTaskSet), or domain.ErrNotFound when
// a run does not exist in the tenant. The API asks it for every run a request
// names before it previews or clears any, so a dry run reports the refusal the
// clear would meet and a clear over several runs is refused before the first
// one is cleared. The current version's tasks are read once for all the runs.
// The refusal of a request over several runs says how many of them differ.
func (r *Repository) CheckClearVersion(ctx context.Context, tenant, dagID string, runIDs []string, opts domain.ClearOptions) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	runs := make([]queries.DagRun, 0, len(runIDs))
	for _, runID := range runIDs {
		run, gerr := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
		if gerr != nil {
			return mapNotFound(gerr)
		}
		runs = append(runs, run)
	}
	if !rebinds(dag, opts) {
		return nil
	}
	g := taskSetGuard{q: r.q, dag: dag, versions: map[pgtype.UUID]versionTasks{}}
	var first error
	differ := 0
	for _, run := range runs {
		refused := g.check(ctx, run)
		var safe *domain.SafeError
		switch {
		case refused == nil:
			continue
		case !errors.As(refused, &safe):
			return refused
		}
		differ++
		if first == nil {
			first = refused
		}
	}
	if differ == 0 || len(runs) == 1 {
		return first
	}
	var safe *domain.SafeError
	errors.As(first, &safe)
	return domain.Safef(domain.ErrConflict, "%d of the %d runs this clear spans would change their tasks, so none is cleared. %s",
		differ, len(runs), safe.ClientMessage())
}

// rebinds reports whether a clear with opts moves its run to the DAG's
// current version.
func rebinds(dag queries.Dag, opts domain.ClearOptions) bool {
	return opts.ResetDagRun && opts.RunOnLatestVersion && dag.CurrentVersionID.Valid
}

// checkRebindTaskSet refuses a clear that re-binds the run to the DAG's
// current version (run_on_latest_version) when that version's tasks differ
// from the run's (#1702; ADR 0020 decides which version a clear re-runs).
//
// Airflow 3.2 re-binds a finished run and then calls DagRun.verify_integrity,
// which creates a task instance for every task the new version adds and marks
// REMOVED the task instances of the tasks it dropped. A clear here only resets
// the task instances the run has, and the scheduler materializes a run's task
// instances only when it has none, so the same re-bind would leave an added
// task with no task instance (the run finishes without ever running it) and a
// dropped task's task instance behind with no task to run it. Until a clear
// creates and removes task instances, it refuses that re-bind and names the
// differing tasks and the two ways out.
//
// The run's tasks are its task instances. A run that has none yet is compared
// by the tasks of the version it is bound to, because the scheduler may have
// read that version already and materialize it after the re-bind commits; a
// match then gives the run the current version's tasks whichever version the
// scheduler materializes. It passes when the clear does not re-bind, when the
// run is already on the current version, and when the task sets match. Reads
// go through q, so ClearTaskInstances runs it inside its transaction.
func checkRebindTaskSet(ctx context.Context, q *queries.Queries, dag queries.Dag, run queries.DagRun, opts domain.ClearOptions) error {
	if !rebinds(dag, opts) {
		return nil
	}
	g := taskSetGuard{q: q, dag: dag, versions: map[pgtype.UUID]versionTasks{}}
	return g.check(ctx, run)
}

// versionTasks is a version's label and the set of its task ids.
type versionTasks struct {
	label string
	tasks map[string]bool
}

// taskSetGuard compares runs with the current version of one DAG, reading
// each version's tasks once.
type taskSetGuard struct {
	q        *queries.Queries
	dag      queries.Dag
	versions map[pgtype.UUID]versionTasks
}

// check is checkRebindTaskSet for one run of g's DAG.
func (g taskSetGuard) check(ctx context.Context, run queries.DagRun) error {
	if g.dag.CurrentVersionID == run.DagVersionID {
		return nil
	}
	have, known, err := g.runTasks(ctx, run)
	if err != nil || !known {
		return err
	}
	target, err := g.version(ctx, g.dag.CurrentVersionID)
	if err != nil {
		return err
	}
	added, removed := missingFrom(target.tasks, have), missingFrom(have, target.tasks)
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	return domain.Safef(domain.ErrConflict,
		"run_on_latest_version would move run %q to the current version %q of dag %q, which %s; "+
			"a clear does not yet create or remove task instances. Clear without run_on_latest_version "+
			"to re-run the version the run was created with, or trigger a new run to run the current version",
		run.RunID, target.label, g.dag.DagID, describeTaskDrift(added, removed))
}

// runTasks is the run's task ids: its task instances, or the tasks of the
// version it is bound to when it has none yet. known is false when the run
// has neither, and then nothing is compared.
func (g taskSetGuard) runTasks(ctx context.Context, run queries.DagRun) (have map[string]bool, known bool, err error) {
	tis, lerr := g.q.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: run.TenantID, DagRunID: run.ID})
	if lerr != nil {
		return nil, false, fmt.Errorf("listing the task instances of the run a clear re-binds: %w", lerr)
	}
	if len(tis) > 0 {
		have = make(map[string]bool, len(tis))
		for _, ti := range tis {
			have[ti.TaskID] = true
		}
		return have, true, nil
	}
	if !run.DagVersionID.Valid {
		return nil, false, nil
	}
	own, err := g.version(ctx, run.DagVersionID)
	if err != nil {
		return nil, false, err
	}
	return own.tasks, true, nil
}

// version reads a version of g's DAG once.
func (g taskSetGuard) version(ctx context.Context, id pgtype.UUID) (versionTasks, error) {
	if v, ok := g.versions[id]; ok {
		return v, nil
	}
	row, err := g.q.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: id, TenantID: g.dag.TenantID})
	if err != nil {
		return versionTasks{}, fmt.Errorf("loading a version a clear compares: %w", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(row.Spec, &spec); err != nil {
		return versionTasks{}, fmt.Errorf("decoding a version a clear compares: %w", err)
	}
	v := versionTasks{label: row.Version, tasks: make(map[string]bool, len(spec.Tasks))}
	for _, t := range spec.Tasks {
		v.tasks[t.TaskID] = true
	}
	g.versions[id] = v
	return v, nil
}

// missingFrom lists, sorted, the keys of from that other lacks.
func missingFrom(from, other map[string]bool) []string {
	out := make([]string, 0)
	for id := range from {
		if !other[id] {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return out
}

// describeTaskDrift phrases the added and removed task ids for the refusal.
func describeTaskDrift(added, removed []string) string {
	parts := make([]string, 0, 2)
	if len(added) > 0 {
		parts = append(parts, fmt.Sprintf("adds tasks [%s]", strings.Join(added, " ")))
	}
	if len(removed) > 0 {
		parts = append(parts, fmt.Sprintf("removes tasks [%s]", strings.Join(removed, " ")))
	}
	return strings.Join(parts, " and ")
}
