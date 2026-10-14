package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// fakeGcloud writes a stand-in for gcloud that logs its argv, a copy of the
// build config it was given and the entries of the source archive, then
// exits with exit (0 for success). On failure it says why on stderr, as
// gcloud does.
func fakeGcloud(t *testing.T, exit int) (tool, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir = t.TempDir()
	tool = filepath.Join(dir, "gcloud")
	script := `#!/bin/sh
echo "$*" >> "` + dir + `/calls.log"
for a in "$@"; do
  case "$a" in
    --config=*) cp "${a#--config=}" "` + dir + `/config.json" ;;
    *.tar.gz) tar -tzf "$a" | sort > "` + dir + `/archive.txt"; tar -xzOf "$a" ` + remoteDockerfile + ` > "` + dir + `/Dockerfile" 2>/dev/null ;;
  esac
done
if [ ` + strconv.Itoa(exit) + ` -ne 0 ]; then
  echo "ERROR: (gcloud.builds.submit) You do not currently have an active account selected." >&2
  exit ` + strconv.Itoa(exit) + `
fi
echo "DONE"
`
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return tool, dir
}

// cloudRequest is a pushed, pinned build of one file and one directory.
func cloudRequest(t *testing.T) Request {
	t.Helper()
	src := t.TempDir()
	dockerfile := filepath.Join(src, ".leoflow.generated.Dockerfile")
	if err := os.WriteFile(dockerfile, []byte("FROM base\nCOPY dag.py /home/leoflow/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	dag := filepath.Join(src, "dag.py")
	if err := os.WriteFile(dag, []byte("print('dag')\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := request("linux/amd64")
	req.Dockerfile, req.ContextDir = dockerfile, src
	req.Push, req.Pin = true, true
	req.ContextFiles = []LayerFile{{Path: "lib", Dir: true}, {Path: "dag.py", Source: dag}}
	return req
}

// The remote path on Cloud Build: the listed context and the Dockerfile are
// packed as one archive, gcloud submits it with a build config that builds
// for the platform and has Cloud Build push the image, and the digest comes
// from the registry, never from gcloud's output (ADR 0075).
func TestCloudBuildSubmitsTheListedContextAndPinsFromTheRegistry(t *testing.T) {
	tool, dir := fakeGcloud(t, 0)
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc123", manifest: singleManifest}
	b := CloudBuild{Project: "acme-ci", Region: "us-central1", Tool: tool, Registry: reg}
	req := cloudRequest(t)

	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	argv := strings.Fields(readFile(t, filepath.Join(dir, "calls.log")))
	if len(argv) < 3 || argv[0] != "builds" || argv[1] != "submit" || !strings.HasSuffix(argv[2], ".tar.gz") {
		t.Fatalf("gcloud argv = %q, want builds submit <archive>.tar.gz first", argv)
	}
	for _, want := range []string{"--project=acme-ci", "--region=us-central1", "--quiet"} {
		if !slices.Contains(argv, want) {
			t.Errorf("gcloud argv = %q, want %s", argv, want)
		}
	}
	if got := readFile(t, filepath.Join(dir, "archive.txt")); got != remoteDockerfile+"\ndag.py\nlib/\n" {
		t.Errorf("archive entries =\n%s\nwant the Dockerfile and the listed context only", got)
	}
	if got := readFile(t, filepath.Join(dir, "Dockerfile")); !strings.Contains(got, "COPY dag.py") {
		t.Errorf("archived Dockerfile = %q, want the request's Dockerfile", got)
	}
	var cfg cloudBuildConfig
	if jerr := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "config.json"))), &cfg); jerr != nil {
		t.Fatalf("build config: %v", jerr)
	}
	wantArgs := "build --platform linux/amd64 --tag reg.io/team/etl:v1 --file " + remoteDockerfile + " ."
	if len(cfg.Steps) != 1 || cfg.Steps[0].Name != cloudBuildDockerImage || strings.Join(cfg.Steps[0].Args, " ") != wantArgs {
		t.Errorf("build steps = %+v, want one %s step running %q", cfg.Steps, cloudBuildDockerImage, wantArgs)
	}
	if strings.Join(cfg.Images, ",") != "reg.io/team/etl:v1" {
		t.Errorf("build images = %q, want Cloud Build to push the tag", cfg.Images)
	}
	if cfg.Options != nil {
		t.Errorf("build options = %+v, want none without a service account", cfg.Options)
	}
	if strings.Join(reg.calls, ",") != "reg.io/team/etl:v1" {
		t.Errorf("registry reads = %q, want the pushed tag once", reg.calls)
	}
	if res.Path != PathRemote || !res.Pushed || res.Ref != "reg.io/team/etl@sha256:abc123" {
		t.Errorf("result = %+v, want the remote path, pushed, pinned to the registry digest", res)
	}
	if !strings.Contains(req.Stdout.(interface{ String() string }).String(), "DONE") {
		t.Errorf("stdout = %q, want gcloud's own output", req.Stdout)
	}
}

