package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeHealthCheck implements HealthChecker with a configurable Ping error so
// the readiness handler can be exercised in both happy and degraded states.
type fakeHealthCheck struct{ err error }

func (f *fakeHealthCheck) Ping(context.Context) error { return f.err }

// TestReadinessHandlerSurfacesDependencyHealth pins the K8s readiness contract:
// every registered dependency is Ping'd in turn; the first failure aborts with
// 503 and includes both the dependency name and the underlying error so an
// operator can see which dep is degraded in `kubectl describe` events. All
// happy → 200 "ready". This guards the chart's readinessProbe wiring against a
// regression where a flaky dep silently passes traffic through.
func TestReadinessHandlerSurfacesDependencyHealth(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("all checks pass → 200 ready", func(t *testing.T) {
		r := gin.New()
		r.GET("/readyz", readinessHandler(map[string]HealthChecker{
			"postgres": &fakeHealthCheck{},
			"redis":    &fakeHealthCheck{},
		}, defaultProbeBudget))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
		if rec.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", rec.Code)
		}
		if !strings.Contains(rec.Body.String(), "ready") {
			t.Errorf("body = %q, want 'ready'", rec.Body.String())
		}
	})

	t.Run("one check fails → 503 with the dep name but NOT the raw error", func(t *testing.T) {
		r := gin.New()
		r.GET("/readyz", readinessHandler(map[string]HealthChecker{
			"postgres": &fakeHealthCheck{err: errors.New("connection refused to secret.host")},
		}, defaultProbeBudget))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
		if rec.Code != http.StatusServiceUnavailable {
			t.Fatalf("status = %d, want 503", rec.Code)
		}
		body := rec.Body.String()
		if !strings.Contains(body, "postgres") {
			t.Errorf("body missing the failed dep name 'postgres': %q", body)
		}
		// The raw dependency error must NOT leak into an unauthenticated response
		// (audit H2). It is logged server-side instead.
		if strings.Contains(body, "connection refused") || strings.Contains(body, "secret.host") {
			t.Errorf("body leaked the raw dependency error: %q", body)
		}
	})

	t.Run("no checks configured → 200 ready (trivially)", func(t *testing.T) {
		r := gin.New()
		r.GET("/readyz", readinessHandler(nil, defaultProbeBudget))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody))
		if rec.Code != http.StatusOK {
			t.Errorf("empty checks should pass, got %d", rec.Code)
		}
	})
}

// TestLivenessHandlerAlwaysOK: the liveness probe is intentionally trivial —
// only an "am I alive" signal that K8s uses to decide whether to restart the
// pod. It never reaches dependencies (a stuck postgres should NOT restart the
// pod). This locks that behavior in.
func TestLivenessHandlerAlwaysOK(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/livez", livenessHandler)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/livez", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}
