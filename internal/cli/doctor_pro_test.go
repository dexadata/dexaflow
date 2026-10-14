package cli

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// doctorAgainst runs `dexaflow doctor` from cwd against server ("" for
// none), with token in DEXAFLOW_TOKEN.
func doctorAgainst(t *testing.T, server, token string) string {
	t.Helper()
	if token != "" {
		t.Setenv("DEXAFLOW_TOKEN", token)
	}
	args := []string{"doctor", "--config", emptyConfig(t)}
	if server != "" {
		args = append(args, "--server-url", server)
	}
	out, _, err := run(t, args...)
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
			want = "docker login " + reg.host
		}
		if !strings.Contains(out, want) || reg.uploadCount() != 1 {
			t.Errorf("deny=%v: uploads=%d, doctor does not say %q:\n%s", deny, reg.uploadCount(), want, out)
		}
	}
}
