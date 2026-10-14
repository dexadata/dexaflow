package client

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestGetDagVersionSpecReadsTheRegisteredSpec: GetDagVersionSpec sends GET
// /api/v2/dags/{dag_id}/dagVersions/{n}/spec through the client's Doer,
// server URL and request editors, escapes the dag_id, and returns the body
// as it came, byte for byte (#1708).
func TestGetDagVersionSpecReadsTheRegisteredSpec(t *testing.T) {
	const spec = `{"dag_id":"a b","dag_version":"v1","image":"r/x@sha256:00","extra":{"kept":true}}`
	var gotAuth, gotPath, gotMethod, gotProbe string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth, gotPath, gotMethod, gotProbe = r.Header.Get("Authorization"), r.URL.EscapedPath(), r.Method, r.Header.Get("X-Probe")
		_, _ = w.Write([]byte(spec))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL+"/", WithRequestEditorFn(bearerAuth("tok")))
	if err != nil {
		t.Fatal(err)
	}
	probe := func(_ context.Context, req *http.Request) error {
		req.Header.Set("X-Probe", "1")
		return nil
	}
	got, err := c.GetDagVersionSpec(context.Background(), "a?b", 3, probe)
	if err != nil {
		t.Fatalf("GetDagVersionSpec: %v", err)
	}
	if string(got) != spec {
		t.Errorf("spec = %s, want the body as served: %s", got, spec)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v2/dags/a%3Fb/dagVersions/3/spec" || gotAuth != "Bearer tok" || gotProbe != "1" {
		t.Errorf("request = %s %s (Authorization %q, probe %q)", gotMethod, gotPath, gotAuth, gotProbe)
	}
}

// TestGetDagVersionSpecReportsTheStatus: an answer other than 200 is a
// *StatusError carrying the status and the body, so a caller can tell a 404
// (an unknown version, or a server from before the route) from a refusal.
func TestGetDagVersionSpecReportsTheStatus(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"detail":"version 9 not found"}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := c.GetDagVersionSpec(context.Background(), "etl", 9)
	var se *StatusError
	if spec != nil || !errors.As(err, &se) || se.StatusCode != http.StatusNotFound || string(se.Body) != `{"detail":"version 9 not found"}` {
		t.Fatalf("spec=%s err=%v, want a *StatusError with 404 and the body", spec, err)
	}
}
