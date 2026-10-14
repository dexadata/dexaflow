package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// CheckClearVersion answers whether a clear of the run with opts may apply,
// without writing anything: nil, or the conflict ClearTaskInstances would
// return (see checkRebindTaskSet). The API asks it before a dry run, so the
// preview reports the refusal the clear itself would meet, and before a clear
// that spans several runs, so a refusal on one clears none of them.
func (r *Repository) CheckClearVersion(ctx context.Context, tenant, dagID, runID string, opts domain.ClearOptions) error {
	dag, err := r.resolveDag(ctx, tenant, dagID)
	if err != nil {
		return err
	}
	run, err := r.q.GetDagRun(ctx, queries.GetDagRunParams{TenantID: dag.TenantID, DagID: dag.ID, RunID: runID})
	if err != nil {
		return mapNotFound(err)
	}
	return checkRebindTaskSet(ctx, r.q, dag, run, opts)
}

// checkRebindTaskSet refuses a clear that re-binds the run to the DAG's
// current version (run_on_latest_version) when that version's tasks differ
// from the run's task instances (#1702).
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
// It passes when the clear does not re-bind, when the run is already on the
// current version, when the run has no task instances yet (the scheduler
// materializes them from the version the run is bound to), and when the task
// sets match. Reads go through q, so ClearTaskInstances runs it inside its
// transaction.
func checkRebindTaskSet(ctx context.Context, q *queries.Queries, dag queries.Dag, run queries.DagRun, opts domain.ClearOptions) error {
	if !opts.ResetDagRun || !opts.RunOnLatestVersion || !dag.CurrentVersionID.Valid || dag.CurrentVersionID == run.DagVersionID {
		return nil
	}
	tis, err := q.ListTaskInstancesByRun(ctx, queries.ListTaskInstancesByRunParams{TenantID: run.TenantID, DagRunID: run.ID})
	if err != nil {
		return fmt.Errorf("listing the task instances of the run a clear re-binds: %w", err)
	}
	if len(tis) == 0 {
		return nil
	}
	version, err := q.GetDagVersionInTenant(ctx, queries.GetDagVersionInTenantParams{ID: dag.CurrentVersionID, TenantID: dag.TenantID})
	if err != nil {
		return fmt.Errorf("loading the version a clear re-binds to: %w", err)
	}
	var spec domain.DAGSpec
	if err := json.Unmarshal(version.Spec, &spec); err != nil {
		return fmt.Errorf("decoding the version a clear re-binds to: %w", err)
	}
	have := make(map[string]bool, len(tis))
	for _, ti := range tis {
		have[ti.TaskID] = true
	}
	want := make(map[string]bool, len(spec.Tasks))
	for _, t := range spec.Tasks {
		want[t.TaskID] = true
	}
	added, removed := missingFrom(want, have), missingFrom(have, want)
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	return domain.Safef(domain.ErrConflict,
		"run_on_latest_version would move run %q to the current version %q of dag %q, which %s; "+
			"a clear does not yet create or remove task instances. Clear without run_on_latest_version "+
			"to re-run the version the run was created with, or trigger a new run to run the current version",
		run.RunID, version.Version, dag.DagID, describeTaskDrift(added, removed))
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
