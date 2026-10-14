package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// noDockerCredentials points the registry keychain at an empty docker
// config, so no credential of the machine running the tests is used.
func noDockerCredentials(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"auths":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DOCKER_CONFIG", dir)
}

// compiled reports whether a deploy of dir got as far as the compile.
func compiled(dir string) bool {
	return fileExists(filepath.Join(dir, "dag.json"))
}

// TestDeployPreflightStopsBeforeTheCompile: a token the server refuses, a
// token that may not deploy and a server that cannot be reached stop the
// deploy before anything is compiled or built, naming what to fix (#1705).
func TestDeployPreflightStopsBeforeTheCompile(t *testing.T) {
	noDockerCredentials(t)
	for _, tc := range []struct {
		name   string
		fake   *deployServer
		server string
		want   []string
	}{
		{"refused token", &deployServer{probeStatus: http.StatusUnauthorized, refusal: "invalid token"}, "",
			[]string{"server returned 401", loginCommandPath}},
		{"token without deploy", &deployServer{probeStatus: http.StatusForbidden, refusal: "missing scope dexaflow:deploy"}, "",
			[]string{"may not deploy", "missing scope dexaflow:deploy", "dexaflow auth create-token --scope deploy"}},
		{"unreachable server", &deployServer{}, "http://127.0.0.1:1",
			[]string{"cannot reach the control plane at http://127.0.0.1:1"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, builder, log := deployFixture(t)
			server := tc.server
			if server == "" {
				srv := httptest.NewServer(tc.fake)
				t.Cleanup(srv.Close)
				server = srv.URL
			}
			out, _, err := run(t, "deploy", dir, "--server", server, "--token", "x", "--yes", "--builder", builder)
			if err == nil {
				t.Fatalf("deploy succeeded:\n%s", out)
			}
			for _, w := range tc.want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("error does not say %q:\n%v", w, err)
				}
			}
			if compiled(dir) || builderRan(log) || tc.fake.registers != 0 {
				t.Errorf("compiled=%v built=%v registers=%d, want nothing done", compiled(dir), builderRan(log), tc.fake.registers)
			}
		})
	}
}

// TestDeployDryRunRefusalStopsBeforeTheBuild: after the compile, the server's
// dry run of the spec refuses what the register would refuse, and the deploy
// stops before the build with the server's reason; a conflict keeps its
// hint.
func TestDeployDryRunRefusalStopsBeforeTheBuild(t *testing.T) {
	noDockerCredentials(t)
	for _, tc := range []struct {
		status  int
		refusal string
		want    string
	}{
		{http.StatusBadRequest, "dag \"etl\" declares unknown connection(s) warehouse", "warehouse"},
		{http.StatusBadRequest, "image img:v1 is not in the allowed images", "allowed images"},
		{http.StatusConflict, "version already registered with other content", "--dag-version"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			dir, builder, log := deployFixture(t)
			fake := &deployServer{dryRunStatus: tc.status, refusal: tc.refusal}
			out, _, err := deployDir(t, fake, dir, builder)
			if err == nil {
				t.Fatalf("deploy succeeded:\n%s", out)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "the server would refuse") {
				t.Errorf("error does not give the server's refusal %q:\n%v", tc.want, err)
			}
			if !compiled(dir) || builderRan(log) || fake.registers != 0 || fake.dryRuns != 1 {
				t.Errorf("compiled=%v built=%v registers=%d dryRuns=%d, want compiled, one dry run, no build", compiled(dir), builderRan(log), fake.registers, fake.dryRuns)
			}
		})
	}
}

