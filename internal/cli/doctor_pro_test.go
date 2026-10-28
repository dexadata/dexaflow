package cli

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
)

// doctorAgainst runs `dexaflow doctor` from cwd against server ("" for
// none), with token in DEXAFLOW_TOKEN.
func doctorAgainst(t *testing.T, server, token string) string {
	t.Helper()
	return doctorAgainstDeps(t, nil, server, token)
}

// doctorAgainstDeps is doctorAgainst under d (testDeps when nil).
func doctorAgainstDeps(t *testing.T, d *cliDeps, server, token string) string {
	t.Helper()
	if token != "" {
		t.Setenv("DEXAFLOW_TOKEN", token)
	}
	args := []string{"doctor", "--config", emptyConfig(t)}
	if server != "" {
		args = append(args, "--server-url", server)
	}
	out, _, err := runDeps(t, d, args...)
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	return out
}

// TestDoctorWithoutATargetSkipsTheProChecks: with no control plane set,
// doctor says how to get the Pro checks and calls nothing.
func TestDoctorWithoutATargetSkipsTheProChecks(t *testing.T) {
	isolateCLIEnv(t)
	out := doctorAgainst(t, "", "")
	if !strings.Contains(out, "pro checks    skipped: no target set") || !strings.Contains(out, "dexaflow target use") {
		t.Errorf("doctor does not say the Pro checks were skipped:\n%s", out)
	}
}

// TestDoctorChecksTheControlPlaneOfTheTarget: with a target, doctor runs the
// deploy preflight's control plane check and reports it, refusals included
// (#1705).
func TestDoctorChecksTheControlPlaneOfTheTarget(t *testing.T) {
	for _, tc := range []struct {
		name string
		fake *deployServer
		want []string
	}{
		{"may deploy", &deployServer{}, []string{"control plane ", "answers, the token may deploy"}},
		{"refused", &deployServer{probeStatus: http.StatusForbidden, refusal: "missing scope dexaflow:deploy"},
			[]string{"control plane ", "FAIL", "missing scope dexaflow:deploy", "dexaflow auth create-token --scope deploy"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			srv := httptest.NewServer(tc.fake)
			t.Cleanup(srv.Close)
			out := doctorAgainst(t, srv.URL, "tok")
			for _, w := range tc.want {
				if !strings.Contains(out, w) {
					t.Errorf("doctor does not say %q:\n%s", w, out)
				}
			}
			if tc.fake.probes != 1 || tc.fake.registers != 0 {
				t.Errorf("probes=%d registers=%d, want one probe and no register", tc.fake.probes, tc.fake.registers)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		isolateCLIEnv(t)
		out := doctorAgainst(t, "http://127.0.0.1:1", "tok")
		if !strings.Contains(out, "cannot reach the control plane at http://127.0.0.1:1") {
			t.Errorf("doctor does not say the server cannot be reached:\n%s", out)
		}
	})
}

// TestDoctorChecksTheRegistryPushOfTheProject: run in a DAG project, doctor
// checks the push to the project's repository, and names the login command
// when the registry refuses it.
func TestDoctorChecksTheRegistryPushOfTheProject(t *testing.T) {
	for _, deny := range []bool{false, true} {
		isolateCLIEnv(t)
		noDockerCredentials(t)
		reg := newFakeOCIRegistry(t, singleManifest)
		reg.denyPush = deny
		dir := versionProject(t, "registry:\n  url: "+reg.host+"/team\n  image_name: etl\n")
		t.Chdir(dir)
		srv := httptest.NewServer(&deployServer{})
		t.Cleanup(srv.Close)
		out := doctorAgainst(t, srv.URL, "tok")
		want := "registry      " + reg.host + "/team/etl accepts a push"
		if deny {
			want = "dexaflow registry login " + reg.host
		}
		if !strings.Contains(out, want) || reg.uploadCount() != 1 {
			t.Errorf("deny=%v: uploads=%d, doctor does not say %q:\n%s", deny, reg.uploadCount(), want, out)
		}
	}
}

// TestDoctorRefreshesAnExpiringSession: a saved session near its expiry is
// renewed as deploy renews it, so doctor reports what a deploy would get,
// not a FAIL for a token deploy would have refreshed.
func TestDoctorRefreshesAnExpiringSession(t *testing.T) {
	isolateCLIEnv(t)
	now := time.Now()
	old := testJWT(t, now.Add(-55*time.Minute), now.Add(5*time.Minute))
	fake := &deployServer{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/token/renew", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"renewed-jwt","token_type":"bearer","expires_in":3600}`))
	})
	mux.HandleFunc("/api/v2/dags/dexaflow-doctor/versions/dry_run", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer renewed-jwt" {
			http.Error(w, `{"detail":"token expired"}`, http.StatusUnauthorized)
			return
		}
		fake.ServeHTTP(w, r)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	cfg := filepath.Join(t.TempDir(), "config.yaml")
	if err := config.PersistSession(cfg, srv.URL, old); err != nil {
		t.Fatal(err)
	}
	out, _, err := run(t, "doctor", "--config", cfg)
	if err != nil {
		t.Fatalf("doctor: %v", err)
	}
	if !strings.Contains(out, "answers, the token may deploy") || fake.probes != 1 {
		t.Errorf("probes=%d, doctor did not check with the renewed token:\n%s", fake.probes, out)
	}
}

// TestDoctorDoesNotStallOnAServerThatHangs: each network check of doctor
// gives up after doctorCheckTimeout.
func TestDoctorDoesNotStallOnAServerThatHangs(t *testing.T) {
	isolateCLIEnv(t)
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() { close(release); srv.Close() })
	start := time.Now()
	out := doctorAgainst(t, srv.URL, "tok")
	if took := time.Since(start); took > doctorCheckTimeout+3*time.Second {
		t.Errorf("doctor took %v against a hung server, want about %v", took, doctorCheckTimeout)
	}
	if !strings.Contains(out, "cannot reach the control plane") {
		t.Errorf("doctor does not say the server did not answer:\n%s", out)
	}
}
