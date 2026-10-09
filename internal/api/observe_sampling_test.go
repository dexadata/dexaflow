package api

import (
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// routeRecordingSampler records the http.route each span carried when the
// sampler decided on it.
type routeRecordingSampler struct {
	mu     sync.Mutex
	routes []string
}

func (s *routeRecordingSampler) ShouldSample(p sdktrace.SamplingParameters) sdktrace.SamplingResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, kv := range p.Attributes {
		if kv.Key == "http.route" {
			s.routes = append(s.routes, kv.Value.AsString())
		}
	}
	return sdktrace.SamplingResult{Decision: sdktrace.RecordAndSample}
}

func (s *routeRecordingSampler) Description() string { return "routeRecordingSampler" }

// TestObserveExposesRouteToSampler pins that the route is known at span start,
// so a sampler can drop probe and static spans before they are recorded.
func TestObserveExposesRouteToSampler(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sampler := &routeRecordingSampler{}
	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sampler))
	t.Cleanup(func() { _ = tp.Shutdown(t.Context()) })

	r := gin.New()
	r.Use(Observe(nil, tp.Tracer("test")))
	r.GET("/healthz", func(c *gin.Context) { c.Status(http.StatusOK) })
	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/healthz", http.NoBody))

	if len(sampler.routes) != 1 || sampler.routes[0] != "/healthz" {
		t.Fatalf("sampler saw routes %v, want [/healthz]", sampler.routes)
	}
}
