package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeDagSources serves the versions of one DAG, newest first, with the
// source registered with each; IsCurrent marks the version runs use.
type fakeDagSources struct {
	versions []domain.DagVersion
	sources  map[int]string
	tenants  []string
}

func (f *fakeDagSources) GetDagVersionSpec(_ context.Context, tenant, dagID string, n int) (domain.DagVersion, domain.DAGSpec, error) {
	f.tenants = append(f.tenants, tenant)
	for i, v := range f.versions {
		if (n == 0 && i == 0) || v.VersionNumber == n {
			return v, domain.DAGSpec{DagID: dagID, DagVersion: v.Version, Source: f.sources[v.VersionNumber]}, nil
		}
	}
	return domain.DagVersion{}, domain.DAGSpec{}, domain.Safef(domain.ErrNotFound, "version %d of dag %q not found", n, dagID)
}

// rolledBackSources is a DAG with two versions after a rollback to v1: v2 is
// the newest, v1 the current one.
func rolledBackSources() *fakeDagSources {
	at := time.Date(2026, 10, 14, 0, 41, 33, 0, time.UTC)
	return &fakeDagSources{
		versions: []domain.DagVersion{
			{ID: "id-2", VersionNumber: 2, Version: "dev-c9a8f627f5a7", CreatedAt: at.Add(time.Minute)},
			{ID: "id-1", VersionNumber: 1, Version: "dev-eaee68235d64", CreatedAt: at, IsCurrent: true},
		},
		sources: map[int]string{1: "# lifecycle v1\n", 2: "# lifecycle v2\n"},
	}
}

func dagSourceServer(src DagSourceReader) *gin.Engine {
	return dagSourceServerAs(src, "default")
}

// dagSourceServerAs is dagSourceServer for a caller of the given tenant.
func dagSourceServerAs(src DagSourceReader, tenant string) *gin.Engine {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Server: ServerDeps{
			CORSOrigins: []string{"*"},
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: tenant, Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			DagSources: src,
		},
	})
}

type dagSourceBody struct {
	Content        string `json:"content"`
	DagID          string `json:"dag_id"`
	VersionNumber  int    `json:"version_number"`
	DagDisplayName string `json:"dag_display_name"`
}

func getDagSource(t *testing.T, srv *gin.Engine, path string) (int, dagSourceBody) {
	t.Helper()
	rec := authGet(srv, http.MethodGet, path, "")
	var body dagSourceBody
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s: %v (%s)", path, err, rec.Body.String())
		}
	}
	return rec.Code, body
}

// TestDagSourceFollowsVersionNumber: as in Airflow 3.2, version_number selects
// the version whose source is served, and without it the newest version is
// served (as with version_number=0), even when a rollback made an older
// version current.
func TestDagSourceFollowsVersionNumber(t *testing.T) {
	src := rolledBackSources()
	srv := dagSourceServerAs(src, "acme")
	cases := []struct {
		path       string
		wantNumber int
		wantSource string
	}{
		{"/api/v2/dagSources/lifecycle?version_number=2", 2, "# lifecycle v2\n"},
		{"/api/v2/dagSources/lifecycle?version_number=1", 1, "# lifecycle v1\n"},
		{"/api/v2/dagSources/lifecycle", 2, "# lifecycle v2\n"},
		// Airflow tests `if version_number:`, so 0 is the newest version too.
		{"/api/v2/dagSources/lifecycle?version_number=0", 2, "# lifecycle v2\n"},
	}
	for _, tc := range cases {
		code, body := getDagSource(t, srv, tc.path)
		if code != http.StatusOK {
			t.Fatalf("%s = %d", tc.path, code)
		}
		if body.Content != tc.wantSource || body.VersionNumber != tc.wantNumber {
			t.Errorf("%s = version %d with %q, want version %d with %q", tc.path, body.VersionNumber, body.Content, tc.wantNumber, tc.wantSource)
		}
		if body.DagID != "lifecycle" || body.DagDisplayName != "lifecycle" {
			t.Errorf("%s: dag_id %q, dag_display_name %q", tc.path, body.DagID, body.DagDisplayName)
		}
	}
	if len(src.tenants) != len(cases) {
		t.Errorf("source read %d times, want %d", len(src.tenants), len(cases))
	}
	for _, tenant := range src.tenants {
		if tenant != "acme" {
			t.Errorf("source read as tenant %q, want the caller's", tenant)
		}
	}
}

// TestDagSourceUnknownVersionIsNotFound: a version the DAG does not have is a
// 404, as in Airflow; a version_number that is not an integer is a 400.
func TestDagSourceUnknownVersionIsNotFound(t *testing.T) {
	srv := dagSourceServer(rolledBackSources())
	for _, q := range []string{"3", "-1"} {
		if code, _ := getDagSource(t, srv, "/api/v2/dagSources/lifecycle?version_number="+q); code != http.StatusNotFound {
			t.Errorf("version_number=%s = %d, want 404", q, code)
		}
	}
	if code, _ := getDagSource(t, srv, "/api/v2/dagSources/lifecycle?version_number=two"); code != http.StatusBadRequest {
		t.Errorf("version_number=two = %d, want 400", code)
	}
	if code, _ := getDagSource(t, dagSourceServer(&fakeDagSources{}), "/api/v2/dagSources/nope"); code != http.StatusNotFound {
		t.Errorf("a DAG with no version = %d, want 404", code)
	}
}
