package cli

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
)

func set(ids ...string) map[string]struct{} {
	m := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		m[id] = struct{}{}
	}
	return m
}

// TestReconcileStartup_DeregistersGhosts: on boot, DAGs the control plane still
// has registered that are NOT in the current workspace (stale from a reused
// metadata DB or a previous workspace, #404) are deregistered, so Lite self-heals
// to match the workspace without manual cleanup.
func TestReconcileStartup_DeregistersGhosts(t *testing.T) {
	t.Parallel()
	var deleted []string
	del := func(id string) error { deleted = append(deleted, id); return nil }

	seen := reconcileStartup(set("hello", "ghost_a", "ghost_b"), nil, set("hello"), del, func(string, ...any) {})

	sort.Strings(deleted)
	if !reflect.DeepEqual(deleted, []string{"ghost_a", "ghost_b"}) {
		t.Errorf("deregistered = %v, want [ghost_a ghost_b]", deleted)
	}
	if !reflect.DeepEqual(seen, set("hello")) {
		t.Errorf("seen = %v, want {hello}", seen)
	}
}

// TestReconcileStartup_FetchErrorIsFailSafe: if the registered-DAG list can't be
// fetched (control plane unreachable), reconcile deregisters NOTHING and keeps the
// workspace IDs — never destructive on an unreliable control plane.
func TestReconcileStartup_FetchErrorIsFailSafe(t *testing.T) {
	t.Parallel()
	called := false
	del := func(string) error { called = true; return nil }

	seen := reconcileStartup(nil, errors.New("connection refused"), set("hello"), del, func(string, ...any) {})

	if called {
		t.Error("deleteDag called despite a fetch error — must be fail-safe")
	}
	if !reflect.DeepEqual(seen, set("hello")) {
		t.Errorf("seen = %v, want the workspace fallback {hello}", seen)
	}
}

// TestReconcileStartup_CleanIsNoop: when the control plane already matches the
// workspace, nothing is deregistered.
func TestReconcileStartup_CleanIsNoop(t *testing.T) {
	t.Parallel()
	called := false
	reconcileStartup(set("hello"), nil, set("hello"), func(string) error { called = true; return nil }, func(string, ...any) {})
	if called {
		t.Error("deleteDag called on a clean workspace")
	}
}

// TestFetchRegisteredDagIDs parses the control plane's GET /api/v2/dags into a
// set of dag_ids, and surfaces a non-200 as an error (so reconcile stays
// fail-safe rather than treating an error page as "no DAGs registered").
func TestFetchRegisteredDagIDs(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/dags" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(`{"dags":[{"dag_id":"hello"},{"dag_id":"weather"}],"total_entries":2}`))
	}))
	defer srv.Close()

	got, err := fetchRegisteredDagIDs(srv.URL, "tok")
	if err != nil {
		t.Fatalf("fetchRegisteredDagIDs() error: %v", err)
	}
	if !reflect.DeepEqual(got, set("hello", "weather")) {
		t.Errorf("got %v, want {hello weather}", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := fetchRegisteredDagIDs(bad.URL, "tok"); err == nil {
		t.Error("fetchRegisteredDagIDs() on 500 = nil error, want error (fail-safe)")
	}
}

// TestImportErrorStale: an import error is stale (and should be cleared on boot)
// when its file is outside the current workspace (a previous workspace, #404) OR
// its file no longer exists (a removed broken DAG). A current broken DAG — file
// present, under the workspace — is NOT stale: its error is real and must stay.
func TestImportErrorStale(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	existing := filepath.Join(root, "good", "dag.py")
	if err := os.MkdirAll(filepath.Dir(existing), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(existing, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name     string
		filename string
		want     bool
	}{
		{"outside workspace", "/some/other/ws/foo/dag.py", true},
		{"under workspace, file gone", filepath.Join(root, "gone", "dag.py"), true},
		{"under workspace, file exists", existing, false},
	}
	for _, c := range cases {
		if got := importErrorStale(c.filename, root); got != c.want {
			t.Errorf("%s: importErrorStale(%q) = %v, want %v", c.name, c.filename, got, c.want)
		}
	}
}

// TestReconcileImportErrors_ClearsStale: only the stale entries are cleared.
func TestReconcileImportErrors_ClearsStale(t *testing.T) {
	t.Parallel()
	stale := map[string]bool{"/a/stale.py": true, "/b/current.py": false, "/c/stale2.py": true}
	var cleared []string
	reconcileImportErrors(
		[]string{"/a/stale.py", "/b/current.py", "/c/stale2.py"}, nil,
		func(f string) bool { return stale[f] },
		func(f string) error { cleared = append(cleared, f); return nil },
		func(string, ...any) {},
	)
	sort.Strings(cleared)
	if !reflect.DeepEqual(cleared, []string{"/a/stale.py", "/c/stale2.py"}) {
		t.Errorf("cleared = %v, want [/a/stale.py /c/stale2.py]", cleared)
	}
}

// TestReconcileImportErrors_FetchErrorIsFailSafe: a list-fetch error clears nothing.
func TestReconcileImportErrors_FetchErrorIsFailSafe(t *testing.T) {
	t.Parallel()
	called := false
	reconcileImportErrors(nil, errors.New("unreachable"),
		func(string) bool { return true },
		func(string) error { called = true; return nil },
		func(string, ...any) {})
	if called {
		t.Error("cleared an import error despite a fetch error — must be fail-safe")
	}
}

// TestFetchImportErrorFiles parses the control plane's GET /api/v2/importErrors
// into the list of filenames; a non-200 is an error (fail-safe).
func TestFetchImportErrorFiles(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"import_errors":[{"filename":"/ws/a/dag.py"},{"filename":"/ws/b/dag.py"}]}`))
	}))
	defer srv.Close()
	got, err := fetchImportErrorFiles(srv.URL, "tok")
	if err != nil {
		t.Fatalf("fetchImportErrorFiles() error: %v", err)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"/ws/a/dag.py", "/ws/b/dag.py"}) {
		t.Errorf("got %v, want the two filenames", got)
	}

	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer bad.Close()
	if _, err := fetchImportErrorFiles(bad.URL, "tok"); err == nil {
		t.Error("fetchImportErrorFiles() on 500 = nil error, want error")
	}
}
