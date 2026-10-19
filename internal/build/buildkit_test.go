package build

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// buildKitDigest is the digest the fake buildctl writes to its metadata
// file.
const buildKitDigest = "sha256:4f1c2a9be0d3c8a7f6e5d4c3b2a19081726354433221100ffeeddccbbaa99887"

// buildKitPods is what the fake kubectl lists: a pod being replaced, one
// not ready yet and the ready one, out of name order.
const buildKitPods = `{"items": [
  {"metadata": {"name": "dexaflow-builder-7d9-a0", "deletionTimestamp": "2026-10-19T10:00:00Z"}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}},
  {"metadata": {"name": "dexaflow-builder-7d9-bb"}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}},
  {"metadata": {"name": "dexaflow-builder-7d9-aa"}, "status": {"phase": "Pending", "conditions": [{"type": "Ready", "status": "False"}]}}
]}`

// fakeBuildKitTools puts stand-ins for kubectl and buildctl first on PATH
// and returns their directory. Each logs its argv to <tool>.log.
//   - `kubectl auth can-i` answers yes, or no with can-i-no in the directory;
//   - `kubectl get pods` prints pods.json (buildKitPods by default);
//   - buildctl writes buildKitDigest to its --metadata-file, nothing with
//     no-digest in the directory, and fails as a refused exec does with
//     buildctl-fails.
func fakeBuildKitTools(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	kubectl := `#!/bin/sh
d="` + dir + `"
echo "$*" >> "$d/kubectl.log"
case "$*" in
*"auth can-i"*)
  if [ -f "$d/can-i-no" ]; then echo no; exit 1; fi
  echo yes ;;
*"get pods"*) cat "$d/pods.json" ;;
esac
exit 0
`
	buildctl := `#!/bin/sh
d="` + dir + `"
echo "$*" >> "$d/buildctl.log"
if [ -f "$d/buildctl-fails" ]; then
  echo 'error: pods "dexaflow-builder-7d9-bb" is forbidden: User "ana" cannot create resource "pods/exec"' >&2
  exit 1
fi
meta=""
prev=""
for a in "$@"; do
  if [ "$prev" = "--metadata-file" ]; then meta="$a"; fi
  prev="$a"
done
if [ -n "$meta" ] && [ ! -f "$d/no-digest" ]; then
  echo '{"containerimage.digest": "` + buildKitDigest + `"}' > "$meta"
fi
echo "#1 [internal] load build definition from Dockerfile"
exit 0
`
	for name, script := range map[string]string{"kubectl": kubectl, "buildctl": buildctl, "pods.json": buildKitPods} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return dir
}

