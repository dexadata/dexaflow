package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// The public /api/v2 endpoints below back Airflow features Dexaflow does not
// implement yet (tags, warnings, import errors, assets, plugins,
// human-in-the-loop). The 3.2.1 UI polls them on the DAG list and detail
// screens; a 404 surfaces as a broken detail view and console errors. Each
// returns a schema-valid empty collection so the UI degrades gracefully. Real
// implementations are tracked per endpoint (GitHub issues #26–#32).

// apiEmptyCollection renders a {<field>:[], total_entries:0} envelope.
func apiEmptyCollection(field string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{field: []any{}, "total_entries": 0})
	}
}

// registerAPIStubs mounts graceful empty responses for the unimplemented public
// /api/v2 list endpoints the Airflow UI polls.
func registerAPIStubs(r gin.IRouter) {
	r.GET("/api/v2/dagTags", apiEmptyCollection("tags"))             // #26
	r.GET("/api/v2/dagWarnings", apiEmptyCollection("dag_warnings")) // #27
	// /api/v2/importErrors is owned by registerImportErrors (real when an
	// ImportErrorStore is set, empty stub otherwise) — see #28.
	r.GET("/api/v2/plugins/importErrors", apiEmptyCollection("import_errors")) // #28
	r.GET("/api/v2/assets", apiEmptyCollection("assets"))                      // #29
	r.GET("/api/v2/assets/events", apiEmptyCollection("asset_events"))         // #29
	r.GET("/api/v2/plugins", apiEmptyCollection("plugins"))                    // #30
	// /api/v2/pools is owned by registerUIPools: real Pro-only CRUD when the Pro
	// edition sets a PoolStore, else the same empty-collection stub (ADR 0053, #31).
	r.GET("/api/v2/providers", apiEmptyCollection("providers")) // #30
	r.GET("/api/v2/jobs", apiEmptyCollection("jobs"))           // #30
	r.GET("/api/v2/backfills", apiEmptyCollection("backfills")) // Backfills screen
	// The connection form's "create default connections" action: the SPA POSTs
	// here when the Connections area opens and its generated client handles only
	// 401/403 — an unhandled 404 crashed the React view, so the connector config
	// page "wouldn't open". Dexaflow seeds no legacy default connections, so this
	// is a no-op that returns the empty envelope the form reads (`.connections`).
	r.POST("/api/v2/connections/defaults", apiEmptyCollection("connections"))
	// The Config screen reads {sections:[]} (not a collection envelope). Dexaflow
	// does not expose the Airflow config, so render an empty (graceful) one.
	r.GET("/api/v2/config", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"sections": []any{}})
	})
	// /api/v2/eventLogs is owned by registerUIAudit (real when an AuditLogReader
	// is set, empty stub otherwise) — see #37.
	// Human-in-the-loop details, polled at the DAG-run level (#32).
	r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/hitlDetails", apiEmptyCollection("hitl_details"))
}
