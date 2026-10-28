package cli

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// clusterServer is a deploy fake that also advertises deploy defaults: body
// is the answer to GET /api/v2/deployDefaults ("" answers 404, as a server
// from before the route does), status overrides its code.
type clusterServer struct {
	*deployServer
	body   string
	status int

	mu        sync.Mutex
	asked     int
	gotBearer string
}

func (s *clusterServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet && r.URL.Path == "/api/v2/deployDefaults" {
		s.mu.Lock()
		s.asked++
		s.gotBearer = r.Header.Get("Authorization")
		s.mu.Unlock()
		switch {
		case s.status != 0:
			w.WriteHeader(s.status)
		case s.body == "":
			w.WriteHeader(http.StatusNotFound)
		default:
			_, _ = w.Write([]byte(s.body))
		}
		return
	}
	s.deployServer.ServeHTTP(w, r)
}

// clusterProject is a project with no registry, platforms or base image of
// its own, outside any workspace, with a stub builder and parser.
func clusterProject(t *testing.T, extraYAML string) (dir, builder, log string) {
	t.Helper()
	isolateCLIEnv(t)
	dir = versionProject(t, extraYAML)
	bin := t.TempDir()
	log = filepath.Join(bin, "builder.log")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(bin, "parser.log")))
	return dir, stubBuilder(t, bin, log, ""), log
}

func clusterBody(registry string) string {
	return `{"registry":"` + registry + `","platforms":["linux/arm64"],"base_image":"ghcr.io/acme/base:2"}`
}

// TestDeployAppliesTheClusterDefaults: a project that names no registry
// deploys to the one the cluster advertises, builds for its platforms on its
// base image, and the deploy says each value came from the cluster.
func TestDeployAppliesTheClusterDefaults(t *testing.T) {
	dir, stub, log := clusterProject(t, "")
	reg := missingRegistry(t) + "/team"
	fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: clusterBody(reg)}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --plan: %v\n%s", err, out)
	}
	from := "(from the deploy defaults of " + srv.URL + ")"
	for _, want := range []string{
		"registry:   " + reg + "/etl " + from,
		"platforms:  linux/arm64 " + from,
		"base image: ghcr.io/acme/base:2 " + from,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
	if fake.gotBearer != "Bearer tok" {
		t.Errorf("deployDefaults was read with Authorization %q, want the deploy's token", fake.gotBearer)
	}
	if builderRan(log) {
		t.Error("--plan ran the builder")
	}
}

// TestClusterDefaultsComeLast: every local source wins over the cluster, so
// a cluster that starts advertising defaults changes no deploy that already
// resolved a value.
func TestClusterDefaultsComeLast(t *testing.T) {
	cases := []struct {
		name     string
		project  string
		args     []string
		userCfg  string
		env      string
		wantFrom string
	}{
		{name: "project", project: "registry: {url: quay.io/me}\nbase_image: python:3.12-slim\nbuild: {platforms: [linux/amd64]}\n", wantFrom: "dexaflow.yaml"},
		{name: "flag", args: []string{"--registry", "quay.io/flag"}, wantFrom: "--registry"},
		{name: "environment", env: "quay.io/env", wantFrom: "DEXAFLOW_REGISTRY"},
		{name: "user config", userCfg: "registry: quay.io/user\n", wantFrom: "registry in "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir, stub, _ := clusterProject(t, tc.project)
			if tc.env != "" {
				t.Setenv("DEXAFLOW_REGISTRY", tc.env)
			}
			fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: clusterBody("ghcr.io/cluster")}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			cfg := filepath.Join(t.TempDir(), "config.yaml")
			writeTree(t, cfg, tc.userCfg)
			args := append([]string{"deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", cfg}, tc.args...)
			out, _, _ := run(t, args...)
			line := lineWith(out, "registry:   ")
			if strings.Contains(line, "ghcr.io/cluster") || !strings.Contains(line, tc.wantFrom) {
				t.Errorf("registry line = %q, want it from %s, not the cluster:\n%s", line, tc.wantFrom, out)
			}
			if tc.name == "project" {
				for _, l := range []string{lineWith(out, "platforms:  "), lineWith(out, "base image: ")} {
					if strings.Contains(l, "deploy defaults of") {
						t.Errorf("%q came from the cluster although the project sets it", l)
					}
				}
			}
		})
	}
}

