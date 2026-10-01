package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

// DashboardStatsReader backs the home dashboard widgets with real counts.
type DashboardStatsReader interface {
	DagStats(ctx context.Context, tenant string) (domain.DagStats, error)
	HistoricalMetrics(ctx context.Context, tenant string, since, until time.Time) (domain.HistoricalMetrics, error)
}

// dagRunStateOrder is the Airflow 3.2.1 DAGRunStates membership; every key is
// always present in the response so the UI's chart never sees a missing series.
var dagRunStateOrder = []string{"queued", "running", "success", "failed"}

// tiStateMap maps Leoflow task states onto the Airflow 3.2.1
// TaskInstanceState members. Members Leoflow does not model (removed,
// restarting, up_for_reschedule, deferred) are always present and zero.
var tiStateMap = map[string]string{
	"none":            "no_status",
	"scheduled":       "scheduled",
	"queued":          "queued",
	"running":         "running",
	"success":         "success",
	"failed":          "failed",
	"skipped":         "skipped",
	"upstream_failed": "upstream_failed",
	"up_for_retry":    "up_for_retry",
}

// tiStateOrder is the full Airflow 3.2.1 TaskInstanceState set, in spec order.
var tiStateOrder = []string{
	"no_status", "removed", "scheduled", "queued", "running", "success",
	"restarting", "failed", "up_for_retry", "up_for_reschedule",
	"upstream_failed", "skipped", "deferred",
}

// dagStatsHandler implements GET /ui/dashboard/dag_stats. The result is logged
// at INFO level (with tenant + the four counts) so Lima debugging — "the
// dashboard shows 0 failed but I see 2 failed DAGs" — has a server-side trail
// to compare against the DB. Lima Bug #3 / 2026-06-01.
func dagStatsHandler(reader DashboardStatsReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		tenant := tenantOf(c)
		s, err := reader.DagStats(c.Request.Context(), tenant)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		slog.Info("/ui/dashboard/dag_stats",
			"tenant", tenant,
			"active_dag_count", s.Active,
			"failed_dag_count", s.Failed,
			"running_dag_count", s.Running,
			"queued_dag_count", s.Queued,
		)
		c.JSON(http.StatusOK, gin.H{
			"active_dag_count":  s.Active,
			"failed_dag_count":  s.Failed,
			"running_dag_count": s.Running,
			"queued_dag_count":  s.Queued,
		})
	}
}

// historicalMetricsHandler implements GET /ui/dashboard/historical_metrics_data.
// start_date is required (the UI always sends it); end_date defaults to now.
func historicalMetricsHandler(reader DashboardStatsReader) gin.HandlerFunc {
	return func(c *gin.Context) {
		since, err := time.Parse(time.RFC3339, c.Query("start_date"))
		if err != nil {
			AbortProblem(c, http.StatusBadRequest, "bad request", "start_date must be an RFC3339 timestamp")
			return
		}
		until := time.Now().UTC()
		if raw := c.Query("end_date"); raw != "" {
			if parsed, perr := time.Parse(time.RFC3339, raw); perr == nil {
				until = parsed
			}
		}
		m, err := reader.HistoricalMetrics(c.Request.Context(), tenantOf(c), since, until)
		if err != nil {
			handleRepoError(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{
			"dag_run_states":       runStatesObject(m.RunStates),
			"task_instance_states": tiStatesObject(m.TIStates),
			"state_count_limit":    5000,
		})
	}
}

// runStatesObject builds the DAGRunStates object, every member present.
func runStatesObject(counts map[string]int) gin.H {
	out := gin.H{}
	for _, k := range dagRunStateOrder {
		out[k] = counts[k]
	}
	return out
}

// tiStatesObject builds the TaskInstanceState count object: all Airflow members
// present and zero-filled, populated from Leoflow states via tiStateMap.
func tiStatesObject(counts map[string]int) gin.H {
	out := gin.H{}
	for _, k := range tiStateOrder {
		out[k] = 0
	}
	for leoState, n := range counts {
		if airflowKey, ok := tiStateMap[leoState]; ok {
			out[airflowKey] = n
		}
	}
	return out
}

// registerUIDashboard mounts the home dashboard stat endpoints. With a reader
// they report real counts; without one (API-only / no DB) they fall back to
// schema-valid zeroed responses so the React dashboard still renders.
func registerUIDashboard(r gin.IRouter, reader DashboardStatsReader) {
	if reader == nil {
		r.GET("/ui/dashboard/dag_stats", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"active_dag_count": 0, "failed_dag_count": 0,
				"running_dag_count": 0, "queued_dag_count": 0,
			})
		})
		r.GET("/ui/dashboard/historical_metrics_data", func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{
				"dag_run_states":       runStatesObject(nil),
				"task_instance_states": zeroTaskInstanceStateCount(),
				"state_count_limit":    0,
			})
		})
		return
	}
	r.GET("/ui/dashboard/dag_stats", dagStatsHandler(reader))
	r.GET("/ui/dashboard/historical_metrics_data", historicalMetricsHandler(reader))
}
