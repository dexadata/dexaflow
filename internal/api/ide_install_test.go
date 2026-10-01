package api

import (
	"encoding/json"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"testing/fstest"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/workspace"
)

// mockExamplesFS produces a small embedded-FS replacement with two example
// DAGs (`bash_pipeline`, `csv_report`) under the same `examples/` root the
// real embed.FS uses. Enough for the install handler to walk and decide.
func mockExamplesFS() fs.FS {
	return fstest.MapFS{
		"examples/bash_pipeline/dag.py":       &fstest.MapFile{Data: []byte("print('bash')\n")},
		"examples/bash_pipeline/leoflow.yaml": &fstest.MapFile{Data: []byte("schema_version: \"1.0\"\ndag_id: bash_pipeline\n")},
		"examples/csv_report/dag.py":          &fstest.MapFile{Data: []byte("print('csv')\n")},
		"examples/csv_report/leoflow.yaml":    &fstest.MapFile{Data: []byte("schema_version: \"1.0\"\ndag_id: csv_report\n")},
	}
}

// ideServerWithExamples wires the IDE routes including the install endpoint,
// so the test can POST to /api/v2/ide/examples/install.
func ideServerWithExamples(store WorkspaceFS, examples fs.FS) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Workspace:     store,
		ExamplesFS:    examples,
	})
}

func newWorkspaceWithProject(t *testing.T, name string) WorkspaceFS {
	t.Helper()
	dir := t.TempDir()
	projectDir := filepath.Join(dir, name)
	if err := os.MkdirAll(projectDir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "leoflow.yaml"),
		[]byte("schema_version: \"1.0\"\ndag_id: "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(projectDir, "dag.py"), []byte("print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	store, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return store
}

// TestInstallExamplesSkipsRootCollision covers #298b (alpha-prep): when the
// workspace already has a top-level project with the same name as an
// embedded example, the install must skip that example wholesale — otherwise
// the next `leoflow lite` boot refuses to start (multi-DAG discovery rejects
// duplicate dag_ids). The handler reports skipped examples in a dedicated
// response field so the IDE can surface "skipped: bash_pipeline (already
// exists)" to the user instead of silently producing a broken workspace.
func TestInstallExamplesSkipsRootCollision(t *testing.T) {
	srv := ideServerWithExamples(newWorkspaceWithProject(t, "bash_pipeline"), mockExamplesFS())

	rec := authGet(srv, http.MethodPost, "/api/v2/ide/examples/install", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("install = %d (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Installed       []string `json:"installed"`
		Skipped         []string `json:"skipped"`
		SkippedExamples []string `json:"skipped_examples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}

	if !slices.Contains(got.SkippedExamples, "bash_pipeline") {
		t.Errorf("bash_pipeline should be in SkippedExamples (collides with workspace root); got %+v", got)
	}
	for _, p := range got.Installed {
		if filepath.Base(filepath.Dir(p)) == "bash_pipeline" {
			t.Errorf("bash_pipeline files should not have been installed; saw %q in Installed", p)
		}
	}
	// csv_report does not collide and should be installed normally.
	foundCSV := false
	for _, p := range got.Installed {
		if filepath.Base(filepath.Dir(p)) == "csv_report" {
			foundCSV = true
			break
		}
	}
	if !foundCSV {
		t.Errorf("csv_report should still install when only bash_pipeline collides; Installed=%+v", got.Installed)
	}
}

// TestInstallExamplesCleanWorkspace pins the happy path: with no collisions,
// every example materializes and nothing is reported as skipped.
func TestInstallExamplesCleanWorkspace(t *testing.T) {
	srv := ideServerWithExamples(newWorkspaceWithProject(t, "unrelated_project"), mockExamplesFS())

	rec := authGet(srv, http.MethodPost, "/api/v2/ide/examples/install", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("install = %d (%s)", rec.Code, rec.Body.String())
	}

	var got struct {
		Installed       []string `json:"installed"`
		Skipped         []string `json:"skipped"`
		SkippedExamples []string `json:"skipped_examples"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.SkippedExamples) != 0 {
		t.Errorf("clean workspace should not skip any example; got %+v", got.SkippedExamples)
	}
	if len(got.Installed) == 0 {
		t.Errorf("clean workspace should install both examples; got nothing")
	}
}
