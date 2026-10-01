package cli

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// healthServer serves the three monitor endpoints health reads, with the
// scheduler status controllable so a test can force an unhealthy report.
func healthServer(t *testing.T, schedulerStatus string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/monitor/health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, apiclient.HealthInfo{
			Scheduler:    &apiclient.ComponentHealth{Status: strptr(schedulerStatus)},
			Metadatabase: &apiclient.ComponentHealth{Status: strptr("healthy")},
			DagProcessor: &apiclient.ComponentHealth{Status: strptr("healthy")},
			Triggerer:    &apiclient.ComponentHealth{Status: strptr("healthy")},
		})
	})
	mux.HandleFunc("/api/v2/monitor/executor", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, apiclient.ExecutorInfo{
			ExecutionModes:     &[]string{"kubernetes"},
			PodDispatchEnabled: boolptr(true),
			TaskNamespace:      strptr("leoflow-tasks"),
		})
	})
	mux.HandleFunc("/api/v2/version", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(t, w, apiclient.VersionInfo{Version: strptr("2.9.1"), GitVersion: strptr("abc1234")})
	})
	return httptest.NewServer(mux)
}

func TestFetchAdminHealthHealthy(t *testing.T) {
	srv := healthServer(t, "healthy")
	defer srv.Close()

	rep, err := fetchAdminHealth(context.Background(), newTestClient(t, srv.URL))
	if err != nil {
		t.Fatalf("fetchAdminHealth: %v", err)
	}
	if !rep.healthy {
		t.Errorf("healthy = false, want true")
	}
	if rep.executor == nil || rep.version == nil {
		t.Errorf("executor/version not captured: %+v", rep)
	}
}

func TestFetchAdminHealthUnhealthy(t *testing.T) {
	srv := healthServer(t, "unhealthy")
	defer srv.Close()

	rep, err := fetchAdminHealth(context.Background(), newTestClient(t, srv.URL))
	if err != nil {
		t.Fatalf("fetchAdminHealth: %v", err)
	}
	if rep.healthy {
		t.Errorf("healthy = true, want false when scheduler is unhealthy")
	}
}

func TestFetchAdminHealthNon200IsError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer srv.Close()

	if _, err := fetchAdminHealth(context.Background(), newTestClient(t, srv.URL)); err == nil {
		t.Errorf("expected error when health endpoint returns 503")
	}
}

// TestAdminHealthCommandExitsNonZero pins the post-deploy smoke-test contract:
// an unhealthy control plane makes `leoflow admin health` return an error (a
// non-zero process exit) while still printing the compact report.
func TestAdminHealthCommandExitsNonZero(t *testing.T) {
	srv := healthServer(t, "unhealthy")
	defer srv.Close()

	out, _, err := run(t, "admin", "health", "--server", srv.URL, "--token", "x")
	if err == nil {
		t.Errorf("expected non-nil error (non-zero exit) for unhealthy control plane")
	}
	if !strings.Contains(out, "UNHEALTHY") {
		t.Errorf("output = %q, want it to contain UNHEALTHY", out)
	}
}

func TestAdminHealthCommandHealthyExitsZero(t *testing.T) {
	srv := healthServer(t, "healthy")
	defer srv.Close()

	out, _, err := run(t, "admin", "health", "--server", srv.URL, "--token", "x")
	if err != nil {
		t.Fatalf("healthy admin health returned error: %v", err)
	}
	if !strings.Contains(out, "HEALTHY") || strings.Contains(out, "UNHEALTHY") {
		t.Errorf("output = %q, want HEALTHY and not UNHEALTHY", out)
	}
}
