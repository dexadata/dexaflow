package client

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestNewInjectsBearerToken: a client built with a token carries
// "Authorization: Bearer <token>" on every request and targets the /api/v2 path
// the operation maps to. This is the pass-through auth the MCP and CLI rely on
// (ADR 0050 D9) — the client never mints a token, it forwards the caller's.
func TestNewInjectsBearerToken(t *testing.T) {
	var gotAuth, gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath = r.Header.Get("Authorization"), r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dags":[],"total_entries":0}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "tok123")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.ListDagsWithResponse(context.Background(), nil); err != nil {
		t.Fatalf("ListDags: %v", err)
	}
	if gotAuth != "Bearer tok123" {
		t.Errorf("Authorization = %q, want %q", gotAuth, "Bearer tok123")
	}
	if gotPath != "/api/v2/dags" {
		t.Errorf("path = %q, want /api/v2/dags", gotPath)
	}
}

// TestNewOmitsAuthHeaderWithoutToken: an empty token leaves requests
// unauthenticated (dev/loopback), rather than sending an empty bearer.
func TestNewOmitsAuthHeaderWithoutToken(t *testing.T) {
	var hadAuth bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, hadAuth = r.Header["Authorization"]
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetHealthzWithResponse(context.Background()); err != nil {
		t.Fatalf("GetHealthz: %v", err)
	}
	if hadAuth {
		t.Error("no token must send no Authorization header")
	}
}

// TestNewTrimsTrailingSlash: a base URL with a trailing slash must not produce
// a double slash in the request path.
func TestNewTrimsTrailingSlash(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	c, err := New(srv.URL+"/", "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if _, err := c.GetHealthzWithResponse(context.Background()); err != nil {
		t.Fatalf("GetHealthz: %v", err)
	}
	if gotPath != "/healthz" {
		t.Errorf("path = %q, want /healthz (no double slash)", gotPath)
	}
}

// TestWithDagSourceVersionSetsTheQueryParameter: GetDagSource keeps its
// signature (the overlay drops version_number from the generated client), and
// WithDagSourceVersion sends it, so a caller can read a given version's source.
// Without the editor no version_number is sent: the server serves the newest.
func TestWithDagSourceVersionSetsTheQueryParameter(t *testing.T) {
	var gotQuery []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = append(gotQuery, r.URL.RawQuery)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":"# v1","dag_id":"etl","version_number":1}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	resp, err := c.GetDagSourceWithResponse(context.Background(), "etl", WithDagSourceVersion(1))
	if err != nil {
		t.Fatalf("GetDagSource: %v", err)
	}
	if resp.JSON200 == nil || resp.JSON200.VersionNumber == nil || *resp.JSON200.VersionNumber != 1 {
		t.Errorf("response = %s", resp.Body)
	}
	if _, err = c.GetDagSourceWithResponse(context.Background(), "etl"); err != nil {
		t.Fatalf("GetDagSource: %v", err)
	}
	if len(gotQuery) != 2 || gotQuery[0] != "version_number=1" || gotQuery[1] != "" {
		t.Errorf("queries = %q, want [version_number=1, empty]", gotQuery)
	}
}

// TestDryRunRegisterSendsTheRegisterToTheDryRunRoute: RegisterDagVersion
// with the DryRunRegister editor posts the same body and query to
// /api/v2/dags/{dag_id}/versions/dry_run, the route that registers nothing.
func TestDryRunRegisterSendsTheRegisterToTheDryRunRoute(t *testing.T) {
	var gotPath, gotQuery string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotQuery = r.URL.Path, r.URL.RawQuery
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"dry_run":true,"created":true}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "tok")
	if err != nil {
		t.Fatal(err)
	}
	paused := true
	resp, err := c.RegisterDagVersionWithResponse(context.Background(), "etl", &RegisterDagVersionParams{IsPaused: &paused},
		RegisterDagVersionJSONRequestBody{DagId: "etl"}, DryRunRegister)
	if err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/dags/etl/versions/dry_run" || gotQuery != "is_paused=true" {
		t.Errorf("request = %s?%s, want /api/v2/dags/etl/versions/dry_run?is_paused=true", gotPath, gotQuery)
	}
	if resp.JSON200 == nil || resp.JSON200.DryRun == nil || !*resp.JSON200.DryRun {
		t.Errorf("answer = %s, want dry_run true decoded", resp.Body)
	}
	// Without the editor the register goes where it always did.
	if _, err := c.RegisterDagVersionWithResponse(context.Background(), "etl", nil, RegisterDagVersionJSONRequestBody{DagId: "etl"}); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/api/v2/dags/etl/versions" {
		t.Errorf("a register went to %s", gotPath)
	}
	// A list of the versions has the same path: the editor refuses it.
	get, gerr := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL+"/api/v2/dags/etl/versions", http.NoBody)
	if gerr != nil {
		t.Fatal(gerr)
	}
	if err := DryRunRegister(context.Background(), get); err == nil || get.URL.Path != "/api/v2/dags/etl/versions" {
		t.Errorf("DryRunRegister on a GET: err=%v path=%s, want a refusal and the path untouched", err, get.URL.Path)
	}
}
