package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// deleteDagHandler implements DELETE /api/v2/dags/{dag_id}. By default (the UI
// trash) it CLEARS the DAG's run history — deletes its runs, task instances, and
// XCom while keeping the DAG registered — because a GitOps DAG does not reload on
// its own, so destroying it would be surprising and lossy (ADR 0020). With
// ?deregister=true it removes the DAG artifact entirely (cascade). Returns 204.
func deleteDagHandler(repo DagRepository, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		dagID := c.Param("dag_id")
		var err error
		action := "dag.history.clear"
		if c.Query("deregister") == "true" {
			action = "dag.delete"
			err = repo.DeleteDag(c.Request.Context(), tenantOf(c), dagID)
		} else {
			err = repo.ClearDagHistory(c.Request.Context(), tenantOf(c), dagID)
		}
		if err != nil {
			handleRepoError(c, err)
			return
		}
		recordDagAudit(c, audit, action, "")
		c.Status(http.StatusNoContent)
	}
}

// deleteDagRunHandler implements DELETE /api/v2/dags/{dag_id}/dagRuns/{run_id},
// mirroring Airflow: it removes a single run (task instances and XCom cascade)
// and returns 204, or 404 when the run does not exist. The UI's "delete run"
// action calls this; without the route it 404'd as "API route not found".
func deleteDagRunHandler(repo DagRunRepository, audit AuditWriter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := repo.DeleteDagRun(c.Request.Context(), tenantOf(c), c.Param("dag_id"), c.Param("dag_run_id")); err != nil {
			handleRepoError(c, err)
			return
		}
		recordDagAudit(c, audit, "dagrun.delete", c.Param("dag_run_id"))
		c.Status(http.StatusNoContent)
	}
}
