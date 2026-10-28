//go:build integration

package api

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// rollbackUIServer is the API over a real repository, with the routes a
// deploy, a rollback and the UI's DAG header and Code tab use.
func rollbackUIServer(t *testing.T) *gin.Engine {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	pg, err := storage.NewPostgres(context.Background(), config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	repo := storage.NewRepository(pg)
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{
			Dags:        repo,
			Versions:    repo,
			Specs:       repo,
			DagVersions: repo,
			DagSources:  repo,
		},
	})
}

// TestRollbackKeepsEachVersionsSourceAndDetailsIntegration registers v1 and
// v2 of a DAG, rolls back to v1, and reads what the UI reads: dagSources
// serves the source of the version it is asked for and of the newest version
// without version_number or with 0 (Airflow 3.2), and details.latest_dag_version is the
// newest version with every field of its row, while bundle_version names the
// current one.
func TestRollbackKeepsEachVersionsSourceAndDetailsIntegration(t *testing.T) {
	srv := rollbackUIServer(t)
	dagID := fmt.Sprintf("api_rbui_%d", time.Now().UnixNano())
	for _, v := range []string{"v1", "v2"} {
		spec := fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":%q,"image":"img:%s",`+
			`"source":"# source of %s\n","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID, v, v, v)
		if rec := authGet(srv, http.MethodPost, "/api/v2/dags/"+dagID+"/versions", spec); rec.Code != http.StatusCreated {
			t.Fatalf("register %s = %d (%s)", v, rec.Code, rec.Body.String())
		}
	}
	if rec := authGet(srv, http.MethodPut, "/api/v2/dags/"+dagID+"/currentVersion", `{"version_number":1}`); rec.Code != http.StatusOK {
		t.Fatalf("rollback = %d (%s)", rec.Code, rec.Body.String())
	}

	sources := []struct {
		query      string
		wantNumber int
		wantSource string
	}{
		{"?version_number=1", 1, "# source of v1\n"},
		{"?version_number=2", 2, "# source of v2\n"},
		{"", 2, "# source of v2\n"},
		{"?version_number=0", 2, "# source of v2\n"},
	}
	for _, tc := range sources {
		code, body := getDagSource(t, srv, "/api/v2/dagSources/"+dagID+tc.query)
		if code != http.StatusOK {
			t.Fatalf("dagSources%s = %d", tc.query, code)
		}
		if body.VersionNumber != tc.wantNumber || body.Content != tc.wantSource || body.DagID != dagID {
			t.Errorf("dagSources%s = %+v, want version %d with %q", tc.query, body, tc.wantNumber, tc.wantSource)
		}
	}
	if code, _ := getDagSource(t, srv, "/api/v2/dagSources/"+dagID+"?version_number=3"); code != http.StatusNotFound {
		t.Errorf("dagSources?version_number=3 = %d, want 404", code)
	}

	rec := authGet(srv, http.MethodGet, "/api/v2/dags/"+dagID+"/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("details = %d (%s)", rec.Code, rec.Body.String())
	}
	var details struct {
		BundleVersion *string        `json:"bundle_version"`
		Latest        map[string]any `json:"latest_dag_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &details); err != nil {
		t.Fatal(err)
	}
	listed := authGet(srv, http.MethodGet, "/api/v2/dags/"+dagID+"/dagVersions/2", "")
	var v2 map[string]any
	if err := json.Unmarshal(listed.Body.Bytes(), &v2); err != nil || listed.Code != http.StatusOK {
		t.Fatalf("dagVersions/2 = %d, %v (%s)", listed.Code, err, listed.Body.String())
	}
	// build_info is served by the version routes only (#1702): the embedded
	// copy is the version's row without it.
	if _, ok := v2["build_info"]; !ok {
		t.Errorf("dagVersions/2 = %v, want a build_info field", v2)
	}
	if v, ok := details.Latest["build_info"]; ok {
		t.Errorf("latest_dag_version carries build_info %v", v)
	}
	delete(v2, "build_info")
	if len(details.Latest) != len(v2) {
		t.Errorf("latest_dag_version has %d fields, dagVersions/2 %d: %v", len(details.Latest), len(v2), details.Latest)
	}
	for k, want := range v2 {
		if got := details.Latest[k]; got != want {
			t.Errorf("latest_dag_version.%s = %v, dagVersions/2 serves %v", k, got, want)
		}
	}
	if details.Latest["bundle_version"] != "v2" || details.Latest["image"] != "img:v2" || details.Latest["spec_hash"] == "" || details.Latest["is_current"] != false {
		t.Errorf("latest_dag_version = %v, want v2, complete, not current", details.Latest)
	}
	if details.BundleVersion == nil || *details.BundleVersion != "v1" {
		t.Errorf("details.bundle_version = %v, want the current v1", details.BundleVersion)
	}
}
