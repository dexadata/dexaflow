package api

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestDagSpecHandler: GET /api/v2/dags/{dag_id}/spec returns the compiled
// dag.json (the structured graph), including the task list — distinct from
// dagSources, which returns the dag.py text.
func TestDagSpecHandler(t *testing.T) {
	srv := structureServer(&fakeSpecReader{spec: diamondSpec()})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/spec", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	if !strings.Contains(body, `"dag_id":"etl"`) {
		t.Errorf("spec missing dag_id: %s", body)
	}
	if !strings.Contains(body, `"load"`) || !strings.Contains(body, `"transform_a"`) {
		t.Errorf("spec missing the task graph: %s", body)
	}
}

// TestDagSpecHandlerNotFound: an unknown DAG surfaces the repo's not-found.
func TestDagSpecHandlerNotFound(t *testing.T) {
	srv := structureServer(&fakeSpecReader{err: ErrNotFound})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/ghost/spec", "")
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

// TestDagVersionSpecHandler: GET /dags/{dag_id}/dagVersions/{n}/spec serves
// the spec registered with that version, current or not, so `dexaflow
// promote` can copy any version, read in the caller's tenant; a number that
// is not an integer is a 400 and an unknown version a 404 (#1708).
func TestDagVersionSpecHandler(t *testing.T) {
	sources := rolledBackSources()
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "acme", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			DagSources: sources,
		},
	})
	for _, tc := range []struct {
		number    string
		wantCode  int
		wantInfix string
	}{
		{"2", http.StatusOK, `"dag_version":"dev-c9a8f627f5a7"`},
		{"1", http.StatusOK, `"dag_version":"dev-eaee68235d64"`},
		{"9", http.StatusNotFound, ""},
		{"0", http.StatusNotFound, ""},
		{"-1", http.StatusNotFound, ""},
		{"two", http.StatusBadRequest, "version_number"},
	} {
		rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions/"+tc.number+"/spec", "")
		if rec.Code != tc.wantCode {
			t.Errorf("%s: status = %d, want %d (%s)", tc.number, rec.Code, tc.wantCode, rec.Body.String())
			continue
		}
		if !strings.Contains(rec.Body.String(), tc.wantInfix) {
			t.Errorf("%s: body %s misses %s", tc.number, rec.Body.String(), tc.wantInfix)
		}
	}
	for _, tenant := range sources.tenants {
		if tenant != "acme" {
			t.Errorf("versioned spec read in tenant %q, want the caller's (acme)", tenant)
		}
	}
	if len(sources.tenants) != 3 {
		t.Errorf("versioned reads = %d, want 3 (versions 2, 1 and 9)", len(sources.tenants))
	}
}
