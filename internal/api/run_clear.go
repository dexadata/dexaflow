package api

import (
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// clearDagRunRequest is the Airflow DAGRunClearBody. dry_run defaults to true
// and only_failed to false, as Airflow declares them. note is accepted and not
// stored (run notes are not editable yet, see patchDagRunHandler).
type clearDagRunRequest struct {
	DryRun             *bool   `json:"dry_run"`
	OnlyFailed         bool    `json:"only_failed"`
	OnlyNew            bool    `json:"only_new"`
	RunOnLatestVersion *bool   `json:"run_on_latest_version"`
	Note               *string `json:"note"`
}

// clearDagRunHandler implements POST /api/v2/dags/{dag_id}/dagRuns/{dag_run_id}/clear,
// the run page's Clear action. A dry run answers the task instances the clear
// would reset (TaskInstanceCollectionResponse); the clear itself resets them,
// re-queues the run and answers the run (DAGRunResponse). Both add
// run_on_latest_version_not_applied when the run keeps its version although
// the request asked for the current one (ADR 0086).
func clearDagRunHandler(tasks TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, specs DagSpecReader, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		var body clearDagRunRequest
		if err := c.ShouldBindJSON(&body); err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", err.Error())
			return
		}
		if body.OnlyNew {
			AbortProblem(c, http.StatusBadRequest, "bad request",
				"only_new is not supported: Dexaflow cannot yet tell which tasks a newer DAG version added, "+
					"and clearing the whole run instead would reset more than was asked")
			return
		}
		ctx, tenant := c.Request.Context(), tenantOf(c)
		dagID, runID := c.Param("dag_id"), c.Param("dag_run_id")
		if _, err := runs.GetDagRun(ctx, tenant, dagID, runID); err != nil {
			handleRepoError(c, err)
			return
		}
		// Same defaults as clearTaskInstances: the run is reset and keeps the
		// version it was created with unless the caller asks for the current one.
		opts := domain.ClearOptions{ResetDagRun: true}
		if body.RunOnLatestVersion != nil {
			opts.RunOnLatestVersion = *body.RunOnLatestVersion
		}
		kept, ok := checkClearVersion(c, tasks, []string{runID}, opts)
		if !ok {
			return
		}
		scope := newClearScope(c, specs, opts, kept)
		affected := affectedTaskInstances(c, tasks, runs, versions, runID, nil, body.OnlyFailed, scope.declared(runID))
		if body.DryRun == nil || *body.DryRun {
			c.JSON(http.StatusOK, clearCollectionOf(affected, kept))
			return
		}
		if _, err := tasks.ClearTaskInstances(ctx, tenant, dagID, runID, nil, body.OnlyFailed, opts); err != nil {
			handleRepoError(c, err)
			return
		}
		for _, ti := range affected {
			recordTaskAudit(c, audit, "taskinstance.clear", ti.DagRunID, ti.TaskID, ti.TryNumber)
		}
		run, err := runs.GetDagRun(ctx, tenant, dagID, runID)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		logKeptVersions(dagID, kept)
		c.JSON(http.StatusOK, clearedRunDTO{dagRunDTO: toDagRunDTO(run), RunOnLatestVersionNotApplied: keptDTOs(kept)})
	}
}

// clearVersionKeptDTO is one run a clear asked to move to the DAG's current
// version that keeps its own, and why (ADR 0086). Dexaflow-only.
type clearVersionKeptDTO struct {
	DagRunID string `json:"dag_run_id"`
	Reason   string `json:"reason"`
}

// clearCollectionDTO is the TaskInstanceCollectionResponse a clear answers,
// plus run_on_latest_version_not_applied, an added field present only when a
// run keeps its version, so a clear that applies as asked answers exactly
// Airflow's shape.
type clearCollectionDTO struct {
	taskInstanceCollectionDTO
	RunOnLatestVersionNotApplied []clearVersionKeptDTO `json:"run_on_latest_version_not_applied,omitempty"`
}

// clearedRunDTO is the DAGRunResponse the run clear answers, with the same
// added field.
type clearedRunDTO struct {
	dagRunDTO
	RunOnLatestVersionNotApplied []clearVersionKeptDTO `json:"run_on_latest_version_not_applied,omitempty"`
}

// clearCollectionOf answers the task instances a clear affects with the runs
// that keep their version.
func clearCollectionOf(affected []taskInstanceDTO, kept []domain.ClearVersionKept) clearCollectionDTO {
	return clearCollectionDTO{
		taskInstanceCollectionDTO:    taskInstanceCollectionDTO{TaskInstances: affected, TotalEntries: len(affected)},
		RunOnLatestVersionNotApplied: keptDTOs(kept),
	}
}

// keptDTOs maps the runs that keep their version; nil when there are none,
// so the field is left out.
func keptDTOs(kept []domain.ClearVersionKept) []clearVersionKeptDTO {
	if len(kept) == 0 {
		return nil
	}
	out := make([]clearVersionKeptDTO, 0, len(kept))
	for _, k := range kept {
		out = append(out, clearVersionKeptDTO{DagRunID: k.RunID, Reason: k.Reason})
	}
	return out
}

// logKeptVersions records each run a clear left on its own version although
// the request asked for the current one.
func logKeptVersions(dagID string, kept []domain.ClearVersionKept) {
	for _, k := range kept {
		slog.Info("clear kept the run on its version", "dag_id", logSafe(dagID), "run_id", logSafe(k.RunID), "reason", k.Reason)
	}
}
