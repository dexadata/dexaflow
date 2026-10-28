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

type fakeVersionLister struct{ versions []domain.DagVersion }

func (f *fakeVersionLister) ListDagVersions(context.Context, string, string) ([]domain.DagVersion, error) {
	return f.versions, nil
}

func versionsServer(dags []domain.DAG, versions []domain.DagVersion) *gin.Engine {
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
		},
		Stores: StoreDeps{
			Dags:        &fakeDagRepo{dags: dags},
			DagVersions: &fakeVersionLister{versions: versions},
		},
	})
}

func TestDagVersionsEndpoint(t *testing.T) {
	srv := versionsServer([]domain.DAG{{DagID: "etl"}},
		[]domain.DagVersion{{ID: "v-uuid", VersionNumber: 1, CreatedAt: time.Now().UTC(), Version: "v1.2.3-abc123"}})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions?order_by=-version_number", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagVersions = %d (%s)", rec.Code, rec.Body.String())
	}
	var col struct {
		DagVersions []map[string]any `json:"dag_versions"`
		Total       int              `json:"total_entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.Total != 1 || len(col.DagVersions) != 1 {
		t.Fatalf("want 1 version, got total=%d len=%d", col.Total, len(col.DagVersions))
	}
	v := col.DagVersions[0]
	// version_number drives the Graph view's structure_data fetch — must be present.
	if v["version_number"].(float64) != 1 {
		t.Errorf("version_number = %v, want 1", v["version_number"])
	}
	// The deployment label is surfaced as bundle_version (traceability).
	if v["bundle_version"] != "v1.2.3-abc123" {
		t.Errorf("bundle_version (deployment id) = %v, want v1.2.3-abc123", v["bundle_version"])
	}
	for _, f := range []string{"id", "dag_id", "bundle_name", "created_at", "dag_display_name", "bundle_version", "bundle_url"} {
		if _, ok := v[f]; !ok {
			t.Errorf("dag version missing %q", f)
		}
	}
}

func TestDagVersionByNumber(t *testing.T) {
	srv := versionsServer([]domain.DAG{{DagID: "etl"}},
		[]domain.DagVersion{
			{ID: "v1-uuid", VersionNumber: 1, CreatedAt: time.Now().UTC()},
			{ID: "v2-uuid", VersionNumber: 2, CreatedAt: time.Now().UTC()},
		})

	// The Code tab fetches a specific version by number (was a 404).
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions/2", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagVersions/2 = %d (%s)", rec.Code, rec.Body.String())
	}
	var v map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v["version_number"].(float64) != 2 || v["id"] != "v2-uuid" {
		t.Errorf("wrong version returned: %+v", v)
	}

	// A missing version number is a 404, not a panic.
	if rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions/99", ""); rec.Code != http.StatusNotFound {
		t.Errorf("missing version = %d, want 404", rec.Code)
	}
}

func TestDagDetailsPopulatesLatestDagVersion(t *testing.T) {
	// The Graph view reads details.latest_dag_version.version_number to fetch
	// version-scoped structure; a null version leaves the graph blank.
	srv := versionsServer([]domain.DAG{{DagID: "etl"}},
		[]domain.DagVersion{{ID: "v-uuid", VersionNumber: 1, CreatedAt: time.Now().UTC()}})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	var d struct {
		Latest *struct {
			VersionNumber int `json:"version_number"`
		} `json:"latest_dag_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.Latest == nil || d.Latest.VersionNumber != 1 {
		t.Errorf("latest_dag_version not populated: %s", rec.Body.String())
	}
}

