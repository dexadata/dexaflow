package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestMetricsNotServedOnAPIPort locks audit H2: even when a Prometheus registry
// is wired, the public HTTP/UI listener must NOT expose /metrics — scraping
// lives on the dedicated observability listener (ObservabilityHandler, the
// metrics port), so an operator can firewall it separately from the API/UI.
func TestMetricsNotServedOnAPIPort(t *testing.T) {
	r := NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Registry:      prometheus.NewRegistry(),
		DevNoAuth:     true,
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code == http.StatusOK {
		t.Errorf("/metrics must not be served on the API port (use the observability listener); got %d", w.Code)
	}
}

// TestReadyzDoesNotLeakDependencyError locks audit H2: /readyz is unauthenticated
// (probes carry no token), so a failing check must not echo the raw dependency
// error — which can carry a DSN, credentials, or internal hostnames — into the
// response body. The caller learns which dependency is unready, nothing more.
func TestReadyzDoesNotLeakDependencyError(t *testing.T) {
	const secret = "postgres://user:hunter2@db.internal:5432/leoflow"
	srv := apiStubServer(map[string]HealthChecker{
		"postgres": fakePinger{err: errors.New("dial " + secret + " connection refused")},
	})
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/readyz", http.NoBody)
	w := httptest.NewRecorder()
	srv.ServeHTTP(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("a failing readiness check must be 503, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), secret) {
		t.Errorf("/readyz leaked the raw dependency error into the response body: %s", w.Body.String())
	}
	// It should still name the failing dependency (safe, and operationally useful).
	if !strings.Contains(w.Body.String(), "postgres") {
		t.Errorf("/readyz should name the unready dependency; body = %s", w.Body.String())
	}
}
