package cli

import (
	"context"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// buildKitYAML configures the in-cluster builder in dexaflow.yaml.
const buildKitYAML = "  remote_builder:\n    buildkit: {namespace: dexaflow-builder}\n"

// fakeBuildKitOnPath puts stand-ins for kubectl and buildctl first on PATH.
// kubectl allows the exec and lists one ready builder pod, logging the
// selector it was asked for; buildctl logs its argv and writes the digest
// in FAKE_BUILDCTL_DIGEST (buildKitPushes sets it) to its metadata file.
func fakeBuildKitOnPath(t *testing.T) (kubectlLog, buildctlLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	kubectlLog, buildctlLog = filepath.Join(dir, "kubectl.log"), filepath.Join(dir, "buildctl.log")
	kubectl := `#!/bin/sh
echo "$*" >> "` + kubectlLog + `"
case "$*" in
*"auth can-i"*) echo yes ;;
*"get pods"*) echo '{"items": [{"metadata": {"name": "dexaflow-builder-5c8-x2"}, "status": {"phase": "Running", "conditions": [{"type": "Ready", "status": "True"}]}}]}' ;;
esac
exit 0
`
	buildctl := `#!/bin/sh
echo "$*" >> "` + buildctlLog + `"
prev=""
for a in "$@"; do
  if [ "$prev" = "--metadata-file" ]; then echo '{"containerimage.digest": "'"$FAKE_BUILDCTL_DIGEST"'"}' > "$a"; fi
  prev="$a"
done
exit 0
`
	for name, script := range map[string]string{"kubectl": kubectl, "buildctl": buildctl} {
		writeExecutable(t, filepath.Join(dir, name), script)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_BUILDCTL_DIGEST", "")
	return kubectlLog, buildctlLog
}

// buildKitPushes pushes what a BuildKit build of ref leaves in the
// registry, and has the fake buildctl report its digest.
func buildKitPushes(t *testing.T, ref string) string {
	t.Helper()
	pushPlatformIndex(t, ref, "linux/amd64")
	pinned, err := build.Registry{}.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_BUILDCTL_DIGEST", pinned.Ref[strings.LastIndex(pinned.Ref, "@")+1:])
	return pinned.Ref
}

// A project with its own Dockerfile and build.remote_builder.buildkit is
// built on the in-cluster builder instead of the local daemon: buildctl
// reaches the ready builder pod over kube-pod://, reads the project
// directory as the context, BuildKit pushes, and the deploy registers the
// digest it reported.
func TestDeployBuildsAProjectDockerfileOnTheInClusterBuilder(t *testing.T) {
	kubectlLog, buildctlLog := fakeBuildKitOnPath(t)
	dir, host, _ := splitProject(t, buildKitYAML)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	ref := buildKitPushes(t, host+"/team/etl:v1")
	daemon, daemonLog := argvBuilder(t)
	fake := &deployServer{}

	out, errOut, err := deployDir(t, fake, dir, daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	want := "builder:    remote (buildkit, namespace dexaflow-builder), the project ships its own Dockerfile, which needs a real build, " +
		"so the remote builder from " + projectConfigPath(dir) + " builds the whole image"
	if !strings.Contains(out, want) {
		t.Errorf("output does not say %q:\n%s", want, out)
	}
	if calls := argvLines(t, daemonLog); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	if kubectl := strings.Join(argvLines(t, kubectlLog), "\n"); !strings.Contains(kubectl, "--namespace=dexaflow-builder get pods --selector=app.kubernetes.io/component=builder") {
		t.Errorf("kubectl calls = %q, want the chart's builder pods listed", kubectl)
	}
	argv := argvLines(t, buildctlLog)
	if len(argv) != 1 || !strings.Contains(argv[0], "--addr kube-pod://dexaflow-builder-5c8-x2?namespace=dexaflow-builder build") ||
		!strings.Contains(argv[0], "--local context="+dir+" ") || !strings.Contains(argv[0], "--opt filename=Dockerfile ") ||
		!strings.Contains(argv[0], "--output type=image,name="+host+"/team/etl:v1,push=true") {
		t.Fatalf("buildctl calls = %q, want one build of the project on the builder pod", argv)
	}
	if fake.registers != 1 || !strings.Contains(out, strings.TrimPrefix(ref, host+"/team/etl@")) {
		t.Errorf("registers=%d, want the reported digest %s registered:\n%s", fake.registers, ref, out)
	}
}

// A cluster whose chart runs the builder advertises it, with its pod
// selector, and a project that names no remote builder is built there.
func TestDeployBuildsOnTheClusterBuildKit(t *testing.T) {
	kubectlLog, buildctlLog := fakeBuildKitOnPath(t)
	dir, host, _ := splitProject(t, "")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	buildKitPushes(t, host+"/team/etl:v1")
	daemon, daemonLog := argvBuilder(t)
	fake := &clusterServer{deployServer: &deployServer{}, body: `{"registry":"","platforms":[],"base_image":"",` +
		`"remote_builder":{"buildkit":{"namespace":"df-builder","pod_selector":"app.kubernetes.io/instance=df,app.kubernetes.io/component=builder"}}}`}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if want := "remote (buildkit, namespace df-builder), the project ships its own Dockerfile, which needs a real build, so the remote builder from the deploy defaults of " + srv.URL; !strings.Contains(out, want) {
		t.Errorf("output does not say %q:\n%s", want, out)
	}
	if calls := argvLines(t, daemonLog); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	if kubectl := strings.Join(argvLines(t, kubectlLog), "\n"); !strings.Contains(kubectl, "--namespace=df-builder get pods --selector=app.kubernetes.io/instance=df,app.kubernetes.io/component=builder") {
		t.Errorf("kubectl calls = %q, want the advertised selector", kubectl)
	}
	if argv := argvLines(t, buildctlLog); len(argv) != 1 || !strings.Contains(argv[0], "?namespace=df-builder build") {
		t.Errorf("buildctl calls = %q, want one build in df-builder", argv)
	}
}

// BuildKit pushes with this machine's docker config, so a preflight
// refusal of that credential stops the deploy before anything is built.
func TestDeployPreflightRefusalStopsAnInClusterBuild(t *testing.T) {
	var deny atomic.Bool
	_, buildctlLog := fakeBuildKitOnPath(t)
	dir, host, _ := splitProjectWith(t, buildKitYAML, &deny)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	dockerCredentialsFor(t, host)
	deny.Store(true)
	daemon, _ := argvBuilder(t)
	fake := &deployServer{}
	out, errOut, err := deployDir(t, fake, dir, daemon, "--dag-version", "v1")
	if err == nil || !strings.Contains(err.Error(), "preflight: the registry "+host) || !strings.Contains(err.Error(), "pushes from this machine") {
		t.Fatalf("deploy = %v, want the preflight refusal naming the local push\n%s\n%s", err, out, errOut)
	}
	if argv := argvLines(t, buildctlLog); argv != nil {
		t.Errorf("buildctl ran: %q", argv)
	}
}

// The warning about secret-looking files says where the context goes: the
// in-cluster builder, whose cache everyone bound to it shares.
func TestUploadedSecretsWarningNamesTheInClusterBuilder(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{Build: &domain.BuildConfig{RemoteBuilder: &domain.RemoteBuilderConfig{
		BuildKit: &domain.BuildKitConfig{Namespace: "dexaflow-builder"}}}}
	var w strings.Builder
	warnUploadedSecrets(&w, []build.LayerFile{{Path: ".env", Source: "x"}}, cfg)
	if got := w.String(); !strings.Contains(got, ".env is sent to the in-cluster builder in namespace dexaflow-builder") || strings.Contains(got, "Cloud Build") {
		t.Errorf("warning = %q, want it to name the in-cluster builder", got)
	}
}
