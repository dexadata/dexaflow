package api

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRegisterVersionBuildInfo: the build_* query parameters reach the
// repository as the version's build info, on the register and on its dry
// run; a register without them sends none; a bad or empty value is refused
// with 400 naming the parameter, before anything is registered (#1702).
func TestRegisterVersionBuildInfo(t *testing.T) {
	full := url.Values{
		"build_commit":       {"0123456789abcdef0123456789abcdef01234567"},
		"build_repo":         {"https://github.com/acme/etl"},
		"build_pipeline_url": {"https://github.com/acme/etl/actions/runs/42"},
		"build_builder":      {"code-layer"},
		"build_base_image":   {"ghcr.io/dexadata/dexaflow-runtime:py3.12-v0.5.4"},
		"build_cli_version":  {"v0.5.4"},
	}
	want := domain.BuildInfo{
		Commit: "0123456789abcdef0123456789abcdef01234567", Repo: "https://github.com/acme/etl",
		PipelineURL: "https://github.com/acme/etl/actions/runs/42", Builder: "code-layer",
		BaseImage: "ghcr.io/dexadata/dexaflow-runtime:py3.12-v0.5.4", CLIVersion: "v0.5.4",
	}
	for _, route := range []string{"/versions", "/versions/dry_run"} {
		for _, tc := range []struct {
			name, query, refused string
			want                 *domain.BuildInfo
		}{
			{name: "none", query: ""},
			{name: "all", query: "?" + full.Encode(), want: &want},
			{name: "commit only", query: "?build_commit=abc1234", want: &domain.BuildInfo{Commit: "abc1234"}},
			{name: "empty value", query: "?build_commit=", refused: "build_commit"},
			{name: "bad commit", query: "?build_commit=main", refused: "build_commit"},
			{name: "ssh repo", query: "?build_repo=" + url.QueryEscape("git@github.com:acme/etl.git"), refused: "build_repo"},
			{name: "repo credentials", query: "?build_repo=" + url.QueryEscape("https://u:p@github.com/acme/etl"), refused: "build_repo"},
			{name: "javascript pipeline", query: "?build_pipeline_url=" + url.QueryEscape("javascript:alert(1)"), refused: "build_pipeline_url"},
			{name: "builder control", query: "?build_builder=a%0Ab", refused: "build_builder"},
			{name: "base image too long", query: "?build_base_image=" + strings.Repeat("i", 513), refused: "build_base_image"},
			{name: "cli version bidi", query: "?build_cli_version=v1%E2%80%AE", refused: "build_cli_version"},
		} {
			t.Run(route+" "+tc.name, func(t *testing.T) {
				repo := &fakeVersionRepo{created: true}
				rec := authGet(versionServer(repo), http.MethodPost, "/api/v2/dags/etl"+route+tc.query, validSpecJSON)
				if tc.refused != "" {
					if rec.Code != http.StatusBadRequest {
						t.Fatalf("register = %d, want 400 (%s)", rec.Code, rec.Body.String())
					}
					if !strings.Contains(rec.Body.String(), tc.refused) {
						t.Errorf("refusal %s does not name %s", rec.Body.String(), tc.refused)
					}
					if len(repo.builds) != 0 {
						t.Errorf("a refused query still registered")
					}
					return
				}
				if rec.Code >= http.StatusMultipleChoices {
					t.Fatalf("register = %d (%s)", rec.Code, rec.Body.String())
				}
				if len(repo.builds) != 1 {
					t.Fatalf("registers = %d, want 1", len(repo.builds))
				}
				got := repo.builds[0]
				if tc.want == nil {
					if got != nil {
						t.Errorf("build_info = %+v, want none", got)
					}
					return
				}
				if got == nil || *got != *tc.want {
					t.Errorf("build_info = %+v, want %+v", got, tc.want)
				}
			})
		}
	}
}

