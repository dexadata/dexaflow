package build

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// envTool is a fake docker that logs its argv and the value of envVar in
// its environment, one call per line each.
func envTool(t *testing.T, envVar string) (tool, argvLog, envLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	tool = filepath.Join(dir, "docker")
	argvLog, envLog = filepath.Join(dir, "argv.log"), filepath.Join(dir, "env.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + argvLog + "\"\n" +
		"printf '%s\\n' \"$" + envVar + "\" >> \"" + envLog + "\"\n" +
		"exit 0\n"
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return tool, argvLog, envLog
}

// Build args reach the tool as --build-arg in name order, and secrets as
// --secret id=<id>,env=<var>: the tool reads the value from its own
// environment, so it is never in the argv, where ps and the build log see
// it. Env adds variables to that environment only.
func TestLocalDaemonPassesBuildArgsAndSecrets(t *testing.T) {
	const indexURL = "https://ci:s3cr3t@pypi.example.com/simple"
	tool, argvLog, envLog := envTool(t, "DEXAFLOW_BUILD_INDEX_URL")
	req := request("linux/amd64")
	req.BuildArgs = map[string]string{"ZONE": "b", "APP_ENV": "prod"}
	req.Secrets = []Secret{{ID: "github_token", Env: "GITHUB_TOKEN"}, {ID: "dexaflow_index_url", Env: "DEXAFLOW_BUILD_INDEX_URL"}}
	req.Env = []string{"DEXAFLOW_BUILD_INDEX_URL=" + indexURL}

	if _, err := (LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}).Build(context.Background(), req); err != nil {
		t.Fatalf("Build: %v", err)
	}
	argv := readLog(t, argvLog)
	want := "build --platform linux/amd64 --build-arg APP_ENV=prod --build-arg ZONE=b " +
		"--secret id=github_token,env=GITHUB_TOKEN --secret id=dexaflow_index_url,env=DEXAFLOW_BUILD_INDEX_URL " +
		"-t reg.io/team/etl:v1 -f /p/Dockerfile /p"
	if argv != want {
		t.Errorf("argv:\n%s\nwant:\n%s", argv, want)
	}
	if strings.Contains(argv, "s3cr3t") {
		t.Errorf("the secret reached the argv: %s", argv)
	}
	if got := readLog(t, envLog); got != indexURL {
		t.Errorf("the tool's environment holds %q, want the index URL", got)
	}
}

// Without Env the tool inherits the CLI's environment unchanged.
func TestLocalDaemonInheritsTheEnvironment(t *testing.T) {
	t.Setenv("GITHUB_TOKEN", "from-the-shell")
	tool, _, envLog := envTool(t, "GITHUB_TOKEN")
	if _, err := (LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}).Build(context.Background(), request()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if got := readLog(t, envLog); got != "from-the-shell" {
		t.Errorf("the tool's environment holds %q, want the shell's", got)
	}
}

// Cloud Build gets the build args in its docker step.
func TestCloudBuildConfigCarriesTheBuildArgs(t *testing.T) {
	req := Request{Image: "reg.io/team/etl:v1", Platforms: []string{"linux/amd64"}, BuildArgs: map[string]string{"B": "2", "A": "1"}}
	got := strings.Join(CloudBuild{Project: "acme-ci"}.config(req).Steps[0].Args, " ")
	want := "build --platform linux/amd64 --build-arg A=1 --build-arg B=2 --tag reg.io/team/etl:v1 --file " + remoteDockerfile + " ."
	if got != want {
		t.Errorf("step args = %q, want %q", got, want)
	}
}

// A secret is read from this machine's environment, which Cloud Build does
// not have: a request that carries one is refused before anything is sent,
// whoever selected the builder.
func TestCloudBuildRefusesASecret(t *testing.T) {
	req := Request{Image: "reg.io/team/etl:v1", Push: true, ContextFiles: []LayerFile{}, Secrets: []Secret{{ID: "tok", Env: "TOKEN"}}}
	_, err := CloudBuild{Project: "acme-ci", Tool: "/nonexistent/gcloud"}.Build(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "secret") {
		t.Fatalf("Build = %v, want a refusal naming the secret", err)
	}
}

// A build that must stay on this machine passes over the remote builder,
// and the builder line says why.
func TestSelectPassesOverTheRemoteBuilderForALocalOnlyBuild(t *testing.T) {
	tool, _ := fakeTool(t, "")
	o := Options{Tool: tool, Remote: &fakeRemote{t: t}, Push: true, NoSplit: "the project ships its own Dockerfile",
		LocalOnly: "build.secrets reach the build from this machine's environment"}
	b, reason := Select(context.Background(), o, "r.io/dags/etl:v1", []string{"linux/amd64"})
	if b.Path() != PathLocalDaemon {
		t.Fatalf("Select = %s, want the daemon", b.Describe())
	}
	if !strings.Contains(reason, "does not apply: build.secrets reach the build from this machine's environment") {
		t.Errorf("reason %q does not say why the remote builder does not apply", reason)
	}
}