// A custom service account needs a logging option in the build, or Cloud
// Build refuses it; the worker pool is passed as given.
func TestCloudBuildPassesTheServiceAccountAndWorkerPool(t *testing.T) {
	tool, dir := fakeGcloud(t, 0)
	sa := "projects/acme-ci/serviceAccounts/builder@acme-ci.iam.gserviceaccount.com"
	pool := "projects/acme-ci/locations/us-central1/workerPools/private"
	b := CloudBuild{Project: "acme-ci", ServiceAccount: sa, WorkerPool: pool, Tool: tool,
		Registry: &fakeRegistry{ref: "reg.io/team/etl@sha256:abc", manifest: singleManifest}}
	if _, err := b.Build(context.Background(), cloudRequest(t)); err != nil {
		t.Fatalf("Build: %v", err)
	}
	argv := strings.Fields(readFile(t, filepath.Join(dir, "calls.log")))
	for _, want := range []string{"--service-account=" + sa, "--worker-pool=" + pool} {
		if !slices.Contains(argv, want) {
			t.Errorf("gcloud argv = %q, want %s", argv, want)
		}
	}
	if slices.ContainsFunc(argv, func(a string) bool { return strings.HasPrefix(a, "--region") }) {
		t.Errorf("gcloud argv = %q, want no --region when none is set", argv)
	}
	var cfg cloudBuildConfig
	if jerr := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "config.json"))), &cfg); jerr != nil {
		t.Fatalf("build config: %v", jerr)
	}
	if cfg.Options == nil || cfg.Options.Logging != "CLOUD_LOGGING_ONLY" {
		t.Errorf("build options = %+v, want CLOUD_LOGGING_ONLY with a service account", cfg.Options)
	}
}

// A refused submit keeps gcloud's reason and says what to check: the
// sign-in, the project and the roles.
func TestCloudBuildFailureNamesTheLoginAndProject(t *testing.T) {
	tool, _ := fakeGcloud(t, 1)
	reg := &fakeRegistry{}
	b := CloudBuild{Project: "acme-ci", Tool: tool, Registry: reg}
	_, err := b.Build(context.Background(), cloudRequest(t))
	if err == nil {
		t.Fatal("Build succeeded, want the gcloud failure")
	}
	for _, want := range []string{"acme-ci", "gcloud auth login", "active account selected", "roles/cloudbuild.builds.editor"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
	if len(reg.calls) != 0 {
		t.Errorf("registry reads = %q, want none after a failed build", reg.calls)
	}
}

// gcloud missing is said before anything is packed, with how to install it.
func TestCloudBuildWithoutGcloudSaysHowToInstallIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := CloudBuild{Project: "acme-ci", Registry: &fakeRegistry{}}
	if err := b.Ready(); err == nil || !strings.Contains(err.Error(), "gcloud is not on PATH") || !strings.Contains(err.Error(), "cloud.google.com/sdk") {
		t.Errorf("Ready = %v, want gcloud missing and where to get it", err)
	}
	_, err := b.Build(context.Background(), cloudRequest(t))
	if err == nil || !strings.Contains(err.Error(), "gcloud is not on PATH") {
		t.Errorf("Build = %v, want gcloud missing", err)
	}
}

// Cloud Build pushes what it builds, and only an unlisted context would be
// uploaded whole: both are refused before gcloud runs.
func TestCloudBuildRefusesAnUnpushedOrUnlistedBuild(t *testing.T) {
	tool, dir := fakeGcloud(t, 0)
	b := CloudBuild{Project: "acme-ci", Tool: tool, Registry: &fakeRegistry{}}
	req := cloudRequest(t)
	req.Push, req.Pin = false, false
	if _, err := b.Build(context.Background(), req); err == nil || !strings.Contains(err.Error(), "push") {
		t.Errorf("unpushed Build = %v, want a refusal that names the push", err)
	}
	req = cloudRequest(t)
	req.ContextFiles = nil
	if _, err := b.Build(context.Background(), req); err == nil || !strings.Contains(err.Error(), "context") {
		t.Errorf("unlisted Build = %v, want a refusal that names the context", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "calls.log")); err == nil {
		t.Error("gcloud ran, want it never to run for a refused request")
	}
}

// Cloud Build runs amd64 workers and the backend sets up no emulation, so it
// declines any other platform and more than one, and says so.
func TestCloudBuildDeclinesOtherPlatforms(t *testing.T) {
	b := CloudBuild{Project: "acme-ci"}
	if why := b.Decline([]string{"linux/amd64"}); why != "" {
		t.Errorf("Decline(linux/amd64) = %q, want none", why)
	}
	for _, p := range [][]string{{"linux/arm64"}, {"linux/amd64", "linux/arm64"}} {
		if why := b.Decline(p); !strings.Contains(why, "linux/amd64 only") {
			t.Errorf("Decline(%v) = %q, want it to say linux/amd64 only", p, why)
		}
	}
}

// The builder line names the backend and where it builds.
func TestCloudBuildDescribe(t *testing.T) {
	if got := (CloudBuild{Project: "acme-ci"}).Describe(); got != "remote (cloud-build, project acme-ci)" {
		t.Errorf("Describe = %q", got)
	}
	if got := (CloudBuild{Project: "acme-ci", Region: "europe-west1"}).Describe(); got != "remote (cloud-build, project acme-ci, region europe-west1)" {
		t.Errorf("Describe = %q", got)
	}
}