// TestDagVersionsExposeBuildInfo: GET dagVersions adds build_info next to
// Airflow's fields, null for a version registered without one.
func TestDagVersionsExposeBuildInfo(t *testing.T) {
	info := &domain.BuildInfo{Commit: "abc1234", Repo: "https://github.com/acme/etl", Builder: "code-layer"}
	srv := versionsServer([]domain.DAG{{DagID: "etl"}}, []domain.DagVersion{
		{ID: "v2", VersionNumber: 2, Version: "b", BuildInfo: info},
		{ID: "v1", VersionNumber: 1, Version: "a"},
	})
	rec := authGet(srv, http.MethodGet, "/api/v2/dags/etl/dagVersions", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/dagVersions = %d (%s)", rec.Code, rec.Body.String())
	}
	var col struct {
		DagVersions []map[string]json.RawMessage `json:"dag_versions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	var got domain.BuildInfo
	if err := json.Unmarshal(col.DagVersions[0]["build_info"], &got); err != nil || got != *info {
		t.Errorf("build_info = %s, want %+v (%v)", col.DagVersions[0]["build_info"], info, err)
	}
	if raw, ok := col.DagVersions[1]["build_info"]; !ok || string(raw) != "null" {
		t.Errorf("older version build_info = %s (present %v), want null", raw, ok)
	}
	for _, field := range []string{"id", "version_number", "dag_id", "bundle_name", "bundle_version", "created_at", "dag_display_name", "bundle_url"} {
		if _, ok := col.DagVersions[0][field]; !ok {
			t.Errorf("Airflow field %s missing", field)
		}
	}
}

// TestBuildInfoOnlyOnVersionRoutes: build_info is served by the routes about
// versions (one version by number, and the from and to of a rollback), and
// left out of the dag_version copies embedded in other answers (a DAG's
// latest_dag_version here; task instances in
// TestTaskInstanceDagVersionIsTheRunsVersion), which stay Airflow's shape.
func TestBuildInfoOnlyOnVersionRoutes(t *testing.T) {
	info := &domain.BuildInfo{Commit: "abc1234"}
	srv := versionsServer([]domain.DAG{{DagID: "etl"}}, []domain.DagVersion{{ID: "v1", VersionNumber: 1, Version: "a", BuildInfo: info}})
	field := func(path string, pick func(map[string]json.RawMessage) map[string]json.RawMessage) (json.RawMessage, bool) {
		t.Helper()
		rec := authGet(srv, http.MethodGet, path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("%s = %d (%s)", path, rec.Code, rec.Body.String())
		}
		var body map[string]json.RawMessage
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		v, ok := pick(body)["build_info"]
		return v, ok
	}
	if v, ok := field("/api/v2/dags/etl/dagVersions/1", func(b map[string]json.RawMessage) map[string]json.RawMessage { return b }); !ok || !strings.Contains(string(v), "abc1234") {
		t.Errorf("dagVersions/1 build_info = %s (present %v)", v, ok)
	}
	if v, ok := field("/api/v2/dags/etl/details", func(b map[string]json.RawMessage) map[string]json.RawMessage {
		var latest map[string]json.RawMessage
		_ = json.Unmarshal(b["latest_dag_version"], &latest)
		return latest
	}); ok {
		t.Errorf("latest_dag_version carries build_info %s", v)
	}

	change := rollbackChange()
	change.To.BuildInfo, change.From.BuildInfo = info, nil
	rec := authGet(versionServer(&fakeVersionRepo{change: change}), http.MethodPut, "/api/v2/dags/etl/currentVersion", `{"version":"v1"}`)
	var resp struct {
		From map[string]json.RawMessage `json:"from"`
		To   map[string]json.RawMessage `json:"to"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(resp.To["build_info"]), "abc1234") || string(resp.From["build_info"]) != "null" {
		t.Errorf("rollback to.build_info = %s, from.build_info = %s", resp.To["build_info"], resp.From["build_info"])
	}
}
