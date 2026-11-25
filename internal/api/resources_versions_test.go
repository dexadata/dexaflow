package api

// The DAG version a run, its task instances and the DAG itself carry on the wire.

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestDagRunDTOCarriesBundleVersion: the run's pinned version must reach the
// wire as bundle_version.
//
// This is not cosmetic. The embedded SPA renders the clear dialog's "Run with
// latest bundle version" checkbox only when the run's bundle_version differs
// from the DAG's AND the run's is neither null nor empty:
//
//	ee = F !== I && I !== null && I !== ``     (F = the DAG's, I = the run's)
//
// leoflow sent null, so the control never rendered. With the server now honoring
// run_on_latest_version, a null here means the operator cannot ask for the
// current version through any interface at all — there is no CLI clear either.
func TestDagRunDTOCarriesBundleVersion(t *testing.T) {
	t.Run("a pinned version is serialized", func(t *testing.T) {
		dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1", Version: "v1.2.3"})
		if dto.BundleVersion == nil {
			t.Fatal("bundle_version is null; the SPA hides the version control when it is")
		}
		if *dto.BundleVersion != "v1.2.3" {
			t.Errorf("bundle_version = %q, want the run's pinned version", *dto.BundleVersion)
		}
	})
	t.Run("an unresolvable version stays null rather than empty", func(t *testing.T) {
		// The SPA treats "" the same as null, so an empty string would be a
		// pointless non-null. A run whose version row is gone is the real case.
		dto := toDagRunDTO(domain.DagRun{DagID: "etl", RunID: "r1"})
		if dto.BundleVersion != nil {
			t.Errorf("bundle_version = %q, want null", *dto.BundleVersion)
		}
	})
}

// TestDagDTOCarriesCurrentVersion: the other half of the comparison. With only
// the run's version populated the checkbox would render unconditionally, because
// a null on the DAG side always differs from a non-null run version.
func TestDagDTOCarriesCurrentVersion(t *testing.T) {
	dto := toDagWithRunsDTO(domain.DAG{DagID: "etl", CurrentVersion: "v2.0.0"}, nil)
	if dto.BundleVersion == nil {
		t.Fatal("bundle_version is null on the DAG; the clear dialog then always offers the toggle")
	}
	if *dto.BundleVersion != "v2.0.0" {
		t.Errorf("bundle_version = %q, want the DAG's current version", *dto.BundleVersion)
	}
}

// TestTaskInstanceDagVersionCarriesTheRunsPinnedLabel: the single-task clear
// dialog of the embedded Airflow 3.2 UI (the ordinary "my task failed, clear
// it" path) decides whether to offer "Run with latest bundle version" in cQt,
// which compares the DAG's details.bundle_version (the current label) with
// the task instance's dag_version.bundle_version, and
// details.latest_dag_version.version_number with dag_version.version_number.
// The literal that built that nested object once omitted BundleVersion, so
// the label comparison never fired.
//
// The label must come from the RUN's pinned version, not from the DAG's
// latest. Here the run's label no longer resolves to a version, the fallback
// case, and it must still be the run's.
func TestTaskInstanceDagVersionCarriesTheRunsPinnedLabel(t *testing.T) {
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", Version: "v1-pinned"}}}
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{
		{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed},
	}}
	srv := newTestServer(Dependencies{
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
			Tasks:   tasks,
			DagRuns: runs,
			DagVersions: &fakeVersionLister{versions: []domain.DagVersion{
				// The DAG's latest is deliberately different from the run's pinned
				// label: if the DTO followed this instead, the two would match and the
				// dialog would hide the control.
				{ID: "v-uuid", VersionNumber: 2, CreatedAt: time.Now().UTC(), Version: "v2-latest"},
			}},
		},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("taskInstances = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		TaskInstances []struct {
			DagVersion *struct {
				BundleVersion *string `json:"bundle_version"`
			} `json:"dag_version"`
		} `json:"task_instances"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.TaskInstances) == 0 {
		t.Fatal("no task instances returned; the assertions below would be vacuous")
	}
	dv := got.TaskInstances[0].DagVersion
	if dv == nil || dv.BundleVersion == nil {
		t.Fatalf("dag_version.bundle_version is null; the single-task clear dialog hides its version control: %s", rec.Body.String())
	}
	if *dv.BundleVersion == "v2-latest" {
		t.Fatal("dag_version.bundle_version followed the DAG's LATEST version; it must be the run's pinned one, or it equals the DAG's and the control stays hidden anyway")
	}
	if *dv.BundleVersion != "v1-pinned" {
		t.Errorf("dag_version.bundle_version = %q, want the run's pinned v1-pinned", *dv.BundleVersion)
	}

	// The clear endpoint builds its own affected/dry-run payload through a SECOND
	// resolver (resolveRunContextFor, which takes the run from the body rather
	// than the path). It is a separate copy of the same two lines, and mutating
	// only that copy left the suite green — an untested duplicate of code this
	// test exists to prove matters.
	rec = authGet(srv, http.MethodPost, "/api/v2/dags/etl/clearTaskInstances",
		`{"dag_run_id":"r1","task_ids":["extract"],"dry_run":true}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear dry_run = %d (%s)", rec.Code, rec.Body.String())
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.TaskInstances) == 0 {
		t.Fatal("the clear preview returned nothing; the assertion below would be vacuous")
	}
	dv = got.TaskInstances[0].DagVersion
	if dv == nil || dv.BundleVersion == nil {
		t.Fatalf("the clear payload's dag_version.bundle_version is null: %s", rec.Body.String())
	}
	if *dv.BundleVersion != "v1-pinned" {
		t.Errorf("clear payload dag_version.bundle_version = %q, want v1-pinned", *dv.BundleVersion)
	}
}