// buildKitRequest is a pushed, pinned build of a project Dockerfile with a
// build argument.
func buildKitRequest(t *testing.T, platforms ...string) Request {
	t.Helper()
	src := t.TempDir()
	dockerfile := filepath.Join(src, "build", "Dockerfile.prod")
	if err := os.MkdirAll(filepath.Dir(dockerfile), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dockerfile, []byte("FROM base\nCOPY dag.py /app/\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	req := request(platforms...)
	req.Dockerfile, req.ContextDir = dockerfile, src
	req.Push, req.Pin = true, true
	req.BuildArgs = map[string]string{"APP_ENV": "prod"}
	return req
}

// The remote path on the in-cluster builder: the CLI finds the ready pod of
// the builder with kubectl, builds on it with buildctl over kube-pod://
// (kubectl exec as the person who deploys), with the project directory as
// the context, the Dockerfile by its own name and the platform and build
// arguments as frontend options, has BuildKit push the tag, and pins the
// digest BuildKit reports for the push (ADR 0087).
func TestBuildKitBuildsOnTheReadyBuilderPod(t *testing.T) {
	dir := fakeBuildKitTools(t)
	req := buildKitRequest(t, "linux/arm64")
	b := InCluster{Namespace: "dexaflow-builder", PodSelector: "app.kubernetes.io/instance=df,app.kubernetes.io/component=builder", Context: "prod-eu"}

	res, err := b.Build(context.Background(), req)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if want := "reg.io/team/etl@" + buildKitDigest; res.Ref != want || res.Path != PathRemote || !res.Pushed {
		t.Errorf("Result = %+v, want a pushed remote build pinned to %s", res, want)
	}
	kubectl := strings.Join(calls(t, filepath.Join(dir, "kubectl.log")), "\n")
	for _, want := range []string{
		"--context=prod-eu --namespace=dexaflow-builder auth can-i create pods/exec",
		"--context=prod-eu --namespace=dexaflow-builder get pods --selector=app.kubernetes.io/instance=df,app.kubernetes.io/component=builder --output=json",
	} {
		if !strings.Contains(kubectl, want) {
			t.Errorf("kubectl calls = %q, want %q", kubectl, want)
		}
	}
	argv := calls(t, filepath.Join(dir, "buildctl.log"))
	if len(argv) != 1 {
		t.Fatalf("buildctl calls = %q, want one build", argv)
	}
	for _, want := range []string{
		"--addr kube-pod://dexaflow-builder-7d9-bb?context=prod-eu&namespace=dexaflow-builder build",
		"--frontend dockerfile.v0",
		"--local context=" + req.ContextDir + " ",
		"--local dockerfile=" + filepath.Join(req.ContextDir, "build") + " ",
		"--opt filename=Dockerfile.prod",
		"--opt platform=linux/arm64",
		"--opt build-arg:APP_ENV=prod",
		"--output type=image,name=reg.io/team/etl:v1,push=true",
		"--metadata-file ",
	} {
		if !strings.Contains(argv[0]+" ", want) {
			t.Errorf("buildctl argv = %q, want %q in it", argv[0], want)
		}
	}
	if !strings.Contains(outOf(req.Stdout)+outOf(req.Stderr), "on pod dexaflow-builder-7d9-bb") {
		t.Errorf("output does not name the pod the build runs on:\nstdout: %s\nstderr: %s", outOf(req.Stdout), outOf(req.Stderr))
	}
}

// Without a context the kube-pod address and kubectl use the current
// context, and without a selector the pods of the chart's builder are
// listed.
func TestBuildKitDefaultsToTheCurrentContextAndTheChartSelector(t *testing.T) {
	dir := fakeBuildKitTools(t)
	if _, err := (InCluster{Namespace: "builds"}).Build(context.Background(), buildKitRequest(t, "linux/amd64")); err != nil {
		t.Fatalf("Build: %v", err)
	}
	kubectl := strings.Join(calls(t, filepath.Join(dir, "kubectl.log")), "\n")
	if strings.Contains(kubectl, "--context") || !strings.Contains(kubectl, "--selector=app.kubernetes.io/component=builder ") {
		t.Errorf("kubectl calls = %q, want the current context and the chart's selector", kubectl)
	}
	argv := strings.Join(calls(t, filepath.Join(dir, "buildctl.log")), "\n")
	if !strings.Contains(argv, "--addr kube-pod://dexaflow-builder-7d9-bb?namespace=builds build") {
		t.Errorf("buildctl argv = %q, want the pod in namespace builds with no context", argv)
	}
}

// A build BuildKit reports no digest for is pinned from the registry.
func TestBuildKitPinsFromTheRegistryWithoutAReportedDigest(t *testing.T) {
	dir := fakeBuildKitTools(t)
	touch(t, filepath.Join(dir, "no-digest"))
	reg := &fakeRegistry{ref: "reg.io/team/etl@sha256:abc123", manifest: singleManifest}
	res, err := InCluster{Namespace: "builds", Registry: reg}.Build(context.Background(), buildKitRequest(t, "linux/amd64"))
	if err != nil || res.Ref != "reg.io/team/etl@sha256:abc123" {
		t.Errorf("Build = %+v, %v; want the digest the registry holds", res, err)
	}
}

// The builder is not ready, so Select passes it over for the local daemon,
// when buildctl or kubectl is missing or when the person deploying is not
// allowed to exec into the builder (not bound by builder.subjects); the
// reason says what to do.
func TestBuildKitReadyNamesWhatIsMissing(t *testing.T) {
	dir := fakeBuildKitTools(t)
	b := InCluster{Namespace: "builds"}
	if err := b.Ready(context.Background()); err != nil {
		t.Fatalf("Ready = %v, want ready", err)
	}
	touch(t, filepath.Join(dir, "can-i-no"))
	if err := b.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "builder.subjects") || !strings.Contains(err.Error(), "namespace builds") {
		t.Errorf("Ready = %v, want a refusal naming builder.subjects and the namespace", err)
	}
	if err := os.Remove(filepath.Join(dir, "buildctl")); err != nil {
		t.Fatal(err)
	}
	if err := b.Ready(context.Background()); err == nil || !strings.Contains(err.Error(), "buildctl is not on PATH") || !strings.Contains(err.Error(), "github.com/moby/buildkit/releases") {
		t.Errorf("Ready = %v, want it to say how to install buildctl", err)
	}
}

// The rootless builder has no emulation, so it builds one platform: a
// build for several goes to the local daemon.
func TestBuildKitDeclinesSeveralPlatforms(t *testing.T) {
	b := InCluster{Namespace: "builds"}
	if why := b.Decline([]string{"linux/arm64"}); why != "" {
		t.Errorf("Decline(linux/arm64) = %q, want it accepted", why)
	}
	if why := b.Decline([]string{"linux/amd64", "linux/arm64"}); !strings.Contains(why, "one platform") {
		t.Errorf("Decline(two) = %q, want it to say one platform", why)
	}
	if got := b.Describe(); got != "remote (buildkit, namespace builds)" {
		t.Errorf("Describe = %q", got)
	}
	if got := (InCluster{Namespace: "builds", Context: "prod"}).Describe(); got != "remote (buildkit, namespace builds, context prod)" {
		t.Errorf("Describe = %q", got)
	}
}

// Build secrets reach BuildKit from this machine's environment, and the
// pod is shared by everyone bound to it, whose build steps see each
// other's processes: a build with secrets never runs there.
func TestBuildKitRefusesSecretsAndUnpushedBuilds(t *testing.T) {
	dir := fakeBuildKitTools(t)
	req := buildKitRequest(t, "linux/amd64")
	req.Secrets = []Secret{{ID: "github_token", Env: "GITHUB_TOKEN"}}
	if _, err := (InCluster{Namespace: "builds"}).Build(context.Background(), req); err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Errorf("Build with secrets = %v, want a refusal", err)
	}
	req = buildKitRequest(t, "linux/amd64")
	req.Push = false
	if _, err := (InCluster{Namespace: "builds"}).Build(context.Background(), req); err == nil || !strings.Contains(err.Error(), "push") {
		t.Errorf("Build without push = %v, want a refusal", err)
	}
	if got := calls(t, filepath.Join(dir, "buildctl.log")); got != nil {
		t.Errorf("buildctl ran: %q", got)
	}
}

