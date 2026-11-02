package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// The public /api/v2 endpoints below back Airflow features Dexaflow does not
// implement yet (tags, warnings, import errors, assets, plugins,
// human-in-the-loop). The 3.2.1 UI polls them on the DAG list and detail
// screens; a 404 surfaces as a broken detail view and console errors. Each
// returns a schema-valid empty collection so the UI degrades gracefully. None
// has a real implementation planned; website/content/concepts/ui-compatibility.md
// lists what the UI degrades and why.

// apiEmptyCollection renders a {<field>:[], total_entries:0} envelope.
func apiEmptyCollection(field string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{field: []any{}, "total_entries": 0})
	}
}

// registerAPIStubs mounts graceful empty responses for the unimplemented public
// /api/v2 list endpoints the Airflow UI polls.
func registerAPIStubs(r gin.IRouter) {
	// /api/v2/dagTags is owned by registerDagTags (real when a DagRepository is
	// set, empty stub otherwise), see #1927.
	r.GET("/api/v2/dagWarnings", apiEmptyCollection("dag_warnings"))
	// /api/v2/importErrors is owned by registerImportErrors (real when an
	// ImportErrorStore is set, empty stub otherwise).
	r.GET("/api/v2/plugins/importErrors", apiEmptyCollection("import_errors"))
	r.GET("/api/v2/assets", apiEmptyCollection("assets"))
	r.GET("/api/v2/assets/events", apiEmptyCollection("asset_events"))
	// /api/v2/plugins is owned by pluginsHandler: the built-in plugin that
	// carries the API tokens screen when the token API is mounted (#1886).
	// /api/v2/pools is owned by registerUIPools: real Pro-only CRUD when the Pro
	// edition sets a PoolStore, else the same empty-collection stub (ADR 0053).
	r.GET("/api/v2/providers", apiEmptyCollection("providers"))
	r.GET("/api/v2/jobs", apiEmptyCollection("jobs"))
	// The Config screen reads {sections:[]} (not a collection envelope). Dexaflow
	// does not expose the Airflow config, so render an empty (graceful) one.
	r.GET("/api/v2/config", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"sections": []any{}})
	})
	// /api/v2/eventLogs is owned by registerUIAudit (real when an AuditLogReader
	// is set, empty stub otherwise).
	// Human-in-the-loop details, polled at the DAG-run level.
	r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/hitlDetails", apiEmptyCollection("hitl_details"))
}