// TestWorkspaceDefaultsWinOverTheCluster: a workspace's own defaults beat
// what the cluster advertises; the cluster still fills what they leave out.
func TestWorkspaceDefaultsWinOverTheCluster(t *testing.T) {
	isolateCLIEnv(t)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\ndefaults:\n  registry: quay.io/ws\n")
	project := filepath.Join(root, "dags", "etl")
	writeTree(t, filepath.Join(project, projectConfigFile), "dag_id: etl\n")
	writeTree(t, filepath.Join(project, "dag.py"), "# dag\n")
	bin := t.TempDir()
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(bin, "parser.log")))
	stub := stubBuilder(t, bin, filepath.Join(bin, "builder.log"), "")
	fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: clusterBody("ghcr.io/cluster")}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Chdir(project)

	out, _, _ := run(t, "deploy", ".", "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
	if line := lineWith(out, "registry:   "); !strings.Contains(line, "quay.io/ws/etl (from defaults in ") {
		t.Errorf("registry line = %q, want the workspace defaults", line)
	}
	if line := lineWith(out, "platforms:  "); !strings.Contains(line, "linux/arm64 (from the deploy defaults of "+srv.URL+")") {
		t.Errorf("platforms line = %q, want the cluster's, which the workspace leaves unset", line)
	}
}

// TestDeployToAServerWithoutClusterDefaults: a server from before the route
// answers 404, and the deploy resolves exactly as before: silently, from
// the local settings, with the same registry error when there are none.
func TestDeployToAServerWithoutClusterDefaults(t *testing.T) {
	dir, stub, _ := clusterProject(t, "registry: {url: quay.io/me}\n")
	fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, errOut, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --plan against an older server: %v\n%s", err, out)
	}
	if fake.asked != 1 {
		t.Errorf("deployDefaults asked %d times, want once", fake.asked)
	}
	if strings.Contains(out+errOut, "deploy defaults") {
		t.Errorf("a 404 must be silent:\nstdout:\n%s\nstderr:\n%s", out, errOut)
	}
	if line := lineWith(out, "platforms:  "); !strings.Contains(line, "linux/amd64 (from the default)") {
		t.Errorf("platforms line = %q, want the built-in default", line)
	}

	bare, stub2, _ := clusterProject(t, "")
	_, _, err = run(t, "deploy", bare, "--server", srv.URL, "--token", "tok", "--builder", stub2, "--plan", "--config", emptyConfig(t))
	if err == nil || !strings.Contains(err.Error(), "deploy requires a container registry") {
		t.Errorf("err = %v, want the registry guard", err)
	}
}

