package build

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

// fakeGcloud writes a stand-in for gcloud and returns it with its
// directory. Every call appends its argv to calls.log.
//   - `builds submit` copies the build config to config.json, lists the
//     archive in archive.txt and prints the build as gcloud --format=json
//     does; with submit-fails in the directory it fails as a signed-out
//     gcloud does;
//   - `builds log` prints a log line; with log-hangs in the directory it
//     waits, writing INT to signals when it gets SIGINT;
//   - `builds describe` prints describe.json (setDescribe);
//   - `builds cancel` only logs.
func fakeGcloud(t *testing.T) (tool, dir string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir = t.TempDir()
	tool = filepath.Join(dir, "gcloud")
	script := `#!/bin/sh
d="` + dir + `"
echo "$*" >> "$d/calls.log"
case "$1 $2" in
"builds submit")
  for a in "$@"; do
    case "$a" in
      --config=*) cp "${a#--config=}" "$d/config.json" ;;
      *.tar.gz) tar -tzf "$a" | sort > "$d/archive.txt"; tar -xzOf "$a" ` + remoteDockerfile + ` > "$d/Dockerfile" 2>/dev/null ;;
    esac
  done
  if [ -f "$d/submit-fails" ]; then
    echo "ERROR: (gcloud.builds.submit) You do not currently have an active account selected." >&2
    exit 1
  fi
  echo '{"id": "b-123", "logUrl": "https://console.cloud.google.com/cloud-build/builds/b-123", "status": "QUEUED"}'
  ;;
"builds log")
  trap 'echo INT >> "$d/signals"; exit 130' INT
  echo "Step #0: building the image"
  if [ -f "$d/log-hangs" ]; then
    : > "$d/log-started"
    while :; do sleep 0.05; done
  fi
  ;;
"builds describe")
  cat "$d/describe.json"
  ;;
esac
exit 0
`
	if err := os.WriteFile(tool, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	setDescribe(t, dir, "SUCCESS", "sha256:abc123")
	return tool, dir
}

// setDescribe makes `builds describe` report status, and digest for the
// image of request().
func setDescribe(t *testing.T, dir, status, digest string) {
	t.Helper()
	doc := `{"id": "b-123", "status": "` + status + `", "statusDetail": "step 0 exited 1",
		"logUrl": "https://console.cloud.google.com/cloud-build/builds/b-123",
		"results": {"images": [{"name": "reg.io/team/etl:v1", "digest": "` + digest + `"}]}}`
	if err := os.WriteFile(filepath.Join(dir, "describe.json"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

func touch(t *testing.T, p string) {
	t.Helper()
	if err := os.WriteFile(p, nil, 0o600); err != nil {
		t.Fatal(err)
	}
}

// gcloudCalls returns the logged argv lines, one per call.
func gcloudCalls(t *testing.T, dir string) []string {
	t.Helper()
	return calls(t, filepath.Join(dir, "calls.log"))
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

func outOf(w any) string { return w.(interface{ String() string }).String() }

// The remote path on Cloud Build: the listed context and the Dockerfile are
// packed as one archive, gcloud submits it asynchronously with a build
// config that builds for the platform and has Cloud Build push the image,
// the log is streamed, the finished build is described, and the result is
// pinned to the digest the registry holds, which must be the one the build
// reports (ADR 0075, ADR 0083).
func TestCloudBuildSubmitsTheListedContextAndPinsFromTheRegistry(t *testing.T) {
	tool, dir := fakeGcloud(t)
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc123", manifest: singleManifest}
	b := CloudBuild{Project: "acme-ci", Region: "us-central1", Tool: tool, Registry: reg}
	req := cloudRequest(t)

	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := gcloudCalls(t, dir)
	if len(got) != 3 {
		t.Fatalf("gcloud calls = %q, want submit, log and describe", got)
	}
	submit := strings.Fields(got[0])
	if len(submit) < 3 || submit[0] != "builds" || submit[1] != "submit" || !strings.HasSuffix(submit[2], ".tar.gz") {
		t.Fatalf("submit argv = %q, want builds submit <archive>.tar.gz first", submit)
	}
	for _, want := range []string{"--project=acme-ci", "--region=us-central1", "--async", "--format=json", "--quiet"} {
		if !slices.Contains(submit, want) {
			t.Errorf("submit argv = %q, want %s", submit, want)
		}
	}
	if got[1] != "builds log b-123 --stream --project=acme-ci --region=us-central1" {
		t.Errorf("log call = %q, want the build's log streamed", got[1])
	}
	if got[2] != "builds describe b-123 --format=json --project=acme-ci --region=us-central1" {
		t.Errorf("describe call = %q", got[2])
	}
	if a := readFile(t, filepath.Join(dir, "archive.txt")); a != remoteDockerfile+"\ndag.py\nlib/\n" {
		t.Errorf("archive entries =\n%s\nwant the Dockerfile and the listed context only", a)
	}
	if df := readFile(t, filepath.Join(dir, "Dockerfile")); !strings.Contains(df, "COPY dag.py") {
		t.Errorf("archived Dockerfile = %q, want the request's Dockerfile", df)
	}
	var cfg cloudBuildConfig
	if jerr := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "config.json"))), &cfg); jerr != nil {
		t.Fatalf("build config: %v", jerr)
	}
	wantArgs := "build --platform linux/amd64 --tag reg.io/team/etl:v1 --file " + remoteDockerfile + " ."
	if len(cfg.Steps) != 1 || cfg.Steps[0].Name != cloudBuildDockerImage || strings.Join(cfg.Steps[0].Args, " ") != wantArgs {
		t.Errorf("build steps = %+v, want one %s step running %q", cfg.Steps, cloudBuildDockerImage, wantArgs)
	}
	if strings.Join(cfg.Images, ",") != "reg.io/team/etl:v1" || cfg.Options != nil {
		t.Errorf("build images = %q options %+v, want Cloud Build to push the tag, no options", cfg.Images, cfg.Options)
	}
	if res.Path != PathRemote || !res.Pushed || res.Ref != "reg.io/team/etl@sha256:abc123" {
		t.Errorf("result = %+v, want the remote path, pushed, pinned to the registry digest", res)
	}
	if out := outOf(req.Stdout); !strings.Contains(out, "Step #0: building the image") {
		t.Errorf("stdout = %q, want the streamed log", out)
	}
	if e := outOf(req.Stderr); !strings.Contains(e, "build b-123") || !strings.Contains(e, "console.cloud.google.com") {
		t.Errorf("stderr = %q, want the build id and its log link", e)
	}
}

// A tag that names another image than the one the build pushed (a
// concurrent push of the same tag) is refused instead of registered.
func TestCloudBuildRefusesADigestThatIsNotTheBuilds(t *testing.T) {
	tool, dir := fakeGcloud(t)
	setDescribe(t, dir, "SUCCESS", "sha256:fromthebuild")
	b := CloudBuild{Project: "acme-ci", Tool: tool, Registry: &fakeRegistry{ref: "reg.io/team/etl@sha256:other", manifest: singleManifest}}
	_, err := b.Build(context.Background(), cloudRequest(t))
	if err == nil || !strings.Contains(err.Error(), "sha256:fromthebuild") || !strings.Contains(err.Error(), "sha256:other") {
		t.Fatalf("Build = %v, want a refusal naming both digests", err)
	}
}

// A custom service account logs to Cloud Logging only, which gcloud cannot
// stream: the build is described until it ends, and its log is named.
func TestCloudBuildWithAServiceAccountPollsAndNamesTheLog(t *testing.T) {
	tool, dir := fakeGcloud(t)
	sa := "projects/acme-ci/serviceAccounts/builder@acme-ci.iam.gserviceaccount.com"
	pool := "projects/acme-ci/locations/us-central1/workerPools/private"
	b := CloudBuild{Project: "acme-ci", ServiceAccount: sa, WorkerPool: pool, Tool: tool, Poll: time.Millisecond,
		Registry: &fakeRegistry{ref: "reg.io/team/etl@sha256:abc123", manifest: singleManifest}}
	req := cloudRequest(t)
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatalf("Build: %v", err)
	}
	got := gcloudCalls(t, dir)
	submit := strings.Fields(got[0])
	for _, want := range []string{"--service-account=" + sa, "--worker-pool=" + pool} {
		if !slices.Contains(submit, want) {
			t.Errorf("submit argv = %q, want %s", submit, want)
		}
	}
	if slices.ContainsFunc(got, func(c string) bool { return strings.HasPrefix(c, "builds log") }) {
		t.Errorf("gcloud calls = %q, want no log stream for a Cloud Logging build", got)
	}
	var cfg cloudBuildConfig
	if jerr := json.Unmarshal([]byte(readFile(t, filepath.Join(dir, "config.json"))), &cfg); jerr != nil {
		t.Fatalf("build config: %v", jerr)
	}
	if cfg.Options == nil || cfg.Options.Logging != "CLOUD_LOGGING_ONLY" {
		t.Errorf("build options = %+v, want CLOUD_LOGGING_ONLY with a service account", cfg.Options)
	}
	if e := outOf(req.Stderr); !strings.Contains(e, "gcloud logging read") {
		t.Errorf("stderr = %q, want how to read the Cloud Logging log", e)
	}
}

