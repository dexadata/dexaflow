package api

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Airflow health status strings the UI renders (green / red).
const (
	healthStatusHealthy   = "healthy"
	healthStatusUnhealthy = "unhealthy"
)

// Heartbeater reports a long-running component's liveness and last heartbeat for
// the monitor health endpoint. The scheduler implements it.
type Heartbeater interface {
	Heartbeat() (healthy bool, last time.Time)
}

// componentHealth resolves a component's status string and heartbeat timestamp.
// Without a heartbeater (component not wired) it reports healthy with now, since
// in Dexaflow's single process its role is served by this very request.
func componentHealth(hb Heartbeater) (status, heartbeat string) {
	if hb == nil {
		return healthStatusHealthy, time.Now().UTC().Format(time.RFC3339)
	}
	healthy, last := hb.Heartbeat()
	status = healthStatusHealthy
	if !healthy {
		status = healthStatusUnhealthy
	}
	return status, last.UTC().Format(time.RFC3339)
}

// monitorHealthHandler implements GET /api/v2/monitor/health (Airflow
// HealthInfoResponse). The Airflow UI's home dashboard polls it to color the
// component health widgets (metadatabase, scheduler, triggerer, dag processor).
//
// The metadatabase status is a real probe of the "postgres" checker — the SAME
// check /readyz runs, connection and schema both. It has to be: this endpoint
// reads the same checks map, and when it did Ping only, the #1023 state produced
// a control plane answering `metadatabase: healthy` at HTTP 200 here while
// /readyz was 503ing, the pod was out of the Service's endpoints and the
// scheduler was failing every tick. The dashboard a user looks at to find out
// whether the database is fine must not be the one surface that says it is.
//
// The scheduler status is a real heartbeat (sched, when wired, reports its last
// loop tick — a stalled leader goes unhealthy). Dexaflow's single Go control plane
// subsumes the triggerer and DAG-processor roles (no separate Python daemons):
// triggering is folded into the scheduler and DAG "processing" is GitOps
// compile-time, so those mirror the scheduler heartbeat. See docs/ui-compatibility.md.
func monitorHealthHandler(checks map[string]HealthChecker, sched Heartbeater) gin.HandlerFunc {
	return func(c *gin.Context) {
		dbStatus := healthStatusHealthy
		if hc, ok := checks["postgres"]; ok {
			ctx, cancel := withProbeBudget(c)
			defer cancel()
			if err := checkDependency(ctx, hc); err != nil {
				// Unlike /readyz this endpoint always answers 200 (the Airflow
				// HealthInfoResponse carries the verdict in the body), so the
				// error is logged rather than returned — and, as there, never
				// echoed to the caller.
				slog.WarnContext(c.Request.Context(), "monitor health check failed", "dependency", "postgres", "error", err)
				dbStatus = healthStatusUnhealthy
			}
		}
		schedStatus, schedBeat := componentHealth(sched)
		c.JSON(http.StatusOK, gin.H{
			"metadatabase": gin.H{"status": dbStatus},
			"scheduler": gin.H{
				"status":                     schedStatus,
				"latest_scheduler_heartbeat": schedBeat,
			},
			"triggerer": gin.H{
				"status":                     schedStatus,
				"latest_triggerer_heartbeat": schedBeat,
			},
			"dag_processor": gin.H{
				"status":                         schedStatus,
				"latest_dag_processor_heartbeat": schedBeat,
			},
		})
	}
}
