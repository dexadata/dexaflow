package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
)

func scrape(t *testing.T, h http.Handler) string {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/metrics", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("/metrics = %d", rec.Code)
	}
	return rec.Body.String()
}

func widgetsRegistry() *prometheus.Registry {
	reg := prometheus.NewRegistry()
	c := prometheus.NewCounter(prometheus.CounterOpts{Name: "dexaflow_widgets_total", Help: "Widgets."})
	reg.MustRegister(c)
	c.Inc()
	return reg
}

// TestObservabilityHandlerPublishesLegacyNamesByDefault pins today's output: a
// scrape carries both the dexaflow_ and the leoflow_ name of every family.
func TestObservabilityHandlerPublishesLegacyNamesByDefault(t *testing.T) {
	body := scrape(t, ObservabilityHandler(widgetsRegistry(), nil))
	for _, name := range []string{"dexaflow_widgets_total 1", "leoflow_widgets_total 1"} {
		if !strings.Contains(body, name) {
			t.Errorf("scrape lacks %q", name)
		}
	}
}

// TestObservabilityHandlerCanDropLegacyNames pins the opt-out behind
// observability.metrics.drop_legacy_names: only the current names are served.
func TestObservabilityHandlerCanDropLegacyNames(t *testing.T) {
	body := scrape(t, ObservabilityHandler(widgetsRegistry(), nil, WithoutLegacyMetricNames()))
	if !strings.Contains(body, "dexaflow_widgets_total 1") {
		t.Errorf("scrape lacks the current name:\n%s", body)
	}
	if strings.Contains(body, "leoflow_") {
		t.Errorf("scrape still carries a leoflow_ family:\n%s", body)
	}
}
