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

// zeroTaskInstanceStateCount is the Airflow 3.2.1 TaskInstanceStateCount with
// every required state counter at zero.
func zeroTaskInstanceStateCount() gin.H {
	return gin.H{
		"no_status": 0, "removed": 0, "scheduled": 0, "queued": 0, "running": 0,
		"success": 0, "restarting": 0, "failed": 0, "up_for_retry": 0,
		"up_for_reschedule": 0, "upstream_failed": 0, "skipped": 0, "deferred": 0,
	}
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
}
