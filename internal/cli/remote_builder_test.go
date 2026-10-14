package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
)

// cloudBuildYAML configures the Cloud Build remote builder in dexaflow.yaml.
const cloudBuildYAML = "  remote_builder:\n    cloud_build: {project: acme-ci, region: us-central1}\n"

// fakeGcloudOnPath puts a stand-in for gcloud first on PATH. It logs its
// argv and the entries of the source archive it was given, and exits 0, as
// a build Cloud Build ran and pushed would; the test pushes what the build
// would have pushed.
func fakeGcloudOnPath(t *testing.T) (argvLog, archiveLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	argvLog, archiveLog = filepath.Join(dir, "argv.log"), filepath.Join(dir, "archive.log")
	script := `#!/bin/sh
echo "$*" >> "` + argvLog + `"
for a in "$@"; do
  case "$a" in
    *.tar.gz) tar -tzf "$a" | sort >> "` + archiveLog + `" ;;
  esac
done
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return argvLog, archiveLog
}

// A project with its own Dockerfile needs a real build: with a cloud
// builder configured, the deploy sends the context (as the .dockerignore and
// exclude_paths leave it) to Cloud Build instead of running the daemon, and
// registers the digest the registry holds for the pushed tag.
func TestDeployBuildsAProjectDockerfileOnCloudBuild(t *testing.T) {
	argvLog, archiveLog := fakeGcloudOnPath(t)
	dir, host, _ := splitProject(t, cloudBuildYAML+"exclude_paths: [secret.txt]\n")
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\nCOPY . /app\n")
	writeProjectFile(t, dir, "secret.txt", "token\n")
	writeProjectFile(t, dir, "lib/util.py", "x = 1\n")
	image := host + "/team/etl:v1"
	pushPlatformIndex(t, image, "linux/amd64") // what Cloud Build pushes
	daemon, daemonLog := argvBuilder(t)
	fake := &deployServer{}

	out, _, err := deployDir(t, fake, dir, daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if calls := argvLines(t, daemonLog); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	want := "builder:    remote (cloud-build, project acme-ci, region us-central1), the project ships its own Dockerfile, which needs a real build, " +
		"so the remote builder from " + projectConfigPath(dir) + " builds the whole image"
	if !strings.Contains(out, want) {
		t.Errorf("output does not say %q:\n%s", want, out)
	}
	argv := argvLines(t, argvLog)
	if len(argv) != 1 || !strings.HasPrefix(argv[0], "builds submit ") || !strings.Contains(argv[0], "--project=acme-ci") || !strings.Contains(argv[0], "--region=us-central1") {
		t.Fatalf("gcloud calls = %q, want one builds submit on acme-ci in us-central1", argv)
	}
	archive := strings.Join(argvLines(t, archiveLog), " ")
	for _, want := range []string{".dexaflow-remote.Dockerfile", "Dockerfile", "dag.py", "dexaflow.yaml", "lib/util.py"} {
		if !strings.Contains(" "+archive+" ", " "+want+" ") {
			t.Errorf("archive = %q, want %s in it", archive, want)
		}
	}
	for _, unwanted := range []string{"secret.txt", generatedDockerfileName} {
		if strings.Contains(archive, unwanted) {
			t.Errorf("archive = %q, want no %s", archive, unwanted)
		}
	}
	pinned, err := build.Registry{}.Resolve(context.Background(), image)
	if err != nil {
		t.Fatal(err)
	}
	if fake.registers != 1 || !strings.Contains(out, strings.TrimPrefix(pinned.Ref, host+"/team/etl@")) {
		t.Errorf("registers=%d, want the pushed digest %s registered:\n%s", fake.registers, pinned.Ref, out)
	}
}

// A missing dependency image that no wheels build (uv is absent in these
// tests) is built on Cloud Build from its Dockerfile alone, then the code is
// appended through the registry: no daemon runs, and no project file leaves
// the machine.
func TestDeployBuildsTheDependencyImageOnCloudBuild(t *testing.T) {
	argvLog, archiveLog := fakeGcloudOnPath(t)
	dir, _, deps := splitProject(t, cloudBuildYAML)
	pushPlatformIndex(t, deps, "linux/amd64") // what Cloud Build pushes
	daemon, daemonLog := argvBuilder(t)
	out, _, err := deployDir(t, &deployServer{}, dir, daemon, "--rebuild")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if calls := argvLines(t, daemonLog); calls != nil {
		t.Errorf("the daemon ran: %q", calls)
	}
	if !strings.Contains(out, "builder:    remote (cloud-build, project acme-ci, region us-central1) for the dependency image, then code-layer") {
		t.Errorf("output does not say the remote builder builds the dependency image:\n%s", out)
	}
	if argv := argvLines(t, argvLog); len(argv) != 1 || !strings.HasPrefix(argv[0], "builds submit ") {
		t.Fatalf("gcloud calls = %q, want one builds submit", argv)
	}
	if archive := argvLines(t, archiveLog); strings.Join(archive, " ") != ".dexaflow-remote.Dockerfile" {
		t.Errorf("archive = %q, want the dependency Dockerfile alone", archive)
	}
}

// The target's or workspace's remote_builder applies to a project that sets
// none, and the builder line names the workspace file.
func TestWorkspaceDefaultsConfigureTheRemoteBuilder(t *testing.T) {
	isolateCLIEnv(t)
	root, project := defaultsWorkspace(t, "dag_id: orders\n")
	ws := readProjectFile(t, root, workspaceFileName)
	writeTree(t, filepath.Join(root, workspaceFileName), ws+"  remote_builder:\n    cloud_build: {project: acme-shared}\n")
	t.Chdir(root)
	root2 := NewRootCommand()
	cmd, rest, err := root2.Find([]string{"deploy", "--config", emptyConfig(t), "--target", "bare"})
	if err != nil {
		t.Fatal(err)
	}
	if perr := cmd.ParseFlags(rest); perr != nil {
		t.Fatal(perr)
	}
	cfg, err := loadProjectConfig(project)
	if err != nil {
		t.Fatal(err)
	}
	prov, err := applyWorkspaceDefaults(cmd, project, cfg, true)
	if err != nil {
		t.Fatalf("applyWorkspaceDefaults: %v", err)
	}
	rb := cfg.Build.RemoteBuilder
	if rb == nil || rb.CloudBuild == nil || rb.CloudBuild.Project != "acme-shared" {
		t.Fatalf("remote_builder = %+v, want the workspace default", rb)
	}
	if !strings.Contains(prov.remoteSource, "defaults in") || !strings.Contains(prov.remoteSource, workspaceFileName) {
		t.Errorf("source = %q, want the workspace defaults named", prov.remoteSource)
	}
}

// A bad remote_builder in the workspace file is refused when it loads.
func TestWorkspaceFileRefusesABadRemoteBuilder(t *testing.T) {
	path := filepath.Join(t.TempDir(), workspaceFileName)
	writeTree(t, path, "targets:\n  dev: {server: https://dev.example, remote_builder: {cloud_build: {project: X}}}\n")
	if _, err := parseWorkspaceFile(path); err == nil || !strings.Contains(err.Error(), `target "dev": remote_builder.cloud_build.project`) {
		t.Errorf("parseWorkspaceFile = %v, want the target's field named", err)
	}
}

// Where the image is built is not an input of the image: configuring a
// remote builder leaves the content label as it was.
func TestRemoteBuilderDoesNotMoveTheContentLabel(t *testing.T) {
	dir := versionProject(t, "build:\n  platforms: [linux/amd64]\n")
	before := labelOf(t, dir)
	editYAML(t, dir, "build:\n", "build:\n"+cloudBuildYAML)
	if after := labelOf(t, dir); after != before {
		t.Errorf("label moved from %s to %s when remote_builder was set", before, after)
	}
}

// A build that is not pushed stays on the daemon, and the builder line says
// why the remote builder does not apply.
func TestCompileWithoutPushKeepsTheDaemon(t *testing.T) {
	argvLog, _ := fakeGcloudOnPath(t)
	dir, host, _ := splitProject(t, cloudBuildYAML)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	daemon, daemonLog := argvBuilder(t)
	out, _, err := run(t, "compile", dir, "--build", "--image", host+"/team/etl:v1", "--builder", daemon)
	if err != nil {
		t.Fatalf("compile --build: %v\n%s", err, out)
	}
	if !strings.Contains(out, "pushes what it builds, and this build is not pushed") {
		t.Errorf("output does not say why the remote builder does not apply:\n%s", out)
	}
	if calls := argvLines(t, daemonLog); len(calls) != 1 || !strings.HasPrefix(calls[0], "build ") {
		t.Errorf("daemon calls = %q, want the build", calls)
	}
	if argv := argvLines(t, argvLog); argv != nil {
		t.Errorf("gcloud ran: %q", argv)
	}
}

// A registry that refuses this machine's push does not stop a deploy whose
// project configures a remote builder: Cloud Build pushes with its own
// service account, so the refusal is a note that says so.
func TestDeployPreflightRefusalIsANoteWithARemoteBuilder(t *testing.T) {
	argvLog, _ := fakeGcloudOnPath(t)
	reg := newFakeOCIRegistry(t, singleManifest)
	reg.denyPush = true
	dockerCredentialsFor(t, reg.host)
	dir := versionProject(t, "registry:\n  url: "+reg.host+"/team\n  image_name: etl\nbuild:\n"+cloudBuildYAML)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	daemon, _ := argvBuilder(t)
	fake := &deployServer{}
	out, errOut, err := deployDir(t, fake, dir, daemon, "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "note: ") || !strings.Contains(errOut, "the remote builder pushes with its own credentials") {
		t.Errorf("stderr does not carry the refusal as a note about the remote builder:\n%s", errOut)
	}
	if argv := argvLines(t, argvLog); len(argv) != 1 || fake.registers != 1 {
		t.Errorf("gcloud calls %q, registers %d, want the remote build and the register", argv, fake.registers)
	}
}
