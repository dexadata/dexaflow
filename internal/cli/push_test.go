package cli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const pushSpec = `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`

func TestPushVersionPostsSpec(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"created":true}`)
	}))
	defer srv.Close()

	status, body, err := pushVersion(context.Background(), srv.URL, "tok", "etl", []byte(pushSpec))
	if err != nil {
		t.Fatalf("pushVersion: %v", err)
	}
	if status != http.StatusCreated {
		t.Errorf("status = %d, want 201", status)
	}
	if gotPath != "/api/v2/dags/etl/versions" {
		t.Errorf("path = %q", gotPath)
	}
	if gotAuth != "Bearer tok" {
		t.Errorf("auth = %q, want Bearer tok", gotAuth)
	}
	if !strings.Contains(body, "created") {
		t.Errorf("body = %q", body)
	}
}

func TestPushCommandEndToEnd(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(pushSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "push", f, "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("push command: %v", err)
	}
	if !strings.Contains(out, "Registered") {
		t.Errorf("output = %q, want to contain Registered", out)
	}
}

func TestPushCommandUsesConfigTokenWhenFlagAbsent(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	// A config produced by `dexaflow auth login`: it carries both server_url and token.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server_url: "+srv.URL+"\ntoken: jwt-saved\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(pushSpec), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, _, err := run(t, "push", f, "--config", cfgPath); err != nil {
		t.Fatalf("push using config token: %v", err)
	}
	if gotAuth != "Bearer jwt-saved" {
		t.Errorf("auth = %q, want the token persisted by login (Bearer jwt-saved)", gotAuth)
	}
}

func TestPushCommandUsesConfigServerURL(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hit = true
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	t.Setenv("LEOFLOW_SERVER_URL", srv.URL)
	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(pushSpec), 0o644); err != nil {
		t.Fatal(err)
	}
	// No --server flag: the server URL must come from config (env).
	if _, _, err := run(t, "push", f); err != nil {
		t.Fatalf("push without --server: %v", err)
	}
	if !hit {
		t.Error("server URL from config was not used")
	}
}

func TestPushCommandRejectsInvalidSpec(t *testing.T) {
	f := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(f, []byte(`{"dag_id":"etl","tasks":[]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := run(t, "push", f, "--server", "http://unused"); err == nil {
		t.Error("push should reject an invalid spec before posting")
	}
}
