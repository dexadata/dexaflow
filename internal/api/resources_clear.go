package api

// Clearing task instances: the UI's clear dialog and its dry run.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// clearTaskIDs accepts the Airflow UI's task_ids, where each element is either a
// task id string OR a [task_id, map_index] tuple (mapped tasks). Lite is unmapped,
// so the map index is dropped. Without this, the real UI payload (e.g.
// [["hello", -1]]) fails to bind and the clear request 400s.
type clearTaskIDs []string

// UnmarshalJSON decodes each task_ids element as a string or a [task_id, map_index] tuple.
func (t *clearTaskIDs) UnmarshalJSON(b []byte) error {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return err
	}
	out := make([]string, 0, len(raw))
	for _, el := range raw {
		var s string
		if json.Unmarshal(el, &s) == nil {
			out = append(out, s)
			continue
		}
		var tuple []json.RawMessage
		if err := json.Unmarshal(el, &tuple); err != nil || len(tuple) == 0 {
			return fmt.Errorf("task_ids element must be a string or [task_id, map_index] tuple")
		}
		var id string
		if err := json.Unmarshal(tuple[0], &id); err != nil {
			return fmt.Errorf("task_ids tuple task id: %w", err)
		}
		out = append(out, id)
	}
	*t = out
	return nil
}

type clearRequest struct {
	TaskIDs            clearTaskIDs `json:"task_ids"`
	DagRunID           string       `json:"dag_run_id"`
	OnlyFailed         *bool        `json:"only_failed"`
	OnlyRunning        *bool        `json:"only_running"`
	ResetDagRuns       *bool        `json:"reset_dag_runs"`
	RunOnLatestVersion *bool        `json:"run_on_latest_version"`
	DryRun             *bool        `json:"dry_run"`
	IncludeUpstream    bool         `json:"include_upstream"`
	IncludeDownstream  bool         `json:"include_downstream"`
	IncludePast        bool         `json:"include_past"`
	IncludeFuture      bool         `json:"include_future"`
}

func clearTaskInstancesHandler(repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, specs DagSpecReader, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body clearRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		// only_running is declared here and in the spec and is read by nothing, so
		// it has always been accepted and discarded. Refuse it rather than keep
		// discarding it, because the default change below makes silence dangerous:
		// Airflow rejects only_failed and only_running both true and defaults
		// only_failed to true, so `{"only_failed": false, "only_running": true}` is
		// the ONLY way to ask Airflow for "clear the running ones". Read here with
		// only_running dropped, that same body means "clear every task instance
		// named, successes included" - the narrowest request in the compatibility
		// target becoming the widest one here.
		//
		// Refusing is honest. Implementing it is a feature: Airflow sets RESTARTING
		// and kills the running task, so it needs a running-only reset and pod
		// termination, not a predicate change.
		if body.OnlyRunning != nil && *body.OnlyRunning {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				"only_running is not supported: Dexaflow cannot clear a running task instance. "+
					"Airflow's equivalent sets the task to RESTARTING and kills it; this server has no such path, "+
					"and honoring only_failed=false alone would clear every task instance named by the request, "+
					"including ones that succeeded. Wait for the task to settle, or name the task instances explicitly.")
			return
		}
		// Airflow 3.2.1 declares only_failed=True and dry_run=True on
		// ClearTaskInstancesBody. leoflow defaulted both the other way, so the same
		// unflagged request that previews on Airflow executed here, over every named
		// task instance including the ones that had succeeded (#1137). Compatibility
		// with that API is the declared target, and the safe direction happens to be
		// the same direction, so a nil field now means what Airflow means.
		onlyFailed := body.OnlyFailed == nil || *body.OnlyFailed
		// Expand the task set by the DAG topology when up/downstream is requested,
		// then fan the same set across the target runs (current + past/future).
		taskIDs := expandClearTasks(c, specs, body)
		targets := clearTargetRuns(c, runs, body.DagRunID, body.IncludePast, body.IncludeFuture)
		// Defaults match Apache Airflow 3.x: a run keeps the version it was created
		// with unless the caller asks for the current one. A clear then reproduces
		// the attempt it is clearing, which is what makes clearing a week-old task
		// mean anything; testing a fix is a NEW run, or an explicit
		// run_on_latest_version=true.
		opts := domain.ClearOptions{ResetDagRun: true, RunOnLatestVersion: false}
		if body.ResetDagRuns != nil {
			opts.ResetDagRun = *body.ResetDagRuns
		}
		if body.RunOnLatestVersion != nil {
			opts.RunOnLatestVersion = *body.RunOnLatestVersion
		}
		kept, ok := checkClearVersion(c, repo, targets, opts)
		if !ok {
			return
		}
		scope := newClearScope(c, specs, opts, kept)
		affected := make([]taskInstanceDTO, 0)
		for _, rid := range targets {
			affected = append(affected, affectedTaskInstances(c, repo, runs, versions, rid, taskIDs, onlyFailed, scope.declared(rid))...)
		}
		// The UI previews with dry_run=true before confirming; it expects the set of
		// affected task instances back (TaskInstanceCollectionResponse), not a count.
		if body.DryRun == nil || *body.DryRun {
			c.JSON(http.StatusOK, clearCollectionOf(affected, kept))
			return
		}
		for _, rid := range targets {
			if _, err := repo.ClearTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), rid, taskIDs, onlyFailed, opts); err != nil {
				handleRepoError(c, err)
				return
			}
		}
		for _, ti := range affected {
			recordTaskAudit(c, audit, "taskinstance.clear", ti.DagRunID, ti.TaskID, ti.TryNumber)
		}
		logKeptVersions(c.Param("dag_id"), kept)
		c.JSON(http.StatusOK, clearCollectionOf(affected, kept))
	}
}