// A refused submit keeps gcloud's reason and says what to check: the
// sign-in, the project and the roles.
func TestCloudBuildFailureNamesTheLoginAndProject(t *testing.T) {
	tool, dir := fakeGcloud(t)
	touch(t, filepath.Join(dir, "submit-fails"))
	reg := &fakeRegistry{}
	_, err := CloudBuild{Project: "acme-ci", Tool: tool, Registry: reg}.Build(context.Background(), cloudRequest(t))
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

// A build that ends in failure names its id, its status and how to read
// its log.
func TestCloudBuildFailedBuildNamesItsLog(t *testing.T) {
	tool, dir := fakeGcloud(t)
	setDescribe(t, dir, "FAILURE", "")
	_, err := CloudBuild{Project: "acme-ci", Region: "us-central1", Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), cloudRequest(t))
	for _, want := range []string{"b-123", "FAILURE", "step 0 exited 1", "gcloud builds log b-123 --project=acme-ci --region=us-central1"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to name %q", err, want)
		}
	}
}

// Stopping the command (Ctrl-C) interrupts gcloud with SIGINT rather than
// killing it, and cancels the build on Cloud Build, which would otherwise
// go on and push.
func TestCloudBuildStopsTheRemoteBuildWhenCanceled(t *testing.T) {
	tool, dir := fakeGcloud(t)
	touch(t, filepath.Join(dir, "log-hangs"))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(dir, "log-started")); err == nil {
				cancel()
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	_, err := CloudBuild{Project: "acme-ci", Tool: tool, Registry: &fakeRegistry{}}.Build(ctx, cloudRequest(t))
	if err == nil || !strings.Contains(err.Error(), "b-123") || !strings.Contains(err.Error(), "canceled") {
		t.Errorf("Build = %v, want the stop and the canceled build named", err)
	}
	if sig, rerr := os.ReadFile(filepath.Join(dir, "signals")); rerr != nil || strings.TrimSpace(string(sig)) != "INT" { //nolint:gosec // a path this test created
		t.Errorf("signals = %q (%v), want gcloud interrupted with SIGINT", sig, rerr)
	}
	if got := gcloudCalls(t, dir); !slices.Contains(got, "builds cancel b-123 --project=acme-ci") {
		t.Errorf("gcloud calls = %q, want the build canceled", got)
	}
}

