package cli

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// noDockerCredentials points the registry keychain at an empty docker
// config, so no credential of the machine running the tests is used.
func noDockerCredentials(t *testing.T) {
	t.Helper()
	dockerCredentialsFor(t, "")
}

// dockerCredentialsFor points the registry keychain at a docker config with
// a credential for host ("" for none).
func dockerCredentialsFor(t *testing.T, host string) {
	t.Helper()
	dir := t.TempDir()
	cfg := `{"auths":{}}`
	if host != "" {
		cfg = `{"auths":{"` + host + `":{"auth":"` + base64.StdEncoding.EncodeToString([]byte("bot:secret")) + `"}}}`
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(cfg), 0o600); err != nil {
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
		args   []string
		want   []string
	}{
		{"refused token", &deployServer{probeStatus: http.StatusUnauthorized, refusal: "invalid token"}, "", nil,
			[]string{"server returned 401", loginCommandPath}},
		{"token without deploy", &deployServer{probeStatus: http.StatusForbidden, refusal: "missing scope dexaflow:deploy"}, "", nil,
			[]string{"may not deploy", "missing scope dexaflow:deploy", "dexaflow auth create-token --scope deploy"}},
		{"token without deploy, --paused", &deployServer{probeStatus: http.StatusForbidden, refusal: "missing scope dexaflow:deploy"}, "", []string{"--paused"},
			[]string{"may not deploy", "--paused", "dexaflow:run"}},
		{"deploys through the trusted issuer", &deployServer{probeStatus: http.StatusForbidden, viaIssuer: true,
			refusal: "deploys go through the trusted issuer: use its deploy token"}, "", nil,
			[]string{"takes deploys only through its trusted issuer", "deploys go through the trusted issuer", "deploy audience"}},
		{"unreachable server", &deployServer{}, "http://127.0.0.1:1", nil,
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
			args := append([]string{"deploy", dir, "--server", server, "--token", "x", "--yes", "--builder", builder}, tc.args...)
			out, _, err := run(t, args...)
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
// dry run refuses what the register would refuse whatever the image built
// next, and the deploy stops before the build with the server's reason.
func TestDeployDryRunRefusalStopsBeforeTheBuild(t *testing.T) {
	noDockerCredentials(t)
	for _, tc := range []struct {
		status  int
		refusal string
		want    string
	}{
		{http.StatusBadRequest, "dag \"etl\" declares unknown connection(s) warehouse", "warehouse"},
		{http.StatusBadRequest, "image is not in the allowed images", "allowed images"},
		{http.StatusForbidden, "the limit max_dags of 1 is reached", "max_dags"},
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

// TestDeployDryRunOfAnImageNotYetBuilt: before a build the digest is not
// known, so the dry run is sent the repository pinned to a placeholder
// digest (the shape the register gets), and a conflict, which the built
// image decides, is a note: the deploy builds and registers.
func TestDeployDryRunOfAnImageNotYetBuilt(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, log := deployFixtureAt(t, reg.host+"/team")
	fake := &deployServer{dryRunStatus: http.StatusConflict, refusal: "version already registered with other content"}
	// --force builds although the registry holds an image under every tag.
	out, errOut, err := deployDir(t, fake, dir, builder, "--force", "--dag-version", "v9")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !builderRan(log) || fake.registers != 1 {
		t.Errorf("built=%v registers=%d, want a build and a register", builderRan(log), fake.registers)
	}
	if !strings.Contains(errOut, "note: the server would refuse this register") || !strings.Contains(errOut, "image built now decides") {
		t.Errorf("stderr does not carry the conflict as a note:\n%s", errOut)
	}
	t.Run("placeholder digest", func(t *testing.T) {
		// A registry with no image: nothing to resolve before the build (the
		// deploy then fails to read the pushed digest, which is not checked).
		dir, builder, _ := deployFixture(t)
		fake := &deployServer{}
		if out, _, _ := deployDir(t, fake, dir, builder); fake.dryRuns != 1 {
			t.Fatalf("dryRuns=%d, want one\n%s", fake.dryRuns, out)
		}
		want := "/team/etl@sha256:" + strings.Repeat("0", 64)
		if len(fake.dryRunImages) != 1 || !strings.HasSuffix(fake.dryRunImages[0], want) {
			t.Errorf("dry run images = %q, want the repository at the placeholder digest", fake.dryRunImages)
		}
	})
}

// TestDeploySkipBuildOfARegisteredLabelDeploys: --skip-build registers the
// image the registry holds, so its dry run is sent that digest: re-running
// it for a label registered with that image answers unchanged, not 409, and
// the deploy goes through; another image under the label still stops.
func TestDeploySkipBuildOfARegisteredLabelDeploys(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, log := deployFixtureAt(t, reg.host+"/team")
	fake := &deployServer{registeredImage: reg.host + "/team/etl@" + reg.digest}
	out, _, err := deployDir(t, fake, dir, builder, "--skip-build", "--dag-version", "v9")
	if err != nil {
		t.Fatalf("deploy --skip-build of a registered label: %v\n%s", err, out)
	}
	if fake.dryRuns != 1 || fake.registers != 1 || builderRan(log) {
		t.Errorf("dryRuns=%d registers=%d built=%v, want one dry run, one register, no build", fake.dryRuns, fake.registers, builderRan(log))
	}
	if !strings.Contains(out, "the server would register etl v9 as unchanged") {
		t.Errorf("output does not say the content is unchanged:\n%s", out)
	}
	other := &deployServer{registeredImage: reg.host + "/team/etl@sha256:" + strings.Repeat("1", 64)}
	_, _, err = deployDir(t, other, dir, builder, "--skip-build", "--dag-version", "v9")
	if err == nil || !strings.Contains(err.Error(), "--dag-version") || other.registers != 0 {
		t.Errorf("err = %v registers=%d, want the conflict to stop a --skip-build deploy", err, other.registers)
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
	if len(fake.dryRunQueries) != 1 || fake.dryRunQueries[0] != "is_paused=true" {
		t.Errorf("dry run queries = %q, want the deploy's is_paused", fake.dryRunQueries)
	}
}

// TestDeployPrintsEachServerWarningOnce: the register's answer repeats what
// the preflight's dry run warned about and may add more (#1703); the deploy
// prints each warning once on stderr.
func TestDeployPrintsEachServerWarningOnce(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
	const misfit, unpinned = `task "train" asks for 2 CPU, more than size 1`, `image "x" is not pinned by digest`
	fake := &deployServer{dryRunWarnings: []string{misfit}, registerWarnings: []string{misfit, unpinned}}
	out, errOut, err := deployDir(t, fake, dir, builder, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	for _, w := range []string{misfit, unpinned} {
		if n := strings.Count(errOut, "warning: the server: "+w); n != 1 {
			t.Errorf("stderr carries %q %d times, want once:\n%s", w, n, errOut)
		}
	}
}

// TestDeployAgainstAServerWithoutDryRun: a server that predates the dry run
// route answers it 404 and registers nothing; the deploy goes on, says its
// checks run at register time, and registers once.
func TestDeployAgainstAServerWithoutDryRun(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
	fake := &deployServer{noDryRun: true}
	out, errOut, err := deployDir(t, fake, dir, builder)
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if fake.registers != 1 || fake.dryRuns != 0 {
		t.Errorf("registers=%d dryRuns=%d, want one register, no dry run", fake.registers, fake.dryRuns)
	}
	if !strings.Contains(errOut, "offers no dry run of the register") {
		t.Errorf("stderr does not say the server offers no dry run:\n%s", errOut)
	}
}

// TestDeployPreflightChecksTheRegistryPush: a registry that refuses a push
// with the docker config's credential stops a docker deploy before the
// build, naming the login command. With no credential, or another builder,
// whose push may use credentials the docker config does not hold, the
// refusal is a note. Nothing is checked when nothing is pushed
// (--skip-build), or when the registry already holds the image for the
// content label (the skip path).
func TestDeployPreflightChecksTheRegistryPush(t *testing.T) {
	t.Run("refused, docker with a credential", func(t *testing.T) {
		reg := newFakeOCIRegistry(t, singleManifest)
		reg.denyPush = true
		dockerCredentialsFor(t, reg.host)
		dir := versionProject(t, "registry:\n  url: "+reg.host+"/team\n  image_name: etl\n")
		t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
		builder, log := argvBuilder(t)
		fake := &deployServer{}
		_, _, err := deployDir(t, fake, dir, builder, "--force")
		if err == nil || !strings.Contains(err.Error(), "dexaflow registry login "+reg.host) || !strings.Contains(err.Error(), reg.host+"/team/etl") {
			t.Fatalf("err = %v, want the refused repository and dexaflow registry login %s", err, reg.host)
		}
		if compiled(dir) || argvLines(t, log) != nil || fake.registers != 0 {
			t.Errorf("compiled=%v builder=%q registers=%d, want nothing done", compiled(dir), argvLines(t, log), fake.registers)
		}
	})
	for _, tc := range []struct {
		name, cred, login string
	}{
		{"refused, no credential", "", "dexaflow registry login"},
		{"refused, another builder", "cred", "builder.sh login"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newFakeOCIRegistry(t, singleManifest)
			reg.denyPush = true
			if tc.cred != "" {
				dockerCredentialsFor(t, reg.host)
			} else {
				noDockerCredentials(t)
			}
			dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
			if tc.cred == "" {
				// The docker builder, with nothing in the docker config.
				builder, _ = argvBuilder(t)
			}
			fake := &deployServer{}
			out, errOut, err := deployDir(t, fake, dir, builder, "--force")
			if err != nil {
				t.Fatalf("deploy: %v\n%s", err, out)
			}
			if !strings.Contains(errOut, "note: ") || !strings.Contains(errOut, tc.login+" "+reg.host) || fake.registers != 1 {
				t.Errorf("registers=%d, stderr does not carry the refusal as a note naming %q:\n%s", fake.registers, tc.login, errOut)
			}
		})
	}
	t.Run("allowed", func(t *testing.T) {
		noDockerCredentials(t)
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
			noDockerCredentials(t)
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

// TestServerDryRunOfAnOlderReplicaIsANote: during a rolling upgrade the dry
// run may reach a replica that predates the route; it answers 404 and
// registers nothing, and the deploy goes on with a note.
func TestServerDryRunOfAnOlderReplicaIsANote(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/versions/dry_run") {
			t.Errorf("dry run sent to %s", r.URL.Path)
		}
		http.NotFound(w, r)
	}))
	t.Cleanup(srv.Close)
	spec := filepath.Join(t.TempDir(), "dag.json")
	if err := os.WriteFile(spec, []byte(`{"dag_id":"etl","image":"r/etl:v1"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	var out, warn strings.Builder
	_, err := serverDryRun(t.Context(), &out, &warn, versionPost{serverURL: srv.URL, dagID: "etl"}, spec, dryImage{ref: "r/etl@sha256:" + strings.Repeat("0", 64)}, "v1", labelDefault)
	if err != nil || !strings.Contains(warn.String(), "note: ") || !strings.Contains(warn.String(), "404") {
		t.Fatalf("err = %v, warn = %q; want a note naming the 404", err, warn.String())
	}
}

// TestDeployDryRunOfAReusedImageHasItsDigest: on the skip path the registry
// already holds the image for the content label and the register gets that
// digest, so the dry run is sent it too, and an allowlist naming exactly that
// digest lets the deploy through.
func TestDeployDryRunOfAReusedImageHasItsDigest(t *testing.T) {
	noDockerCredentials(t)
	dir, reg := platformProjectWith(t, amd64Only, "")
	builder, log := argvBuilder(t)
	want := reg.host + "/team/etl@" + reg.digest
	fake := &deployServer{policy: imagePolicy(t, want)}
	out, errOut, err := deployDir(t, fake, dir, builder)
	if err != nil {
		t.Fatalf("deploy of a reused image allowlisted by digest: %v\n%s", err, out)
	}
	if !strings.Contains(out, "registry already holds the image") || argvLines(t, log) != nil || fake.registers != 1 {
		t.Fatalf("builder=%q registers=%d, want the skip path and one register:\n%s", argvLines(t, log), fake.registers, out)
	}
	if len(fake.dryRunImages) != 1 || fake.dryRunImages[0] != want || strings.Contains(errOut, "note: ") {
		t.Errorf("dry run images %q, want the reused digest %s and no note:\n%s", fake.dryRunImages, want, errOut)
	}
}

// TestDeployDryRunImageRefusalBeforeABuildIsANote: before a build the dry run
// has a placeholder digest, so a refusal of that image (an allowlist naming
// one exact digest, which a reproducible build may make) is left to the
// register: a note, and the deploy builds.
func TestDeployDryRunImageRefusalBeforeABuildIsANote(t *testing.T) {
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir, builder, log := deployFixtureAt(t, reg.host+"/team")
	fake := &deployServer{policy: imagePolicy(t, reg.host+"/team/etl@"+reg.digest)}
	out, errOut, err := deployDir(t, fake, dir, builder, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !builderRan(log) || fake.registers != 1 || !strings.Contains(errOut, "note: the server would refuse this register") ||
		!strings.Contains(errOut, "image built now decides") {
		t.Errorf("built=%v registers=%d, stderr does not carry the image refusal as a note:\n%s", builderRan(log), fake.registers, errOut)
	}
}

// TestDeployDryRunImageRefusalTheDigestCannotChangeStops: before a build, an
// image refusal stops the deploy unless the real digest could still turn it,
// that is unless the allowlist names this repository at a digest and the
// image is all the server refuses. A repository the allowlist does not cover,
// or a refusal that comes with another one, fails the register whatever the
// build makes.
func TestDeployDryRunImageRefusalTheDigestCannotChangeStops(t *testing.T) {
	noDockerCredentials(t)
	for _, tc := range []struct {
		name  string
		extra string
		cfg   func(host, digest string) domain.ExecutionPolicyConfig
		want  string
	}{
		{"another repository", "", func(host, _ string) domain.ExecutionPolicyConfig {
			return domain.ExecutionPolicyConfig{Images: []string{host + "/team-b/*"}}
		}, "execution_allowlist.images"},
		{"a digest of another repository", "", func(host, digest string) domain.ExecutionPolicyConfig {
			return domain.ExecutionPolicyConfig{Images: []string{host + "/team/etl-old@" + digest}}
		}, "execution_allowlist.images"},
		{"with a field refusal", "defaults:\n  node_selector:\n    disktype: ssd\n", func(host, digest string) domain.ExecutionPolicyConfig {
			return domain.ExecutionPolicyConfig{Images: []string{host + "/team/etl@" + digest}, Fields: []string{"none"}}
		}, "execution.node_selector"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg := newFakeOCIRegistry(t, singleManifest)
			dir := versionProject(t, "registry:\n  url: "+reg.host+"/team\n  image_name: etl\n"+tc.extra)
			t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
			builder, log := argvBuilder(t)
			policy, err := domain.ParseExecutionPolicy(tc.cfg(reg.host, reg.digest))
			if err != nil {
				t.Fatal(err)
			}
			fake := &deployServer{policy: policy}
			_, errOut, err := deployDir(t, fake, dir, builder, "--force")
			if err == nil || !strings.Contains(err.Error(), "preflight: the server would refuse this register") || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err = %v, want the preflight to stop on %s\n%s", err, tc.want, errOut)
			}
			if fake.dryRuns != 1 || argvLines(t, log) != nil || fake.registers != 0 {
				t.Errorf("dry runs=%d builder=%q registers=%d, want one dry run and nothing built or registered", fake.dryRuns, argvLines(t, log), fake.registers)
			}
		})
	}
}

// imagePolicy is an execution allowlist of images only.
func imagePolicy(t *testing.T, images ...string) *domain.ExecutionPolicy {
	t.Helper()
	p, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Images: images})
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestDeployDryRunServerErrorIsANote: a dry run the server could not answer
// (a 5xx, or 429 under load) says nothing about the register: a note.
func TestDeployDryRunServerErrorIsANote(t *testing.T) {
	noDockerCredentials(t)
	for _, status := range []int{http.StatusInternalServerError, http.StatusServiceUnavailable, http.StatusTooManyRequests} {
		reg := newFakeOCIRegistry(t, singleManifest)
		dir, builder, _ := deployFixtureAt(t, reg.host+"/team")
		fake := &deployServer{dryRunStatus: status, refusal: "busy"}
		out, errOut, err := deployDir(t, fake, dir, builder, "--force")
		if err != nil || fake.registers != 1 || !strings.Contains(errOut, "note: ") {
			t.Errorf("%d: err=%v registers=%d, want a note and the deploy through\n%s\n%s", status, err, fake.registers, out, errOut)
		}
	}
}