// checkClearVersion asks the repository, once for all the target runs and
// before any is previewed or cleared, which of them keep their own version
// although the clear asks for the DAG's current one (ADR 0086). An unknown
// run is answered 404 here, so nothing is cleared. It writes the error
// response and returns false on an error; a clear that does not ask for the
// current version is not looked up.
func checkClearVersion(c *gin.Context, repo TaskInstanceRepository, targets []string, opts domain.ClearOptions) ([]domain.ClearVersionKept, bool) {
	if !opts.ResetDagRun || !opts.RunOnLatestVersion {
		return nil, true
	}
	kept, err := repo.RunsKeepingVersion(c.Request.Context(), tenantOf(c), c.Param("dag_id"), targets, opts)
	if err != nil {
		handleRepoError(c, err)
		return nil, false
	}
	return kept, true
}

// expandClearTasks expands the requested task_ids along the DAG topology when
// include_upstream/downstream is set (needs the spec); otherwise returns them
// unchanged. An empty task list (clear-the-whole-run) is left empty.
func expandClearTasks(c *gin.Context, specs DagSpecReader, body clearRequest) []string {
	seeds := []string(body.TaskIDs)
	if specs == nil || len(seeds) == 0 || (!body.IncludeUpstream && !body.IncludeDownstream) {
		return seeds
	}
	spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
	if err != nil {
		return seeds
	}
	return expandTaskIDs(spec.Tasks, seeds, body.IncludeUpstream, body.IncludeDownstream)
}

// clearTargetRuns resolves which runs a clear applies to: just the named run, or
// also the DAG's past/future runs (by logical_date) when include_past/future is
// set. Falls back to the single run if the run set cannot be listed.
func clearTargetRuns(c *gin.Context, runs DagRunRepository, currentRunID string, includePast, includeFuture bool) []string {
	if (!includePast && !includeFuture) || runs == nil {
		return []string{currentRunID}
	}
	dagID := c.Param("dag_id")
	cur, err := runs.GetDagRun(c.Request.Context(), tenantOf(c), dagID, currentRunID)
	if err != nil {
		return []string{currentRunID}
	}
	all, err := listDagRunsPage(c, runs, dagID, maxRunScan, 0)
	if err != nil {
		return []string{currentRunID}
	}
	out := []string{currentRunID}
	for _, r := range all {
		if r.RunID == currentRunID {
			continue
		}
		if (includePast && r.LogicalDate.Before(cur.LogicalDate)) || (includeFuture && r.LogicalDate.After(cur.LogicalDate)) {
			out = append(out, r.RunID)
		}
	}
	return out
}