// TestDeployPreflightPassesAndSaysSo: an accepted preflight prints its
// lines and the server's warnings, sends the dry run with the deploy's
// is_paused, then builds and registers.
func TestDeployPreflightPassesAndSaysSo(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, log := deployFixtureAt(t, reg.host+"/team")
	fake := &deployServer{dryRunWarnings: []string{`task "train" asks for 2 CPU, more than size 1`}}
	// --force builds, though the registry holds an image under every tag.
	out, errOut, err := deployDir(t, fake, dir, builder, "--paused", "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	for _, want := range []string{"preflight:  control plane ", "the token may deploy", "preflight:  the server would register etl"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
	if !strings.Contains(errOut, `warning: the server: task "train" asks for 2 CPU`) {
		t.Errorf("stderr does not carry the server's warning:\n%s", errOut)
	}
	if fake.probes != 1 || fake.dryRuns != 1 || fake.registers != 1 || !builderRan(log) {
		t.Errorf("probes=%d dryRuns=%d registers=%d built=%v, want one of each", fake.probes, fake.dryRuns, fake.registers, builderRan(log))
	}
	if len(fake.dryRunQueries) != 1 || !strings.Contains(fake.dryRunQueries[0], "dry_run=true") || !strings.Contains(fake.dryRunQueries[0], "is_paused=true") {
		t.Errorf("dry run queries = %q, want dry_run=true and the deploy's is_paused", fake.dryRunQueries)
	}
}

// TestDeployAgainstAServerWithoutDryRun: a server that predates the dry run
// is never sent the compiled spec with dry_run, which it would register
// with the tag instead of the digest; the deploy goes on, and says its
// checks run at register time.
func TestDeployAgainstAServerWithoutDryRun(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
	fake := &deployServer{noDryRun: true}
	out, errOut, err := deployDir(t, fake, dir, builder)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if fake.registers != 1 || len(fake.regQueries) != 1 || strings.Contains(fake.regQueries[0], "dry_run") {
		t.Errorf("registers=%d queries=%q, want one register, no dry run", fake.registers, fake.regQueries)
	}
	if !strings.Contains(errOut, "no dry run of the register") {
		t.Errorf("stderr does not say the server offers no dry run:\n%s", errOut)
	}
}

// TestDeployPreflightChecksTheRegistryPush: a registry that refuses a push
// stops the deploy before the build, naming the login command; nothing is
// checked when nothing is pushed (--skip-build), or when the registry
// already holds the image for the content label (the skip path).
func TestDeployPreflightChecksTheRegistryPush(t *testing.T) {
	noDockerCredentials(t)
	t.Run("refused", func(t *testing.T) {
		reg := newFakeOCIRegistry(t, singleManifest)
		reg.denyPush = true
		dir, builder, log := deployFixtureAt(t, reg.host+"/team")
		fake := &deployServer{}
		_, _, err := deployDir(t, fake, dir, builder, "--force")
		if err == nil || !strings.Contains(err.Error(), "docker login "+reg.host) || !strings.Contains(err.Error(), reg.host+"/team/etl") {
			t.Fatalf("err = %v, want the refused repository and docker login %s", err, reg.host)
		}
		if compiled(dir) || builderRan(log) || fake.registers != 0 {
			t.Errorf("compiled=%v built=%v registers=%d, want nothing done", compiled(dir), builderRan(log), fake.registers)
		}
	})
	t.Run("allowed", func(t *testing.T) {
		reg := newFakeOCIRegistry(t, singleManifest)
		dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
		out, _, err := deployDir(t, &deployServer{}, dir, builder, "--force")
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, out)
		}
		if !strings.Contains(out, "preflight:  "+reg.host+"/team/etl accepts a push") || reg.uploadCount() != 1 {
			t.Errorf("uploads=%d, output does not say the push is allowed:\n%s", reg.uploadCount(), out)
		}
	})
	for _, args := range [][]string{{"--skip-build"}, nil} {
		t.Run(strings.Join(append([]string{"nothing to push"}, args...), " "), func(t *testing.T) {
			reg := newFakeOCIRegistry(t, singleManifest)
			reg.denyPush = true
			dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
			out, _, err := deployDir(t, &deployServer{}, dir, builder, args...)
			if err != nil {
				t.Fatalf("deploy %v: %v\n%s", args, err, out)
			}
			if reg.uploadCount() != 0 {
				t.Errorf("deploy %v checked the push (%d uploads), want none", args, reg.uploadCount())
			}
		})
	}
}

// TestDeployPreflightFailureInTheJSONReport: --output json reports a
// preflight refusal as the DAG's failure, on stdout, and the human lines go
// to stderr.
func TestDeployPreflightFailureInTheJSONReport(t *testing.T) {
	noDockerCredentials(t)
	dir, builder, _ := deployFixture(t)
	fake := &deployServer{probeStatus: http.StatusForbidden, refusal: "missing permission deploy:dag"}
	out, _, err := deployDir(t, fake, dir, builder, "--output", "json")
	if err == nil {
		t.Fatal("deploy succeeded")
	}
	var report deployReport
	if jerr := json.Unmarshal([]byte(out), &report); jerr != nil {
		t.Fatalf("stdout is not the JSON report: %v\n%s", jerr, out)
	}
	if len(report.DAGs) != 1 || report.DAGs[0].Status != deployStatusFailed || !strings.Contains(report.DAGs[0].Error, "missing permission deploy:dag") {
		t.Errorf("report = %+v, want etl failed with the refusal", report)
	}
}

// TestServerDryRunRefusesAnAnswerWithoutTheHeader: an answer to a dry run
// without the dry run header comes from a server that may have registered
// the spec; the deploy stops rather than build on it.
func TestServerDryRunRefusesAnAnswerWithoutTheHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"created":true}`))
	}))
	t.Cleanup(srv.Close)
	spec := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(spec, []byte(`{"dag_id":"etl"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, warn strings.Builder
	err := serverDryRun(t.Context(), &out, &warn, versionPost{serverURL: srv.URL, dagID: "etl"}, spec, "v1", labelDefault)
	if err == nil || !strings.Contains(err.Error(), "may have registered") {
		t.Fatalf("err = %v, want a stop naming the missing header", err)
	}
}
