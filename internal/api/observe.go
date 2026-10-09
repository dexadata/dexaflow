package api

import (
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

// Metrics records HTTP request metrics. observability.Metrics implements it.
type Metrics interface {
	RecordHTTPRequest(method, path string, status int, dur time.Duration)
}

// Observe wraps each request in an OTel span and records HTTP metrics (ADR
// 0010). A nil tracer falls back to the global (no-op) tracer; nil metrics are
// skipped, so the middleware is safe in tests.
func Observe(metrics Metrics, tracer trace.Tracer) gin.HandlerFunc {
	if tracer == nil {
		tracer = otel.Tracer("leoflow")
	}
	return func(c *gin.Context) {
		start := time.Now()
		route := c.FullPath()
		if route == "" {
			route = "unmatched"
		}
		// The attributes go in at start so a sampler can decide on the route.
		ctx, span := tracer.Start(c.Request.Context(), c.Request.Method+" "+route,
			trace.WithAttributes(
				attribute.String("http.method", c.Request.Method),
				attribute.String("http.route", route),
			))
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.status_code", status))
		span.End()
		if metrics != nil {
			metrics.RecordHTTPRequest(c.Request.Method, route, status, time.Since(start))
		}
	}
}