// expandTaskIDs grows a seed set of task ids along the DAG topology:
// includeDownstream adds transitive descendants (tasks depending on a seed),
// includeUpstream adds transitive ancestors (tasks a seed depends on). Seeds are
// always included; with neither flag the seeds are returned unchanged.
func expandTaskIDs(tasks []domain.TaskSpec, seeds []string, includeUpstream, includeDownstream bool) []string {
	deps := make(map[string][]string, len(tasks))     // task -> its upstreams
	children := make(map[string][]string, len(tasks)) // task -> its downstreams
	for _, t := range tasks {
		deps[t.TaskID] = t.DependsOn
		for _, d := range t.DependsOn {
			children[d] = append(children[d], t.TaskID)
		}
	}
	result := map[string]bool{}
	var walk func(id string, adj map[string][]string)
	walk = func(id string, adj map[string][]string) {
		for _, next := range adj[id] {
			if !result[next] {
				result[next] = true
				walk(next, adj)
			}
		}
	}
	for _, s := range seeds {
		result[s] = true
		if includeUpstream {
			walk(s, deps)
		}
		if includeDownstream {
			walk(s, children)
		}
	}
	out := make([]string, 0, len(result))
	for id := range result {
		out = append(out, id)
	}
	return out
}

// clearScope tells, for each run a clear targets, which tasks the version
// the re-run executes declares, so the preview and the audit list only the
// task instances the clear resets (ADR 0086).
type clearScope struct {
	// current is the tasks of the DAG's current version when the clear asks
	// to move its runs there, nil otherwise or when that version is unknown.
	current map[string]bool
	// kept names the runs that keep their own version.
	kept map[string]bool
}

// newClearScope reads the current version's tasks when the clear with opts
// moves runs to it. kept lists the runs that keep their own version.
func newClearScope(c *gin.Context, specs DagSpecReader, opts domain.ClearOptions, kept []domain.ClearVersionKept) clearScope {
	scope := clearScope{kept: make(map[string]bool, len(kept))}
	for _, k := range kept {
		scope.kept[k.RunID] = true
	}
	if specs == nil || !opts.ResetDagRun || !opts.RunOnLatestVersion {
		return scope
	}
	spec, err := specs.GetCurrentSpec(c.Request.Context(), tenantOf(c), c.Param("dag_id"))
	if err != nil {
		slog.Warn("reading the current version a clear moves runs to", "dag_id", logSafe(c.Param("dag_id")), "error", err)
		return scope
	}
	scope.current = make(map[string]bool, len(spec.Tasks))
	for _, t := range spec.Tasks {
		scope.current[t.TaskID] = true
	}
	return scope
}

// declared returns the tasks the re-run of runID executes when the run moves
// to the current version, or nil when it keeps its own: the clear then skips
// only the task instances a past move left removed, the tasks its version
// does not declare.
func (s clearScope) declared(runID string) map[string]bool {
	if s.kept[runID] {
		return nil
	}
	return s.current
}

// affectedTaskInstances returns the task instances a clear would touch: the named
// tasks (or all when none named), restricted to failed ones when onlyFailed.
// A task the version the re-run executes does not declare is left out, since
// the clear does not reset it: outside declared when the run moves (declared
// is not nil), a removed task instance otherwise.
func affectedTaskInstances(c *gin.Context, repo TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister,
	runID string, taskIDs []string, onlyFailed bool, declared map[string]bool) []taskInstanceDTO {
	tis, _, err := repo.ListTaskInstances(c.Request.Context(), tenantOf(c), c.Param("dag_id"), runID, 1000, 0)
	if err != nil {
		return []taskInstanceDTO{}
	}
	want := make(map[string]bool, len(taskIDs))
	for _, id := range taskIDs {
		want[id] = true
	}
	logical, version := resolveRunContextFor(c, runs, versions, runID)
	out := make([]taskInstanceDTO, 0, len(tis))
	for _, ti := range tis {
		if len(want) > 0 && !want[ti.TaskID] {
			continue
		}
		if (declared != nil && !declared[ti.TaskID]) || (declared == nil && ti.State == domain.TaskStateRemoved) {
			continue
		}
		if onlyFailed && ti.State != domain.TaskStateFailed && ti.State != domain.TaskStateUpstreamFailed {
			continue
		}
		dto := toTaskInstanceDTO(ti)
		dto.LogicalDate, dto.RunAfter, dto.DagVersion = logical, logical, version
		out = append(out, dto)
	}
	return out
}