// No ready pod, and a failed build, fail with what to check.
func TestBuildKitFailuresSayWhatToCheck(t *testing.T) {
	dir := fakeBuildKitTools(t)
	if err := os.WriteFile(filepath.Join(dir, "pods.json"), []byte(`{"items": []}`), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := InCluster{Namespace: "builds"}.Build(context.Background(), buildKitRequest(t, "linux/amd64"))
	if err == nil || !strings.Contains(err.Error(), "no ready pod") || !strings.Contains(err.Error(), "builder.enabled") {
		t.Errorf("Build with no pod = %v, want it to say no ready pod and builder.enabled", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "pods.json"), []byte(`{"items": [{"metadata": {"name": "bad name;x"}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}]}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if _, berr := (InCluster{Namespace: "builds"}).Build(context.Background(), buildKitRequest(t, "linux/amd64")); berr == nil || !strings.Contains(berr.Error(), "no ready pod") {
		t.Errorf("Build with a malformed pod name = %v, want it refused", berr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "pods.json"), []byte(buildKitPods), 0o600); werr != nil {
		t.Fatal(werr)
	}
	touch(t, filepath.Join(dir, "buildctl-fails"))
	_, err = InCluster{Namespace: "builds"}.Build(context.Background(), buildKitRequest(t, "linux/amd64"))
	if err == nil || !strings.Contains(err.Error(), "dexaflow-builder-7d9-bb") || !strings.Contains(err.Error(), "dexaflow registry login") {
		t.Errorf("Build that fails = %v, want the pod and how to sign in to the registry", err)
	}
}
