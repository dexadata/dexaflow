package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakeHeartbeater struct {
	healthy bool
	last    time.Time
}

func (f fakeHeartbeater) Heartbeat() (bool, time.Time) { return f.healthy, f.last }

func TestMonitorHealthAllComponentsHealthy(t *testing.T) {
	rec := authGet(stubsServer(), http.MethodGet, "/api/v2/monitor/health", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/api/v2/monitor/health = %d, want 200", rec.Code)
	}
	var h struct {
		Metadatabase struct {
			Status string `json:"status"`
		} `json:"metadatabase"`
		Scheduler struct {
			Status    string `json:"status"`
			Heartbeat string `json:"latest_scheduler_heartbeat"`
		} `json:"scheduler"`
		Triggerer struct {
			Status string `json:"status"`
		} `json:"triggerer"`
		DagProcessor struct {
			Status string `json:"status"`
		} `json:"dag_processor"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
		t.Fatal(err)
	}
	if h.Metadatabase.Status != "healthy" || h.Scheduler.Status != "healthy" ||
		h.Triggerer.Status != "healthy" || h.DagProcessor.Status != "healthy" {
		t.Errorf("all components should be healthy, got %s", rec.Body.String())
	}
	if h.Scheduler.Heartbeat == "" {
		t.Errorf("scheduler heartbeat should be set")
	}
}

func TestMonitorHealthReflectsSchedulerHeartbeat(t *testing.T) {
	gin.SetMode(gin.TestMode)
	last := time.Date(2026, 5, 23, 4, 0, 0, 0, time.UTC)

	serve := func(hb Heartbeater) map[string]map[string]string {
		r := gin.New()
		r.GET("/h", monitorHealthHandler(nil, hb))
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/h", http.NoBody)
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		var h map[string]map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
			t.Fatal(err)
		}
		return h
	}

	// A stalled scheduler turns scheduler/triggerer/dag_processor unhealthy.
	stalled := serve(fakeHeartbeater{healthy: false, last: last})
	for _, comp := range []string{"scheduler", "triggerer", "dag_processor"} {
		if stalled[comp]["status"] != "unhealthy" {
			t.Errorf("%s status = %q, want unhealthy", comp, stalled[comp]["status"])
		}
	}
	if stalled["scheduler"]["latest_scheduler_heartbeat"] != last.Format(time.RFC3339) {
		t.Errorf("heartbeat = %q, want %s", stalled["scheduler"]["latest_scheduler_heartbeat"], last.Format(time.RFC3339))
	}

	// A live scheduler reports healthy.
	live := serve(fakeHeartbeater{healthy: true, last: last})
	if live["scheduler"]["status"] != "healthy" {
		t.Errorf("live scheduler status = %q, want healthy", live["scheduler"]["status"])
	}
}

// TestMonitorHealthAssertsSchemaInvariant closes the adjacent call site with the
// identical #1023 bug. /api/v2/monitor/health reads the SAME checks map and did
// Ping-only, so in the exact state the issue describes — empty database, /readyz
// 503, pod out of the endpoints, scheduler failing every tick — the
// Airflow-compatible surface the UI's home dashboard renders reported
// `metadatabase: healthy` at HTTP 200. Two endpoints reading one map must not
// disagree about whether the database works.
func TestMonitorHealthAssertsSchemaInvariant(t *testing.T) {
	gin.SetMode(gin.TestMode)

	status := func(t *testing.T, pg HealthChecker) string {
		t.Helper()
		r := gin.New()
		r.GET("/h", monitorHealthHandler(map[string]HealthChecker{"postgres": pg}, nil))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/h", http.NoBody))
		var h map[string]map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &h); err != nil {
			t.Fatalf("decoding body %q: %v", rec.Body.String(), err)
		}
		return h["metadatabase"]["status"]
	}

	t.Run("a pingable database with no schema is unhealthy", func(t *testing.T) {
		pg := &fakeSchemaHealthCheck{schemaErr: fmt.Errorf("%w: schema_migrations is absent", domain.ErrSchemaNotCurrent)}
		if got := status(t, pg); got != healthStatusUnhealthy {
			t.Errorf("metadatabase = %q, want %q — /readyz 503s on this same state", got, healthStatusUnhealthy)
		}
	})

	t.Run("a current schema is healthy", func(t *testing.T) {
		if got := status(t, &fakeSchemaHealthCheck{}); got != healthStatusHealthy {
			t.Errorf("metadatabase = %q, want %q", got, healthStatusHealthy)
		}
	})

	t.Run("a dependency that fails Ping is never queried for its schema", func(t *testing.T) {
		pg := &fakeSchemaHealthCheck{pingErr: errors.New("connection refused")}
		if got := status(t, pg); got != healthStatusUnhealthy {
			t.Errorf("metadatabase = %q, want %q", got, healthStatusUnhealthy)
		}
		if pg.schemaCalls != 0 {
			t.Errorf("schema queried %d times after a failed Ping, want 0", pg.schemaCalls)
		}
	})

	t.Run("the schema call is bounded here too", func(t *testing.T) {
		// The UI polls this endpoint; an unbounded query against a wedged
		// database would hold a request goroutine for as long as it takes.
		pg := &fakeSchemaHealthCheck{}
		status(t, pg)
		if !pg.hadDeadline {
			t.Error("schema check ran with no deadline")
		}
	})
}
