package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

type fakePinger struct{ err error }

func (f fakePinger) Ping(context.Context) error { return f.err }

func apiStubServer(checks map[string]HealthChecker) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		HealthChecks:  checks,
	})
}

func TestPublicAPIStubsReturnEmptyCollections(t *testing.T) {
	srv := apiStubServer(nil)
	cases := map[string]string{
		"/api/v2/dagTags":                         "tags",
		"/api/v2/dagWarnings":                     "dag_warnings",
		"/api/v2/importErrors":                    "import_errors",
		"/api/v2/assets":                          "assets",
		"/api/v2/assets/events":                   "asset_events",
		"/api/v2/plugins":                         "plugins",
		"/api/v2/pools":                           "pools",
		"/api/v2/dags/etl/dagRuns/r1/hitlDetails": "hitl_details",
	}
	for path, field := range cases {
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200", path, rec.Code)
			continue
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Errorf("%s: %v", path, err)
			continue
		}
		if string(body["total_entries"]) != "0" {
			t.Errorf("%s total_entries = %s", path, body["total_entries"])
		}
		if string(body[field]) != "[]" {
			t.Errorf("%s %s = %s, want []", path, field, body[field])
		}
	}
}

func TestMonitorHealthReflectsDBPing(t *testing.T) {
	// Healthy DB -> metadatabase healthy.
	rec := authGet(apiStubServer(map[string]HealthChecker{"postgres": fakePinger{}}), http.MethodGet, "/api/v2/monitor/health", "")
	var ok struct {
		Metadatabase struct{ Status string } `json:"metadatabase"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &ok)
	if ok.Metadatabase.Status != "healthy" {
		t.Errorf("healthy DB -> metadatabase %q, want healthy", ok.Metadatabase.Status)
	}
	// Failing DB -> metadatabase unhealthy (real probe, not a static mock).
	rec = authGet(apiStubServer(map[string]HealthChecker{"postgres": fakePinger{err: context.DeadlineExceeded}}), http.MethodGet, "/api/v2/monitor/health", "")
	var bad struct {
		Metadatabase struct{ Status string } `json:"metadatabase"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &bad)
	if bad.Metadatabase.Status != "unhealthy" {
		t.Errorf("failing DB -> metadatabase %q, want unhealthy", bad.Metadatabase.Status)
	}
}