// TestUnusableClusterDefaultsAreIgnoredWithAWarning: an error answer or a
// value no deploy could use is not applied; the deploy warns and goes on
// with its local settings.
func TestUnusableClusterDefaultsAreIgnoredWithAWarning(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		status int
	}{
		{name: "server error", status: http.StatusInternalServerError},
		{name: "forbidden", status: http.StatusForbidden},
		{name: "not json", body: "<html>"},
		{name: "invalid registry", body: `{"registry":"https://ghcr.io/acme","platforms":[],"base_image":""}`},
		{name: "invalid platform", body: `{"registry":"","platforms":["x86"],"base_image":""}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir, stub, _ := clusterProject(t, "registry: {url: quay.io/me}\n")
			fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: tc.body, status: tc.status}
			srv := httptest.NewServer(fake)
			defer srv.Close()
			out, errOut, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
			if err != nil {
				t.Fatalf("deploy --plan: %v\n%s", err, out)
			}
			if !strings.Contains(errOut, "warning: ignoring the deploy defaults of "+srv.URL) {
				t.Errorf("stderr does not warn:\n%s", errOut)
			}
			if strings.Contains(out, "(from the deploy defaults of") {
				t.Errorf("an unusable answer was applied:\n%s", out)
			}
		})
	}
}

// TestDeployAllReadsTheClusterDefaultsOnce: one read per server, not one per
// DAG of the workspace.
func TestDeployAllReadsTheClusterDefaultsOnce(t *testing.T) {
	isolateCLIEnv(t)
	root := t.TempDir()
	writeTree(t, filepath.Join(root, workspaceFileName), "targets: {}\n")
	for _, id := range []string{"a", "b"} {
		p := filepath.Join(root, "dags", id)
		writeTree(t, filepath.Join(p, projectConfigFile), "dag_id: "+id+"\n")
		writeTree(t, filepath.Join(p, "dag.py"), "# dag\n")
	}
	bin := t.TempDir()
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(bin, "parser.log")))
	stub := stubBuilder(t, bin, filepath.Join(bin, "builder.log"), "")
	fake := &clusterServer{deployServer: &deployServer{status: http.StatusNotFound}, body: clusterBody(missingRegistry(t))}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Chdir(root)
	out, _, err := run(t, "deploy", "--all", "--server", srv.URL, "--token", "tok", "--builder", stub, "--plan", "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy --all --plan: %v\n%s", err, out)
	}
	if fake.asked != 1 {
		t.Errorf("deployDefaults asked %d times for two DAGs, want once", fake.asked)
	}
	if strings.Count(out, "(from the deploy defaults of "+srv.URL+")") < 2 {
		t.Errorf("both DAGs should take the cluster registry:\n%s", out)
	}
}

// lineWith returns the first line of out containing prefix, "" when none.
func lineWith(out, prefix string) string {
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, prefix) {
			return l
		}
	}
	return ""
}

// clusterPlatformsBody advertises linux/arm64 and a base image, no registry.
const clusterPlatformsBody = `{"registry":"","platforms":["linux/arm64"],"base_image":"ghcr.io/acme/base:2"}`

// builtTag returns the -t value of the first build call in the argv log.
func builtTag(t *testing.T, log string) string {
	t.Helper()
	for _, call := range argvLines(t, log) {
		f := strings.Fields(call)
		if len(f) == 0 || f[0] != "build" {
			continue
		}
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "-t" {
				return f[i+1]
			}
		}
	}
	t.Fatalf("no build call with -t in %q", argvLines(t, log))
	return ""
}

// TestBuildPushAndDeploySkipBuildAgreeOnTheLabel: the label hashes the
// resolved config, so `build --push` must apply the cluster's platforms and
// base image as deploy does, or `deploy --skip-build` looks up a tag the
// build never pushed.
func TestBuildPushAndDeploySkipBuildAgreeOnTheLabel(t *testing.T) {
	isolateCLIEnv(t)
	reg := newFakeOCIRegistry(t, amd64AndArm64)
	ws := t.TempDir()
	dir := filepath.Join(ws, "etl")
	writeTree(t, filepath.Join(dir, projectConfigFile), "dag_id: etl\nregistry:\n  url: "+reg.host+"/team\n  image_name: etl\n")
	writeTree(t, filepath.Join(dir, "dag.py"), "# dag\n")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	fake := &clusterServer{deployServer: &deployServer{}, body: clusterPlatformsBody}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Setenv("DEXAFLOW_SERVER_URL", srv.URL)
	t.Setenv("DEXAFLOW_TOKEN", "tok")
	cfg := emptyConfig(t)
	builder, log := argvBuilder(t)

	if out, _, err := run(t, "build", ws, "--push", "--builder", builder, "--config", cfg); err != nil {
		t.Fatalf("build --push: %v\n%s", err, out)
	}
	tag := builtTag(t, log)
	if !strings.Contains(strings.Join(argvLines(t, log), "\n"), "--platform linux/arm64") {
		t.Errorf("build --push did not build for the cluster's platform: %q", argvLines(t, log))
	}
	out, _, err := run(t, "deploy", dir, "--skip-build", "--yes", "--builder", builder, "--config", cfg)
	if err != nil {
		t.Fatalf("deploy --skip-build: %v\n%s", err, out)
	}
	if want := strings.TrimPrefix(tag, reg.host+"/"); !slices.Contains(reg.readList(), want) {
		t.Errorf("deploy --skip-build read %v, want the tag build pushed (%s)", reg.readList(), want)
	}
}

// TestBuildWithoutPushNeverAsksTheServer: a local build stays offline.
func TestBuildWithoutPushNeverAsksTheServer(t *testing.T) {
	isolateCLIEnv(t)
	ws := t.TempDir()
	writeBuildProject(t, ws, "alpha", "alpha-img")
	bin := t.TempDir()
	t.Setenv("LEOFLOW_PARSER_CMD", stubParser(t, bin))
	fake := &clusterServer{deployServer: &deployServer{}, body: clusterPlatformsBody}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	t.Setenv("DEXAFLOW_SERVER_URL", srv.URL)
	if _, err := runBuildCmd(t, "build", ws, "--builder", stubBuilder(t, bin, filepath.Join(bin, "argv.log"), ""), "--config", emptyConfig(t)); err != nil {
		t.Fatalf("build: %v", err)
	}
	if fake.asked != 0 {
		t.Errorf("a build without --push asked the server %d times", fake.asked)
	}
}

// TestDeployBuildsWithTheClusterPlatformAndBaseImage: past the plan, the
// cluster's platform reaches the builder and its base image the generated
// Dockerfile.
func TestDeployBuildsWithTheClusterPlatformAndBaseImage(t *testing.T) {
	isolateCLIEnv(t)
	dir, _ := platformProjectWith(t, amd64AndArm64, "")
	builder, log := argvBuilder(t)
	// The generated Dockerfile is removed after the build: the stub keeps a
	// copy of what it was handed with -f.
	seen := filepath.Join(t.TempDir(), "Dockerfile.seen")
	writeTree(t, builder, "#!/bin/sh\n"+noBuildx+"echo \"$*\" >> \""+log+"\"\n"+
		"while [ $# -gt 0 ]; do if [ \"$1\" = -f ]; then cp \"$2\" \""+seen+"\"; fi; shift; done\nexit 0\n")
	if err := os.Chmod(builder, 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	fake := &clusterServer{deployServer: &deployServer{}, body: clusterPlatformsBody}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "tok", "--yes", "--rebuild", "--builder", builder, "--config", emptyConfig(t))
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	calls := argvLines(t, log)
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 ") {
		t.Errorf("builder calls = %q, want a build for linux/arm64", calls)
	}
	if df := readProjectFile(t, filepath.Dir(seen), filepath.Base(seen)); !strings.Contains(df, "FROM ghcr.io/acme/base:2") {
		t.Errorf("generated Dockerfile does not use the cluster's base image:\n%s", df)
	}
	if fake.registers != 1 {
		t.Errorf("registers = %d, want 1", fake.registers)
	}
}

// TestDoctorChecksTheClusterRegistry: doctor checks the push to the registry
// a deploy would use, the cluster's included.
func TestDoctorChecksTheClusterRegistry(t *testing.T) {
	isolateCLIEnv(t)
	noDockerCredentials(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	dir := versionProject(t, "")
	t.Chdir(dir)
	fake := &clusterServer{deployServer: &deployServer{}, body: `{"registry":"` + reg.host + `/team","platforms":[],"base_image":""}`}
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	out := doctorAgainst(t, srv.URL, "tok")
	if want := "registry      " + reg.host + "/team/etl accepts a push"; !strings.Contains(out, want) || reg.uploadCount() != 1 {
		t.Errorf("uploads=%d, doctor does not say %q:\n%s", reg.uploadCount(), want, out)
	}
}
