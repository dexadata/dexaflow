package api

import (
	"net/http"

	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ObservabilityHandler builds the handler served on the metrics listener: the
// Prometheus /metrics endpoint plus the same /healthz and /readyz the full API
// exposes (reusing the very same handlers, so the semantics are identical —
// trivial liveness, dependency-pinging readiness).
//
// Roles that do not serve the full API — the ADR 0049 scheduler role — still need
// a liveness/readiness surface for the kubelet's probes. The metrics listener runs
// in every role, so mounting health here gives a scheduler-only pod a probe target
// without exposing the API, auth, or UI. The full API keeps its own /healthz and
// /readyz on the HTTP port, so the "all" role is unchanged; this is purely
// additive on the metrics port.
//
// This handler is intentionally unauthenticated (probes carry no token) and does
// not run the API middleware chain; it is the same trust level as scraping
// /metrics, which is already public.
func ObservabilityHandler(registry *prometheus.Registry, checks map[string]HealthChecker) http.Handler {
	r := gin.New()
	r.GET("/healthz", livenessHandler)
	r.GET("/readyz", readinessHandler(checks))
	if registry != nil {
		// Every dexaflow_* family is also published under its pre-rename
		// leoflow_* name, so existing dashboards and alerts keep working.
		r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(observability.WithLegacyNames(registry), promhttp.HandlerOpts{})))
	}
	return r
}
