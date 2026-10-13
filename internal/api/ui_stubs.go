package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// The /ui endpoints below back screens Dexaflow does not implement yet (dashboards,
// calendar, backfills, asset/team management). They are hidden from the curated
// menu, but the SPA may still probe them, so each returns a schema-valid *empty*
// response (the right shape, zeroed) rather than the catch-all "{}" — an array
// endpoint handed an object, or a stats object missing its counts, would crash
// the React view. Writes fall through to the NoRoute 501. See ADR 0018 and
// docs/ui-compatibility.md.

// emptyCollection renders a {total_entries:0, <field>:[]} collection envelope.
func emptyCollection(field string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"total_entries": 0, field: []any{}})
	}
}

// emptyObject renders a bare empty JSON object.
func emptyObject() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	}
}

// zeroTaskInstanceStateCount is the Airflow TaskInstanceStateCount with
// every required state counter at zero.
func zeroTaskInstanceStateCount() gin.H {
	return tiStatesObject(nil)
}

// registerUIStubs mounts graceful empty responses for unimplemented /ui screens.
func registerUIStubs(r gin.IRouter) {
	r.GET("/ui/dependencies", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"edges": []any{}, "nodes": []any{}})
	})
	r.GET("/ui/calendar/:dag_id", emptyCollection("dag_runs"))
	r.GET("/ui/backfills", emptyCollection("backfills"))
	r.GET("/ui/teams", emptyCollection("teams"))
	r.GET("/ui/connections/hook_meta", connectionHookMetaHandler())
	r.GET("/ui/next_run_assets/:dag_id", emptyObject())
	// Read by the Airflow 3.3 DAG and run pages. Dexaflow has no deadline
	// alerts, and the run duration stats panel is optional: duration null
	// hides it.
	r.GET("/ui/dags/:dag_id/deadlineAlerts", emptyCollection("deadline_alerts"))
	r.GET("/ui/dags/:dag_id/dagRuns/:dag_run_id/deadlines", emptyCollection("deadlines"))
	r.GET("/ui/dags/:dag_id/dagRuns/:dag_run_id/stats", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"duration": nil})
	})
}