// The dependency image is built with the secrets and environment of the
// build (the generated Dockerfile mounts the index secret for its pip
// install), and without the build args, which no generated Dockerfile reads.
func TestDependencyRequestKeepsTheSecrets(t *testing.T) {
	req := Request{Platforms: []string{"linux/arm64"}, BuildArgs: map[string]string{"A": "1"},
		Secrets: []Secret{{ID: "dexaflow_index_url", Env: "X"}}, Env: []string{"X=https://pypi.example.com/simple"}}
	got := depsRequest(req, "r.io/dags/etl:deps-1", "/d/Dockerfile", "/d")
	if len(got.Secrets) != 1 || got.Secrets[0].ID != "dexaflow_index_url" || strings.Join(got.Env, ",") != "X=https://pypi.example.com/simple" {
		t.Errorf("secrets=%+v env=%q, want the build's", got.Secrets, got.Env)
	}
	if got.BuildArgs != nil || got.Image != "r.io/dags/etl:deps-1" || !got.Push || !got.Pin || got.ContextFiles == nil {
		t.Errorf("request = %+v, want no build args, the dependency image pushed and pinned, an empty listed context", got)
	}
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// Every way the daemon pushes runs a build that carries the build args and
// the secrets, and only that build gets the index URL in its environment:
// buildx's one build-and-push, docker's and nerdctl's build then push, and
// podman's build of one image or of a manifest list.
func TestLocalDaemonPassesTheInputsOnEveryPushPath(t *testing.T) {
	const indexURL = "https://ci:s3cr3t@pypi.example.com/simple"
	const inputs = "--build-arg APP_ENV=prod --secret id=dexaflow_index_url,env=DEXAFLOW_BUILD_INDEX_URL"
	cases := map[string]struct {
		tool      pushTool
		platforms []string
	}{
		"docker buildx":         {pushTool{name: "docker", buildx: true, metadata: `{"containerimage.digest":"` + pushedDigest + `"}`}, []string{"linux/amd64"}},
		"docker without buildx": {pushTool{name: "docker", pushOutput: "v1: digest: " + pushedDigest + " size: 9\n"}, []string{"linux/amd64"}},
		"podman one platform":   {pushTool{name: "podman", digestFile: pushedDigest}, []string{"linux/amd64"}},
		"podman manifest list":  {pushTool{name: "podman", digestFile: pushedDigest}, []string{"linux/amd64", "linux/arm64"}},
		"nerdctl":               {pushTool{name: "nerdctl"}, []string{"linux/amd64"}},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			c.tool.envVar = "DEXAFLOW_BUILD_INDEX_URL"
			tool, log := c.tool.fake(t)
			req := pushRequest(true, c.platforms...)
			req.BuildArgs = map[string]string{"APP_ENV": "prod"}
			req.Secrets = []Secret{{ID: "dexaflow_index_url", Env: "DEXAFLOW_BUILD_INDEX_URL"}}
			req.Env = []string{"DEXAFLOW_BUILD_INDEX_URL=" + indexURL}
			reg := &fakeRegistry{ref: "reg.io/team/etl@" + pushedDigest, manifest: indexOf(c.platforms...)}
			if len(c.platforms) == 1 {
				reg.manifest = singleManifest
			}
			if _, err := (LocalDaemon{Tool: tool, Registry: reg}).Build(context.Background(), req); err != nil {
				t.Fatalf("Build: %v", err)
			}
			argv := calls(t, log)
			env := strings.Split(strings.TrimSuffix(readRaw(t, filepath.Join(filepath.Dir(log), "env.log")), "\n"), "\n")
			builds := 0
			for i, call := range argv {
				isBuild := strings.HasPrefix(call, "build ") || strings.HasPrefix(call, "buildx build ")
				if isBuild {
					builds++
					if !strings.Contains(call, inputs) {
						t.Errorf("build call %q does not carry %s", call, inputs)
					}
				}
				if strings.Contains(call, "s3cr3t") {
					t.Errorf("the index password reached the argv: %s", call)
				}
				got := ""
				if i < len(env) {
					got = env[i]
				}
				if isBuild && got != indexURL {
					t.Errorf("build call %q ran with the index URL %q, want it", call, got)
				}
				if !isBuild && got != "" {
					t.Errorf("call %q ran with the index URL in its environment", call)
				}
			}
			if builds != 1 {
				t.Errorf("tool calls = %q, want one build", argv)
			}
		})
	}
}

// readRaw reads a log this test's fake wrote, keeping its empty lines.
func readRaw(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path) //nolint:gosec // a path this test created
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// A tool that cannot take `--secret id=<id>,env=<var>` (the legacy docker
// builder has no --secret flag) fails with its own terse message; the error
// says what the secrets need. A failure without secrets does not mention
// them.
func TestLocalDaemonBuildFailureWithSecretsSaysWhatTheyNeed(t *testing.T) {
	tool, _ := fakeTool(t, "build")
	req := request("linux/amd64")
	req.Secrets = []Secret{{ID: "github_token", Env: "GITHUB_TOKEN"}}
	_, err := LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "--secret id=<id>,env=<variable>") || !strings.Contains(err.Error(), "BuildKit") {
		t.Errorf("Build = %v, want an error saying what build secrets need", err)
	}
	_, err = LocalDaemon{Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), request("linux/amd64"))
	if err == nil || strings.Contains(err.Error(), "--secret") {
		t.Errorf("Build = %v, want a failure that does not mention secrets", err)
	}
}