// An image outside Google's registries gets a note: Cloud Build pushes
// with its service account, which only they accept.
func TestCloudBuildWarnsAboutARegistryOutsideGoogleCloud(t *testing.T) {
	tool, _ := fakeGcloud(t)
	req := cloudRequest(t)
	b := CloudBuild{Project: "acme-ci", Tool: tool, Registry: &fakeRegistry{ref: "reg.io/team/etl@sha256:abc123", manifest: singleManifest}}
	if _, err := b.Build(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if e := outOf(req.Stderr); !strings.Contains(e, "Artifact Registry") || !strings.Contains(e, "reg.io") {
		t.Errorf("stderr = %q, want a note about reg.io", e)
	}
}

// A context too large to upload is refused before gcloud runs, naming
// .dockerignore.
func TestCloudBuildRefusesAnOversizedContext(t *testing.T) {
	tool, dir := fakeGcloud(t)
	defer func(n int64) { maxContextArchive = n }(maxContextArchive)
	maxContextArchive = 16
	_, err := CloudBuild{Project: "acme-ci", Tool: tool, Registry: &fakeRegistry{}}.Build(context.Background(), cloudRequest(t))
	if err == nil || !strings.Contains(err.Error(), ".dockerignore") {
		t.Errorf("Build = %v, want a size refusal naming .dockerignore", err)
	}
	if gcloudCalls(t, dir) != nil {
		t.Error("gcloud ran for an oversized context")
	}
}

// gcloud missing is said before anything is packed, with how to install it.
func TestCloudBuildWithoutGcloudSaysHowToInstallIt(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	b := CloudBuild{Project: "acme-ci", Registry: &fakeRegistry{}}
	if err := b.Ready(); err == nil || !strings.Contains(err.Error(), "gcloud is not on PATH") || !strings.Contains(err.Error(), "cloud.google.com/sdk") {
		t.Errorf("Ready = %v, want gcloud missing and where to get it", err)
	}
	if _, err := b.Build(context.Background(), cloudRequest(t)); err == nil || !strings.Contains(err.Error(), "gcloud is not on PATH") {
		t.Errorf("Build = %v, want gcloud missing", err)
	}
}

// Cloud Build pushes what it builds, and only an unlisted context would be
// uploaded whole: both are refused before gcloud runs.
func TestCloudBuildRefusesAnUnpushedOrUnlistedBuild(t *testing.T) {
	tool, dir := fakeGcloud(t)
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
	if gcloudCalls(t, dir) != nil {
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
