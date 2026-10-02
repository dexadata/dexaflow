package api

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/workspace"
)

// ideServer wires a server whose IDE routes are backed by a real workspace FS
// rooted at dir. A nil fs leaves the IDE disabled.
func ideServer(fs WorkspaceFS) *gin.Engine {
	return ideServerWithMonaco(fs, "")
}

// ideServerWithMonaco is ideServer plus a Monaco assets directory to serve.
func ideServerWithMonaco(fs WorkspaceFS, monacoDir string) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Workspace:     fs,
		MonacoDir:     monacoDir,
	})
}

func newWorkspace(t *testing.T) (store WorkspaceFS, dir string) {
	t.Helper()
	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("print('hi')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	fs, err := workspace.New(dir)
	if err != nil {
		t.Fatal(err)
	}
	return fs, dir
}

func TestIDETree(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodGet, "/api/v2/ide/tree", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("tree = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Entries []workspace.Entry `json:"entries"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Path != "dag.py" {
		t.Fatalf("unexpected tree: %+v", got.Entries)
	}
}

func TestIDEReadFile(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodGet, "/api/v2/ide/file?path=dag.py", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("read = %d (%s)", rec.Code, rec.Body.String())
	}
	var got struct {
		Path    string `json:"path"`
		Content string `json:"content"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Path != "dag.py" || got.Content != "print('hi')\n" {
		t.Fatalf("unexpected read: %+v", got)
	}
}

func TestIDEWriteFile(t *testing.T) {
	fs, dir := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodPut, "/api/v2/ide/file", `{"path":"dag.py","content":"print('bye')\n"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("write = %d (%s)", rec.Code, rec.Body.String())
	}
	on, _ := os.ReadFile(filepath.Join(dir, "dag.py"))
	if string(on) != "print('bye')\n" {
		t.Fatalf("file not written, got %q", on)
	}
}

func TestIDECreateAndDelete(t *testing.T) {
	fs, dir := newWorkspace(t)
	srv := ideServer(fs)
	// Create a file.
	rec := authGet(srv, http.MethodPost, "/api/v2/ide/file", `{"path":"tasks/new.py"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks", "new.py")); err != nil {
		t.Fatalf("created file missing: %v", err)
	}
	// Delete it.
	rec = authGet(srv, http.MethodDelete, "/api/v2/ide/file?path=tasks/new.py", "")
	if rec.Code != http.StatusNoContent {
		t.Fatalf("delete = %d (%s)", rec.Code, rec.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks", "new.py")); !os.IsNotExist(err) {
		t.Fatalf("file still present: %v", err)
	}
}

// TestIDEDeleteHandlesNonEmptyFolderRecursively pins the contract the new IDE
// delete UX depends on: hitting DELETE on a directory path removes the
// directory and every descendant. Without this, the editor's "Delete folder"
// confirmation would lie — the call would return 200 but leave a partially
// scrubbed tree, and the user would have to fall back to `rm -rf` from the
// terminal (the friction PR-C/#347 is meant to remove).
func TestIDEDeleteHandlesNonEmptyFolderRecursively(t *testing.T) {
	fs, dir := newWorkspace(t)
	srv := ideServer(fs)
	// Build a small subtree: tasks/{extract.py,transform.py,sub/load.py}.
	if r := authGet(srv, http.MethodPost, "/api/v2/ide/file", `{"path":"tasks/extract.py"}`); r.Code != http.StatusCreated {
		t.Fatalf("create extract = %d (%s)", r.Code, r.Body.String())
	}
	if r := authGet(srv, http.MethodPost, "/api/v2/ide/file", `{"path":"tasks/transform.py"}`); r.Code != http.StatusCreated {
		t.Fatalf("create transform = %d (%s)", r.Code, r.Body.String())
	}
	if r := authGet(srv, http.MethodPost, "/api/v2/ide/file", `{"path":"tasks/sub/load.py"}`); r.Code != http.StatusCreated {
		t.Fatalf("create load = %d (%s)", r.Code, r.Body.String())
	}
	// DELETE the parent directory.
	if r := authGet(srv, http.MethodDelete, "/api/v2/ide/file?path=tasks", ""); r.Code != http.StatusNoContent {
		t.Fatalf("delete folder = %d (%s)", r.Code, r.Body.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "tasks")); !os.IsNotExist(err) {
		t.Fatalf("folder still present after recursive delete: %v", err)
	}
}

func TestIDETraversalRejected(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/file?path=../../etc/passwd", ""); r.Code != http.StatusBadRequest {
		t.Errorf("read traversal = %d, want 400", r.Code)
	}
	if r := authGet(srv, http.MethodPut, "/api/v2/ide/file", `{"path":"../escape.py","content":"x"}`); r.Code != http.StatusBadRequest {
		t.Errorf("write traversal = %d, want 400", r.Code)
	}
}

func TestIDEReadMissingIs404(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/file?path=nope.py", ""); r.Code != http.StatusNotFound {
		t.Errorf("read missing = %d, want 404", r.Code)
	}
}

func TestIDEMissingPathParamIs400(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/file", ""); r.Code != http.StatusBadRequest {
		t.Errorf("missing path = %d, want 400", r.Code)
	}
}

// TestIDEDisabledWhenNoWorkspace asserts the IDE is gated: with no workspace
// (Production, or Lite without one configured) the routes are not registered.
func TestIDEDisabledWhenNoWorkspace(t *testing.T) {
	srv := ideServer(nil)
	if r := authGet(srv, http.MethodGet, "/api/v2/ide/tree", ""); r.Code != http.StatusNotFound {
		t.Errorf("tree without workspace = %d, want 404 (route absent)", r.Code)
	}
	if r := authGet(srv, http.MethodGet, "/ide", ""); r.Code != http.StatusNotFound {
		t.Errorf("/ide without workspace = %d, want 404 (route absent)", r.Code)
	}
}

func TestIDEPageServed(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs)
	rec := authGet(srv, http.MethodGet, "/ide", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("/ide = %d", rec.Code)
	}
	body := rec.Body.String()
	for _, marker := range []string{"<html", "monaco", "/api/v2/ide/tree", "Dexaflow"} {
		if !strings.Contains(body, marker) {
			t.Errorf("/ide page missing marker %q", marker)
		}
	}
	// Guard the Monaco model-leak fix: opening a file must dispose the previous
	// model (models are not GC'd), or every open leaks a buffer and a few tabs
	// freeze the browser. Verified live: without dispose, getModels() grew 1->16
	// over 15 opens; with it, stayed at 1.
	if !strings.Contains(body, ".dispose()") {
		t.Error("/ide page must dispose the previous Monaco model on file open (memory leak otherwise)")
	}
	// PR-C #347 invariants — pin so a refactor never silently drops them:
	//   1. The "create target" chip exists in the header so the user can see
	//      where New file / New folder will land before clicking the button.
	//   2. The expanded-folder set is persisted in localStorage so a page
	//      refresh restores the tree shape.
	//   3. Delete confirmation makes the recursive folder semantics explicit
	//      so a stray click never silently wipes a tree.
	for _, marker := range []string{
		`id="ctx"`,             // (1) the create-target chip
		"leoflow.ide.expanded", // (2) the persisted-expansion key
		"ALL its contents",     // (3) the recursive-delete warning
		"firstLoad",            // (4) auto-expand-on-first-load (no flat-tree regression)
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("/ide page missing PR-C marker %q (UX regression)", marker)
		}
	}
}

func TestIDEMonacoNotProvisioned404(t *testing.T) {
	fs, _ := newWorkspace(t)
	srv := ideServer(fs) // no Monaco dir
	if r := authGet(srv, http.MethodGet, "/ide/vs/loader.js", ""); r.Code != http.StatusNotFound {
		t.Errorf("monaco loader without dir = %d, want 404", r.Code)
	}
}

func TestIDEMonacoServedFromDir(t *testing.T) {
	fs, _ := newWorkspace(t)
	mdir := t.TempDir()
	// The bundle lives in a vs/ subdir, matching how Monaco is extracted and how
	// the page requests it (/ide/vs/...).
	if err := os.MkdirAll(filepath.Join(mdir, "vs", "editor"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "vs", "loader.js"), []byte("// monaco loader"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mdir, "vs", "editor", "editor.main.js"), []byte("// editor"), 0o600); err != nil {
		t.Fatal(err)
	}
	srv := ideServerWithMonaco(fs, mdir)
	if rec := authGet(srv, http.MethodGet, "/ide/vs/loader.js", ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "monaco loader") {
		t.Fatalf("monaco loader = %d (%s)", rec.Code, rec.Body.String())
	}
	// A nested asset resolves too.
	if rec := authGet(srv, http.MethodGet, "/ide/vs/editor/editor.main.js", ""); rec.Code != http.StatusOK {
		t.Fatalf("nested monaco asset = %d", rec.Code)
	}
}
