package api

import (
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
// re-queues the run and answers the run (DAGRunResponse). Both answer 409 when
// run_on_latest_version would move the run to a version with other tasks.
func clearDagRunHandler(tasks TaskInstanceRepository, runs DagRunRepository, versions DagVersionLister, audit AuditWriter) gin.HandlerFunc {
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
		if !checkClearVersion(c, tasks, []string{runID}, opts) {
			return
		}
		affected := affectedTaskInstances(c, tasks, runs, versions, runID, nil, body.OnlyFailed)
		if body.DryRun == nil || *body.DryRun {
			c.JSON(http.StatusOK, taskInstanceCollectionDTO{TaskInstances: affected, TotalEntries: len(affected)})
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
		c.JSON(http.StatusOK, toDagRunDTO(run))
	}
}
