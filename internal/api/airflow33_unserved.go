package api

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// The Airflow 3.3 UI calls endpoints Dexaflow does not serve yet. Unrouted,
// they answered 404 "API route not found", which the UI shows as a broken
// server. They degrade instead the way every other unserved surface does
// (ADR 0018, "hide the uncovered, never break"): a write, or a read of one
// item, answers 501 with a detail the UI shows as a toast; a list read
// answers its schema's empty collection. The calls are those of the embedded
// 3.3.2 bundle; website/content/concepts/ui-compatibility.md lists them under
// the known gaps.

// notAvailable answers 501 for a feature Dexaflow does not serve yet.
func notAvailable(feature string) gin.HandlerFunc {
	return func(c *gin.Context) {
		abortNotAvailable(c, feature)
	}
}

func abortNotAvailable(c *gin.Context, feature string) {
	AbortProblem(c, http.StatusNotImplemented, "not implemented",
		feature+" is not available in Dexaflow yet")
}

const (
	featureBulkRuns           = "acting on several Dag runs at once"
	featureBulkTaskInstances  = "acting on several task instances at once"
	featureDeleteTaskInstance = "deleting a task instance"
	featureBulkVariables      = "bulk updating or deleting variables"
	featureBulkConnections    = "bulk updating or deleting connections"
	featureCreateXCom         = "creating an XCom entry"
	featureDeleteXCom         = "deleting an XCom entry"
	featureUpdateXCom         = "updating an XCom entry"
	featureReparseDAG         = "reparsing a DAG file"
	featurePartitions         = "clearing Dag run partitions"
	featureTaskGroups         = "marking a task group"
	featureStateStore         = "the state store"
	featurePartitionRuns      = "partitioned Dag runs"
	featureAssetEvents        = "clearing queued asset events"
)

// taskStateStoreAction reports whether a task instance catch-all action
// (the path after taskInstances/{task_id}/) addresses the task state store.
func taskStateStoreAction(action string) bool {
	return action == "state-store" || strings.HasPrefix(action, "state-store/")
}

// registerAirflow33Unserved mounts the graceful answers for the Airflow 3.3
// calls Dexaflow does not serve. The task state store reads and its PATCH
// share the taskInstances/{task_id} catch-alls, so their handlers answer them
// (taskInstanceActionHandler, patchTaskInstanceHandler).
func registerAirflow33Unserved(r gin.IRouter) {
	// Runs table bulk actions: "Clear Dag Runs" (with its dry run preview),
	// and "Mark Dag Runs as..." and "Delete Dag Runs" (bulk PATCH).
	r.POST("/api/v2/dags/:dag_id/clearDagRuns", notAvailable(featureBulkRuns))
	r.PATCH("/api/v2/dags/:dag_id/dagRuns", notAvailable(featureBulkRuns))
	r.POST("/api/v2/dags/:dag_id/clearPartitions", notAvailable(featurePartitions))
	r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskGroupInstances/:group_id", notAvailable(featureTaskGroups))
	r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskGroupInstances/:group_id/dry_run", notAvailable(featureTaskGroups))

	// Task instances bulk actions and single-task instance deletion.
	r.PATCH("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances", notAvailable(featureBulkTaskInstances))
	r.DELETE("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id", notAvailable(featureDeleteTaskInstance))

	// Variables and connections bulk actions.
	r.PATCH("/api/v2/variables", notAvailable(featureBulkVariables))
	r.PATCH("/api/v2/connections", notAvailable(featureBulkConnections))

	// XCom entries mutations under a task instance.
	r.POST("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/xcomEntries", notAvailable(featureCreateXCom))
	r.DELETE("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/xcomEntries/:key", notAvailable(featureDeleteXCom))

	// Reparse DAG file.
	r.PUT("/api/v2/parseDagFile/:file_token", notAvailable(featureReparseDAG))

	// Asset events and queues.
	// Airflow answers this with AssetEventCollectionResponse; the UI reads asset_events.
	r.GET("/api/v2/dags/:dag_id/dagRuns/:dag_run_id/upstreamAssetEvents", apiEmptyCollection("asset_events"))
	r.GET("/api/v2/dags/:dag_id/assets/queuedEvents", apiEmptyCollection("queued_events"))
	r.DELETE("/api/v2/dags/:dag_id/assets/queuedEvents", notAvailable(featureAssetEvents))
	r.GET("/api/v2/assets/:asset_id/queuedEvents", apiEmptyCollection("queued_events"))
	r.DELETE("/api/v2/assets/:asset_id/queuedEvents", notAvailable(featureAssetEvents))
	r.GET("/api/v2/dags/:dag_id/assets/:asset_id/queuedEvents", apiEmptyCollection("queued_events"))
	r.DELETE("/api/v2/dags/:dag_id/assets/:asset_id/queuedEvents", notAvailable(featureAssetEvents))

	r.GET("/api/v2/assets/:asset_id/state-store", apiEmptyCollection("asset_state_store"))
	r.DELETE("/api/v2/assets/:asset_id/state-store", notAvailable(featureStateStore))
	r.GET("/api/v2/assets/:asset_id/state-store/:key", notAvailable(featureStateStore))
	r.PUT("/api/v2/assets/:asset_id/state-store/:key", notAvailable(featureStateStore))
	r.DELETE("/api/v2/assets/:asset_id/state-store/:key", notAvailable(featureStateStore))

	ti := "/api/v2/dags/:dag_id/dagRuns/:dag_run_id/taskInstances/:task_id/state-store"
	r.DELETE(ti, notAvailable(featureStateStore))
	r.PUT(ti+"/:key", notAvailable(featureStateStore))
	r.DELETE(ti+"/:key", notAvailable(featureStateStore))

	r.GET("/ui/partitioned_dag_runs", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"partitioned_dag_runs": []any{}, "total": 0})
	})
	r.GET("/ui/pending_partitioned_dag_run/:dag_id", notAvailable(featurePartitionRuns))
}

// serveTaskStateStore answers a task state store read from the task instance
// catch-all: the list is empty, a single key is not available.
func serveTaskStateStore(c *gin.Context, action string) {
	if action == "state-store" {
		apiEmptyCollection("task_state_store")(c)
		return
	}
	abortNotAvailable(c, featureStateStore)
}
