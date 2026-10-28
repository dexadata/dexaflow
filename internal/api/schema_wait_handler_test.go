package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func gateGet(t *testing.T, h http.Handler, method, path string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), method, path, nil))
	return rec
}

// The handler a booting process serves while it waits for its schema (ADR
// 0090 decision 1): liveness passes, so the kubelet leaves the pod up for
// diagnosis; readiness fails with the current reason; nothing else is served,
// so no write path is open before the schema is current.
func TestSchemaWaitHandler(t *testing.T) {
	reason := "schema behind v40, requires v46. Apply the migrations"
	reg := prometheus.NewRegistry()
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "dexaflow_schema_ready"})
	reg.MustRegister(g)
	h := SchemaWaitHandler(reg, func() string { return reason })

	if rec := gateGet(t, h, http.MethodGet, "/healthz"); rec.Code != http.StatusOK {
		t.Errorf("/healthz = %d, want 200 so liveness does not restart a waiting pod", rec.Code)
	}

	rec := gateGet(t, h, http.MethodGet, "/readyz")
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), reason) {
		t.Errorf("/readyz body %q does not carry the reason %q", rec.Body.String(), reason)
	}

	reason = "schema dirty at v41"
	if body := gateGet(t, h, http.MethodGet, "/readyz").Body.String(); !strings.Contains(body, reason) {
		t.Errorf("/readyz did not follow the reason change: %q", body)
	}

	if rec := gateGet(t, h, http.MethodGet, "/metrics"); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "dexaflow_schema_ready") {
		t.Errorf("/metrics = %d %q, want the registry served while waiting", rec.Code, rec.Body.String())
	}

	for _, c := range []struct{ method, path string }{
		{http.MethodPost, "/api/v2/dags/etl/dagRuns"},
		{http.MethodGet, "/api/v2/dags"},
		{http.MethodPost, "/auth/token"},
		{http.MethodGet, "/"},
	} {
		rec := gateGet(t, h, c.method, c.path)
		if rec.Code != http.StatusServiceUnavailable {
			t.Errorf("%s %s = %d while waiting for the schema, want 503", c.method, c.path, rec.Code)
		}
		if !strings.Contains(rec.Body.String(), reason) {
			t.Errorf("%s %s body %q does not say why", c.method, c.path, rec.Body.String())
		}
	}
}

// The API port's gate is built without a registry: the API listener serves no
// /metrics once the process is up, and must not while it waits either.
func TestSchemaWaitHandlerWithoutRegistryServesNoMetrics(t *testing.T) {
	h := SchemaWaitHandler(nil, func() string { return "schema absent" })
	if rec := gateGet(t, h, http.MethodGet, "/metrics"); rec.Code == http.StatusOK {
		t.Errorf("/metrics = 200 on a gate built without a registry")
	}
}
