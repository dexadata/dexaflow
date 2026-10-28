package cli

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dexadata/dexaflow/internal/build"
	"github.com/dexadata/dexaflow/internal/domain"
)

// cloudBuildYAML configures the Cloud Build remote builder in dexaflow.yaml.
const cloudBuildYAML = "  remote_builder:\n    cloud_build: {project: acme-ci, region: us-central1}\n"

// fakeGcloudOnPath puts a stand-in for gcloud first on PATH. It logs the
// argv of each `builds submit` and the entries of the source archive it was
// given, and answers as a build Cloud Build ran and pushed would: a build
// id, a log line, and a successful build reporting the digest in
// FAKE_GCLOUD_DIGEST (cloudBuildPushes sets it, with the image).
func fakeGcloudOnPath(t *testing.T) (argvLog, archiveLog string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	argvLog, archiveLog = filepath.Join(dir, "argv.log"), filepath.Join(dir, "archive.log")
	script := `#!/bin/sh
case "$1 $2" in
"builds submit")
  echo "$*" >> "` + argvLog + `"
  for a in "$@"; do
    case "$a" in
      *.tar.gz) tar -tzf "$a" | sort >> "` + archiveLog + `" ;;
    esac
  done
  echo '{"id": "0f8b3c2a-1d4e-4f5a-9b6c-7d8e9f0a1b2c"}'
  ;;
"builds log") echo "Step #0: building" ;;
"builds describe")
  echo '{"id": "0f8b3c2a-1d4e-4f5a-9b6c-7d8e9f0a1b2c", "status": "SUCCESS", "results": {"images": [{"name": "x", "digest": "'"$FAKE_GCLOUD_DIGEST"'"}]}}'
  ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "gcloud"), []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("FAKE_GCLOUD_DIGEST", "")
	return argvLog, archiveLog
}

// cloudBuildPushes pushes what a Cloud Build build of ref leaves in the
// registry, and has the fake gcloud report its digest.
func cloudBuildPushes(t *testing.T, ref string) {
	t.Helper()
	pushPlatformIndex(t, ref, "linux/amd64")
	pinned, err := build.Registry{}.Resolve(context.Background(), ref)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_GCLOUD_DIGEST", pinned.Ref[strings.LastIndex(pinned.Ref, "@")+1:])
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
	writeProjectFile(t, dir, ".env", "TOKEN=x\n")
	writeProjectFile(t, dir, "lib/util.py", "x = 1\n")
	image := host + "/team/etl:v1"
	cloudBuildPushes(t, image)
	daemon, daemonLog := argvBuilder(t)
	fake := &deployServer{}

	out, stderrOut, err := deployDir(t, fake, dir, daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(stderrOut, "warning: .env is uploaded to Cloud Build") || !strings.Contains(stderrOut, "acme-ci_cloudbuild") {
		t.Errorf("stderr does not warn that .env is uploaded and kept in the source bucket:\n%s", stderrOut)
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
	cloudBuildPushes(t, deps)
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
	t.Parallel()
	path := filepath.Join(t.TempDir(), workspaceFileName)
	writeTree(t, path, "targets:\n  dev: {server: https://dev.example, remote_builder: {cloud_build: {project: X}}}\n")
	if _, err := parseWorkspaceFile(path); err == nil || !strings.Contains(err.Error(), `target "dev": remote_builder.cloud_build.project`) {
		t.Errorf("parseWorkspaceFile = %v, want the target's field named", err)
	}
}

// Where the image is built is not an input of the image: configuring a
// remote builder leaves the content label as it was.
func TestRemoteBuilderDoesNotMoveTheContentLabel(t *testing.T) {
	t.Parallel()
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

// The push preflight is softened only when nothing is pushed from this
// machine: a whole build the remote builder runs. Every other path with a
// remote builder configured pushes with this machine's credential (the code
// layer, the local daemon for arm64 or without gcloud), so a refusal stops
// the deploy before anything is built or pushed, naming the builder.
func TestDeployPreflightRefusalStaysAnErrorWhenThisMachinePushes(t *testing.T) {
	cases := map[string]struct {
		dockerfile bool
		depsCached bool
		args       []string
		noGcloud   bool
	}{
		"code layer onto a cached dependency image":     {depsCached: true},
		"dependency image remote, code layer from here": {},
		"arm64 on the local daemon":                     {dockerfile: true, args: []string{"--platform", "linux/arm64"}},
		"gcloud missing, docker present":                {dockerfile: true, noGcloud: true},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var deny atomic.Bool
			argvLog, _ := fakeGcloudOnPath(t)
			if tc.noGcloud {
				// CI runners ship a real gcloud, so drop every PATH entry that has one.
				t.Setenv("PATH", pathWithout(t, "gcloud"))
			}
			dir, host, deps := splitProjectWith(t, cloudBuildYAML, &deny)
			if tc.dockerfile {
				writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
			}
			if tc.depsCached {
				pushPlatformIndex(t, deps, "linux/amd64")
			}
			dockerCredentialsFor(t, host)
			deny.Store(true)
			daemon, daemonLog := argvBuilder(t)
			fake := &deployServer{}
			out, errOut, err := deployDir(t, fake, dir, daemon, append([]string{"--dag-version", "v1"}, tc.args...)...)
			if err == nil || !strings.Contains(err.Error(), "preflight: the registry "+host) || !strings.Contains(err.Error(), "pushes from this machine") {
				t.Fatalf("deploy = %v, want the preflight refusal naming the local push\n%s\n%s", err, out, errOut)
			}
			if calls := argvLines(t, daemonLog); calls != nil {
				t.Errorf("the daemon ran: %q", calls)
			}
			if !tc.noGcloud {
				if argv := argvLines(t, argvLog); argv != nil {
					t.Errorf("gcloud ran: %q", argv)
				}
			}
			if fake.registers != 0 {
				t.Errorf("registers = %d, want none", fake.registers)
			}
		})
	}
}

// A whole build on Cloud Build pushes nothing from here, so the same
// refusal is a note and the deploy goes on.
func TestDeployPreflightRefusalIsANoteForAWholeRemoteBuild(t *testing.T) {
	var deny atomic.Bool
	argvLog, _ := fakeGcloudOnPath(t)
	dir, host, _ := splitProjectWith(t, cloudBuildYAML, &deny)
	writeProjectFile(t, dir, "Dockerfile", "FROM scratch\n")
	cloudBuildPushes(t, host+"/team/etl:v1")
	dockerCredentialsFor(t, host)
	deny.Store(true)
	daemon, _ := argvBuilder(t)
	fake := &deployServer{}
	out, errOut, err := deployDir(t, fake, dir, daemon, "--dag-version", "v1")
	if err != nil {
		t.Fatalf("deploy: %v\n%s\n%s", err, out, errOut)
	}
	if !strings.Contains(errOut, "note: ") || !strings.Contains(errOut, "pushes with its own credentials") {
		t.Errorf("stderr does not carry the refusal as a note:\n%s", errOut)
	}
	if argv := argvLines(t, argvLog); len(argv) != 1 || fake.registers != 1 {
		t.Errorf("gcloud calls %q, registers %d, want the remote build and the register", argv, fake.registers)
	}
}

// The context a remote build uploads is what a container daemon reads:
// .dockerignore with Docker's rules (a `!` line re-includes under an
// excluded directory), exclude_paths, symlinks kept as links, the
// generated Dockerfile left out, and a <Dockerfile>.dockerignore next to a
// project Dockerfile used in place of .dockerignore, as BuildKit does.
func TestContextFilesListWhatADaemonReads(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		files      map[string]string
		links      map[string]string
		exclude    []string
		dockerfile string
		want       []string
	}{
		"dockerignore with a re-include": {
			files: map[string]string{".dockerignore": "data/\n!data/keep.csv\n*.log\n", "dag.py": "", "data/big.bin": "", "data/keep.csv": "",
				"run.log": "", "sub/run.log": "", generatedDockerfileName: ""},
			want: []string{".dockerignore", "dag.py", "data/", "data/keep.csv", "sub/", "sub/run.log"},
		},
		"exclude_paths and links": {
			files:   map[string]string{"dag.py": "", "secret.txt": "", "lib/a.py": ""},
			links:   map[string]string{"lib/link.py": "a.py"},
			exclude: []string{"secret.txt"},
			want:    []string{".dockerignore", "dag.py", "lib/", "lib/a.py", "lib/link.py -> a.py"},
		},
		"Dockerfile.dockerignore wins": {
			files: map[string]string{".dockerignore": "dag.py\n", "Dockerfile.dockerignore": "notes.md\n", "Dockerfile": "FROM scratch\n",
				"dag.py": "", "notes.md": ""},
			dockerfile: "Dockerfile",
			want:       []string{".dockerignore", "Dockerfile", "Dockerfile.dockerignore", "dag.py"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			for f, body := range tc.files {
				writeProjectFile(t, dir, f, body)
			}
			for l, target := range tc.links {
				if err := os.Symlink(target, filepath.Join(dir, l)); err != nil {
					t.Fatal(err)
				}
			}
			if _, ok := tc.files[".dockerignore"]; !ok {
				writeProjectFile(t, dir, ".dockerignore", "")
			}
			dockerfile := filepath.Join(dir, generatedDockerfileName)
			if tc.dockerfile != "" {
				dockerfile = filepath.Join(dir, tc.dockerfile)
			}
			cfg := &domain.LeoflowConfig{DagID: "etl", DagSource: "dag.py", ExcludePaths: tc.exclude}
			files, err := contextFiles(dir, cfg, dockerfile)
			if err != nil {
				t.Fatal(err)
			}
			if got := layerPaths(files); !slices.Equal(got, tc.want) {
				t.Errorf("context = %q, want %q", got, tc.want)
			}
			for _, f := range files {
				if f.Path == "lib/link.py" && f.Link != "a.py" {
					t.Errorf("lib/link.py = %+v, want a link to a.py", f)
				}
			}
		})
	}
}

// The warning names the bucket gcloud really stages into, also for a
// domain-scoped project.
func TestUploadedSecretsWarningNamesTheSourceBucket(t *testing.T) {
	t.Parallel()
	cfg := &domain.LeoflowConfig{Build: &domain.BuildConfig{RemoteBuilder: &domain.RemoteBuilderConfig{
		CloudBuild: &domain.CloudBuildConfig{Project: "example.com:acme-ci"}}}}
	var w strings.Builder
	warnUploadedSecrets(&w, []build.LayerFile{{Path: ".env", Source: "x"}, {Path: "dag.py", Source: "y"}}, cfg)
	if got := w.String(); !strings.Contains(got, "gs://example_com_acme-ci_cloudbuild/source") || strings.Contains(got, "dag.py") {
		t.Errorf("warning = %q, want .env and gcloud's bucket name only", got)
	}
}

// pathWithout returns PATH with tool missing whatever the host installed: a
// directory that holds tool is replaced by a copy made of links to its other
// entries, so the shell and the other tools it holds stay reachable.
func pathWithout(t *testing.T, tool string) string {
	t.Helper()
	var kept []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if _, err := os.Stat(filepath.Join(dir, tool)); err != nil {
			kept = append(kept, dir)
			continue
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		shadow := t.TempDir()
		for _, e := range entries {
			if e.Name() == tool {
				continue
			}
			if err := os.Symlink(filepath.Join(dir, e.Name()), filepath.Join(shadow, e.Name())); err != nil {
				t.Fatal(err)
			}
		}
		kept = append(kept, shadow)
	}
	return strings.Join(kept, string(os.PathListSeparator))
}
