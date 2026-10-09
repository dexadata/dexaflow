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
		ctx, span := tracer.Start(c.Request.Context(), c.Request.Method+" "+route)
		span.SetAttributes(
			attribute.String("http.method", c.Request.Method),
			attribute.String("http.route", route),
		)
		c.Request = c.Request.WithContext(ctx)

		c.Next()

		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.status_code", status))
		// A refusal answered with a redirect (the trusted-issuer handoff sends a
		// refused browser to the external sign-in) is recorded under the status
		// it stands for, so the route's 4xx/5xx rate and alerts still see it and
		// it is not counted as a successful 303. The span keeps the real status
		// and carries the refusal next to it.
		outcome := status
		if refused := c.GetInt(contextKeyRefusalStatus); refused != 0 {
			outcome = refused
			span.SetAttributes(attribute.Int("dexaflow.refusal_status", refused))
		}
		span.End()
		if metrics != nil {
			metrics.RecordHTTPRequest(c.Request.Method, route, outcome, time.Since(start))
		}
	}
}
