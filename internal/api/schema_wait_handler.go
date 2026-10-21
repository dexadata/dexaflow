package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"github.com/dexadata/dexaflow/internal/observability"
)

// SchemaWaitHandler is what a booting process serves while it waits for its
// database schema (ADR 0090 decision 1), on the API port and on the metrics
// port. /healthz answers 200, so liveness and the startup probe leave the pod
// up for diagnosis instead of restarting it into a crash loop. /readyz answers
// 503 with reason(), the fixed reason and what to do, which never carries the
// DSN or a host since the endpoint is unauthenticated. /metrics is served when
// registry is set, so the wait is visible. Every other request gets the same
// 503: no API route, and so no write path, exists before the schema is
// current.
func SchemaWaitHandler(registry *prometheus.Registry, reason func() string, opts ...ObservabilityOption) http.Handler {
	o := observabilityOptions{legacyNames: true}
	for _, opt := range opts {
		opt(&o)
	}
	notReady := func(c *gin.Context) {
		AbortProblem(c, http.StatusServiceUnavailable, "not ready", reason())
	}
	r := gin.New()
	r.GET("/healthz", livenessHandler)
	r.GET("/readyz", notReady)
	if registry != nil {
		var g prometheus.Gatherer = registry
		if o.legacyNames {
			g = observability.WithLegacyNames(registry)
		}
		r.GET("/metrics", gin.WrapH(promhttp.HandlerFor(g, promhttp.HandlerOpts{})))
	}
	r.NoRoute(notReady)
	return r
}