// TestTaskInstanceDagVersionIsTheRunsVersion: a task instance's dag_version is
// the version its run was created with, complete as the dagVersions listing
// serves it, not the DAG's newest version. After a rollback the two differ, and
// the Code tab of a task instance opens on this version_number.
func TestTaskInstanceDagVersionIsTheRunsVersion(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 41, 33, 0, time.UTC)
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", Version: "label-1"}}}
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed, MapIndex: -1}}}
	srv := newTestServer(Dependencies{
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
			Tasks:   tasks,
			DagRuns: runs,
			DagVersions: &fakeVersionLister{versions: []domain.DagVersion{
				{ID: "id-2", VersionNumber: 2, Version: "label-2", CreatedAt: at.Add(time.Minute), ImageReference: "img@sha256:bb", SpecHash: "h2"},
				{ID: "id-1", VersionNumber: 1, Version: "label-1", CreatedAt: at, ImageReference: "img@sha256:aa", SpecHash: "h1", IsCurrent: true, CreatedBy: "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a"},
			}},
		},
	})
	want := map[string]any{
		"id": "id-1", "version_number": float64(1), "dag_id": "etl", "bundle_name": "leoflow",
		"bundle_version": "label-1", "created_at": "2026-10-14T00:41:33Z", "dag_display_name": "etl",
		"bundle_url": nil, "image": "img@sha256:aa", "spec_hash": "h1", "is_current": true,
		"created_by": "7f3c2a10-5b1e-4c3d-9a8e-2f6b1c0d4e5a",
	}
	check := func(what, method, path, body string) {
		t.Helper()
		rec := authGet(srv, method, path, body)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", what, rec.Code, rec.Body.String())
		}
		var got struct {
			TaskInstances []struct {
				DagVersion map[string]any `json:"dag_version"`
			} `json:"task_instances"`
			DagVersion map[string]any `json:"dag_version"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		dv := got.DagVersion
		if len(got.TaskInstances) > 0 {
			dv = got.TaskInstances[0].DagVersion
		}
		if len(dv) != len(want) {
			t.Errorf("%s: dag_version has %d fields, want %d: %v", what, len(dv), len(want), dv)
		}
		for k, v := range want {
			if g, ok := dv[k]; !ok || g != v {
				t.Errorf("%s: dag_version.%s = %v, want %v", what, k, g, v)
			}
		}
	}
	check("taskInstances", http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances", "")
	check("taskInstance", http.MethodGet, "/api/v2/dags/etl/dagRuns/r1/taskInstances/extract", "")
	check("clear dry_run", http.MethodPost, "/api/v2/dags/etl/clearTaskInstances", `{"dag_run_id":"r1","task_ids":["extract"],"dry_run":true}`)
}

// TestTaskInstanceDagVersionTieBreak: when two versions carry the run's label,
// the task instance's dag_version is the highest-numbered one, the version the
// run's own dag_versions names, so the run and its task instances agree.
func TestTaskInstanceDagVersionTieBreak(t *testing.T) {
	at := time.Date(2026, 10, 14, 0, 41, 33, 0, time.UTC)
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1", Version: "dev"}}}
	tasks := &fakeTaskRepo{tis: []domain.TaskInstance{{TaskID: "extract", RunID: "r1", State: domain.TaskStateFailed, MapIndex: -1}}}
	srv := newTestServer(Dependencies{
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
			Tasks:   tasks,
			DagRuns: runs,
			DagVersions: &fakeVersionLister{versions: []domain.DagVersion{
				{ID: "id-4", VersionNumber: 4, Version: "other", CreatedAt: at.Add(3 * time.Minute)},
				{ID: "id-3", VersionNumber: 3, Version: "dev", CreatedAt: at.Add(2 * time.Minute)},
				{ID: "id-2", VersionNumber: 2, Version: "dev", CreatedAt: at.Add(time.Minute)},
			}},
		},
	})
	for _, path := range []string{"/api/v2/dags/etl/dagRuns/r1/taskInstances", "/api/v2/dags/etl/dagRuns/r1"} {
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", path, rec.Code, rec.Body.String())
		}
		var got struct {
			TaskInstances []struct {
				DagVersion struct {
					ID string `json:"id"`
				} `json:"dag_version"`
			} `json:"task_instances"`
			DagVersions []struct {
				ID string `json:"id"`
			} `json:"dag_versions"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		var id string
		switch {
		case len(got.TaskInstances) == 1:
			id = got.TaskInstances[0].DagVersion.ID
		case len(got.DagVersions) == 1:
			id = got.DagVersions[0].ID
		}
		if id != "id-3" {
			t.Errorf("%s names version %q, want id-3, the highest-numbered dev", path, id)
		}
	}
}
