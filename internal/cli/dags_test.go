package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestDeleteDag(t *testing.T) {
	for _, tc := range []struct {
		name        string
		deregister  bool
		wantQuery   string
		wantPathEnd string
	}{
		{"clear history", false, "", "/api/v2/dags/etl"},
		{"deregister", true, "deregister=true", "/api/v2/dags/etl"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var gotMethod, gotPath, gotQuery, gotAuth string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotMethod, gotPath, gotQuery, gotAuth = r.Method, r.URL.Path, r.URL.RawQuery, r.Header.Get("Authorization")
				w.WriteHeader(http.StatusNoContent)
			}))
			defer srv.Close()

			status, _, err := deleteDag(context.Background(), srv.URL, "tok", "etl", tc.deregister)
			if err != nil {
				t.Fatalf("deleteDag: %v", err)
			}
			if status != http.StatusNoContent {
				t.Errorf("status = %d", status)
			}
			if gotMethod != http.MethodDelete {
				t.Errorf("method = %s, want DELETE", gotMethod)
			}
			if gotPath != tc.wantPathEnd {
				t.Errorf("path = %s, want %s", gotPath, tc.wantPathEnd)
			}
			if gotQuery != tc.wantQuery {
				t.Errorf("query = %q, want %q", gotQuery, tc.wantQuery)
			}
			if gotAuth != "Bearer tok" {
				t.Errorf("auth = %q, want Bearer tok", gotAuth)
			}
		})
	}
}

// TestDagsDeleteUsesConfigToken pins the same contract as the runs commands:
// after `dexaflow auth login` the persisted JWT must be sent, so deleting a DAG
// does not fail with "missing bearer token".
func TestDagsDeleteUsesConfigToken(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")
	if _, _, err := run(t, "dags", "delete", "etl", "--config", cfgPath); err != nil {
		t.Fatalf("dags delete with a logged-in session: %v", err)
	}
	if gotAuth != "Bearer jwt-from-config" {
		t.Errorf("auth = %q, want the token persisted by login (Bearer jwt-from-config)", gotAuth)
	}
}

// TestDagsDeleteFlagTokenBeatsConfig pins the top of the precedence chain for
// `dags delete`: an explicit --token overrides the persisted session.
func TestDagsDeleteFlagTokenBeatsConfig(t *testing.T) {
	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	cfgPath := seedSessionConfig(t, srv.URL, "jwt-from-config")
	if _, _, err := run(t, "dags", "delete", "etl", "--config", cfgPath, "--token", "flag-token"); err != nil {
		t.Fatalf("dags delete --token: %v", err)
	}
	if gotAuth != "Bearer flag-token" {
		t.Errorf("auth = %q, want the explicit flag to win (Bearer flag-token)", gotAuth)
	}
}