// TestDagDetailsPopulatesBundleVersion: /details is the DAG half of the gate in
// ALL THREE clear dialogs —
//
//	run clear:         F = details.bundle_version,  I = run.bundle_version
//	task-group clear:  gated on details.bundle_version alone
//	single-task clear: he = details.bundle_version, ge = ti.dag_version.bundle_version
//
// so a null here hides the "Run with latest bundle version" control everywhere,
// and the operator cannot ask for the current version through any interface.
//
// It exists because the line that populates it was deletable with the whole
// internal/api suite still green: the sibling test covers toDagWithRunsDTO (the
// DAG LIST payload), which no clear dialog reads.
func TestDagDetailsPopulatesBundleVersion(t *testing.T) {
	srv := versionsServer([]domain.DAG{{DagID: "etl", CurrentVersion: "v9.9.9"}},
		[]domain.DagVersion{{ID: "v-uuid", VersionNumber: 1, CreatedAt: time.Now().UTC()}})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/details = %d (%s)", rec.Code, rec.Body.String())
	}
	var d struct {
		BundleVersion *string `json:"bundle_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	if d.BundleVersion == nil {
		t.Fatalf("details.bundle_version is null; every clear dialog hides its version control: %s", rec.Body.String())
	}
	if *d.BundleVersion != "v9.9.9" {
		t.Errorf("details.bundle_version = %q, want the DAG's current version", *d.BundleVersion)
	}
}

// TestDagVersionsExposeProvenance: next to Airflow's fields, each version
// carries its image, spec hash, current flag and the user who registered it
// (#1702), and a version with no recorded user has created_by null.
func TestDagVersionsExposeProvenance(t *testing.T) {
	srv := versionsServer([]domain.DAG{{DagID: "etl"}}, []domain.DagVersion{
		{ID: "v2", VersionNumber: 2, Version: "b", ImageReference: "img@sha256:bb", SpecHash: "h2", IsCurrent: true, CreatedBy: "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a"},
		{ID: "v1", VersionNumber: 1, Version: "a", ImageReference: "img@sha256:aa", SpecHash: "h1"},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagVersions = %d (%s)", rec.Code, rec.Body.String())
	}
	var col struct {
		DagVersions []map[string]any `json:"dag_versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	cur, old := col.DagVersions[0], col.DagVersions[1]
	if cur["image"] != "img@sha256:bb" || cur["spec_hash"] != "h2" || cur["is_current"] != true || cur["created_by"] != "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a" {
		t.Errorf("current version = %v", cur)
	}
	if v, ok := old["created_by"]; !ok || v != nil || old["is_current"] != false {
		t.Errorf("older version = %v, want created_by null and is_current false", old)
	}
}

// TestDagDetailsLatestDagVersionIsComplete: after a rollback, the newest
// version is still latest_dag_version, as in Airflow, and it carries every
// field of its row, as the dagVersions listing serves it: bundle_version,
// image, spec_hash, created_by and is_current (false, since the rollback made
// an older version current).
func TestDagDetailsLatestDagVersionIsComplete(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 41, 33, 0, time.UTC)
	srv := versionsServer([]domain.DAG{{DagID: "etl", CurrentVersion: "label-1"}}, []domain.DagVersion{
		{ID: "id-2", VersionNumber: 2, Version: "label-2", CreatedAt: at.Add(time.Minute), ImageReference: "img@sha256:bb", SpecHash: "h2", CreatedBy: "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a"},
		{ID: "id-1", VersionNumber: 1, Version: "label-1", CreatedAt: at, ImageReference: "img@sha256:aa", SpecHash: "h1", IsCurrent: true},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/details", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/details = %d (%s)", rec.Code, rec.Body.String())
	}
	var d struct {
		Latest map[string]any `json:"latest_dag_version"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{
		"id": "id-2", "version_number": float64(2), "dag_id": "etl", "bundle_name": "leoflow",
		"bundle_version": "label-2", "created_at": "2026-10-14T00:42:33Z", "dag_display_name": "etl",
		"bundle_url": nil, "image": "img@sha256:bb", "spec_hash": "h2", "is_current": false,
		"created_by": "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a",
	}
	if len(d.Latest) != len(want) {
		t.Errorf("latest_dag_version has %d fields, want %d: %v", len(d.Latest), len(want), d.Latest)
	}
	for k, v := range want {
		if got, ok := d.Latest[k]; !ok || got != v {
			t.Errorf("latest_dag_version.%s = %v, want %v", k, got, v)
		}
	}
}
