package cli

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// amd64AndArm64 is the manifest of a list holding both platforms, what the
// registry holds after a multi-platform push.
const amd64Only = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"platform":{"os":"linux","architecture":"amd64"}}]}`

const amd64AndArm64 = `{"schemaVersion":2,"mediaType":"application/vnd.oci.image.index.v1+json","manifests":[{"platform":{"os":"linux","architecture":"amd64"}},{"platform":{"os":"linux","architecture":"arm64"}}]}`

// argvBuilder stands in for docker without buildx: it appends every argv to
// log, one call per line, and succeeds. It reports no digest for its push, so
// the digest comes from the project's registry (platformProjectWith).
func argvBuilder(t *testing.T) (builder, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	builder = filepath.Join(dir, "docker")
	log = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		noBuildx +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"exit 0\n"
	writeExecutable(t, builder, script)
	return builder, log
}

// noBuildx is the first line of a fake docker that has no buildx plugin,
// unlogged, so the build and push it logs are the classic two calls.
const noBuildx = "if [ \"$1 $2\" = \"buildx version\" ]; then exit 1; fi\n"

// argvLines returns the logged builder calls, nil when it never ran.
func argvLines(t *testing.T, log string) []string {
	t.Helper()
	data, err := os.ReadFile(log) //nolint:gosec // a path this test created
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(data)), "\n")
}

// platformProject is a project whose parser is stubbed and whose registry
// holds a two-platform list under every tag.
func platformProject(t *testing.T, extraYAML string) string {
	t.Helper()
	dir, _ := platformProjectWith(t, amd64AndArm64, extraYAML)
	return dir
}

// platformProjectWith is platformProject whose registry holds manifest; the
// registry is returned so a test can name the digest it serves.
func platformProjectWith(t *testing.T, manifest, extraYAML string) (string, *fakeOCIRegistry) {
	t.Helper()
	reg := newFakeOCIRegistry(t, manifest)
	dir := versionProject(t, "registry:\n  url: "+reg.host+"/team\n  image_name: etl\n"+extraYAML)
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	return dir, reg
}

// linesStartingWith counts the lines of out that start with prefix.
func linesStartingWith(out, prefix string) int {
	n := 0
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, prefix) {
			n++
		}
	}
	return n
}

func TestCompilePlatformFlagReachesTheBuilderAndNamesItsSource(t *testing.T) {
	dir := platformProject(t, "build:\n  platforms: [linux/amd64]\n")
	builder, log := argvBuilder(t)

	out, _, err := run(t, "compile", dir, "--build", "--builder", builder, "--platform", "linux/arm64")
	if err != nil {
		t.Fatalf("compile --build --platform: %v", err)
	}
	calls := argvLines(t, log)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 -t ") || !strings.Contains(calls[0], "/team/etl:") {
		t.Fatalf("builder calls = %q, want one build for linux/arm64", calls)
	}
	for _, want := range []string{"platforms:  linux/arm64 (from --platform)", "builder:    local-daemon (" + builder + "), fallback"} {
		if !strings.Contains(out, want) {
			t.Errorf("output does not say %q:\n%s", want, out)
		}
	}
}

func TestCompileNamesWherePlatformsCameFrom(t *testing.T) {
	cases := []struct {
		name, yaml, want string
	}{
		{"dexaflow.yaml", "build:\n  platforms: [linux/arm64]\n", "platforms:  linux/arm64 (from %s)"},
		{"default", "", "platforms:  linux/amd64 (from the default)"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := platformProject(t, c.yaml)
			builder, _ := argvBuilder(t)
			out, _, err := run(t, "compile", dir, "--build", "--builder", builder)
			if err != nil {
				t.Fatalf("compile --build: %v", err)
			}
			want := strings.ReplaceAll(c.want, "%s", filepath.Join(dir, "dexaflow.yaml"))
			if !strings.Contains(out, want) {
				t.Errorf("output does not say %q:\n%s", want, out)
			}
		})
	}
}

// Without --build nothing is built, so nothing about a builder is printed.
func TestCompileWithoutBuildPrintsNoBuilder(t *testing.T) {
	dir := platformProject(t, "")
	out, _, err := run(t, "compile", dir, "--platform", "linux/arm64")
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if strings.Contains(out, "builder:") {
		t.Errorf("a compile without --build printed a builder:\n%s", out)
	}
}

// Several platforms reach the tool as one list, as on main: docker with the
// containerd image store builds and pushes them as one manifest list.
func TestCompileHandsSeveralPlatformsToTheTool(t *testing.T) {
	for _, c := range []struct {
		yaml string
		args []string
	}{
		{"", []string{"--platform", "linux/amd64,linux/arm64"}},
		{"", []string{"--platform", "linux/amd64", "--platform", "linux/arm64"}},
		{"build:\n  platforms: [linux/amd64, linux/arm64]\n", nil},
	} {
		dir, reg := platformProjectWith(t, amd64AndArm64, c.yaml)
		builder, log := argvBuilder(t)
		out, _, err := run(t, append([]string{"compile", dir, "--build", "--push", "--builder", builder}, c.args...)...)
		if err != nil {
			t.Fatalf("%v: compile: %v\n%s", c.args, err, out)
		}
		calls := argvLines(t, log)
		if len(calls) != 2 || !strings.HasPrefix(calls[0], "build --platform linux/amd64,linux/arm64 ") || !strings.HasPrefix(calls[1], "push ") {
			t.Errorf("%v: builder calls = %q, want one build for both platforms and a push", c.args, calls)
		}
		if reg.readCount() != 1 {
			t.Errorf("%v: the registry was read %d time(s), want the push verified once", c.args, reg.readCount())
		}
	}
}

// failingBuilder stands in for a docker whose build refuses a platform list.
func failingBuilder(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	p := filepath.Join(t.TempDir(), "docker")
	script := "#!/bin/sh\nif [ \"$1\" = build ]; then echo 'ERROR: Multi-platform build is not supported for the docker driver.' >&2; exit 1; fi\nexit 0\n"
	writeExecutable(t, p, script)
	return p
}

// When the tool cannot build the list, the error keeps the tool's failure and
// says how to build one platform, naming build.platforms first.
func TestCompileHintsWhenAMultiPlatformBuildFails(t *testing.T) {
	dir := platformProject(t, "build:\n  platforms: [linux/amd64, linux/arm64]\n")
	_, _, err := run(t, "compile", dir, "--build", "--builder", failingBuilder(t))
	if err == nil {
		t.Fatal("a failed build was reported as a success")
	}
	for _, want := range []string{"linux/amd64,linux/arm64", "build.platforms", "containerd image store", "local-daemon"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err, want)
		}
	}
	if strings.Index(err.Error(), "build.platforms") > strings.Index(err.Error(), "--platform") {
		t.Errorf("error %q names --platform before build.platforms; dexaflow dev has no --platform", err)
	}
}

// A malformed platform is refused wherever it comes from, before the parser,
// the builder, the confirmation or any request to the control plane.
func TestMalformedProjectPlatformsStopCompile(t *testing.T) {
	dir := platformProject(t, "build:\n  platforms: [arm64]\n")
	parserLog := filepath.Join(t.TempDir(), "parser.log")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, parserLog))
	builder, log := argvBuilder(t)
	_, _, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "build.platforms") || !strings.Contains(err.Error(), filepath.Join(dir, "dexaflow.yaml")) {
		t.Fatalf("err = %v, want build.platforms and the file named", err)
	}
	if _, serr := os.Stat(parserLog); serr == nil || argvLines(t, log) != nil {
		t.Error("the parser or the builder ran")
	}
}

// The same refusal stops a deploy before the confirmation and before any
// request to the control plane.
func TestMalformedProjectPlatformsStopDeployBeforeTheServer(t *testing.T) {
	dir := platformProject(t, "build:\n  platforms: [arm64]\n")
	builder, log := argvBuilder(t)
	fake := &deployServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "build.platforms") {
		t.Fatalf("err = %v, want build.platforms named", err)
	}
	if fake.reads != 0 || fake.registers != 0 || strings.Contains(out, "server:") || strings.Contains(out, "Deploy ") {
		t.Errorf("reads=%d registers=%d output=%q: the deploy went on past the refusal", fake.reads, fake.registers, out)
	}
	if argvLines(t, log) != nil {
		t.Error("the builder ran")
	}
}

// A malformed value from the workspace defaults is refused with the
// workspace file named.
func TestMalformedWorkspacePlatformsAreRefused(t *testing.T) {
	dir := platformProject(t, "")
	ws := filepath.Dir(dir)
	writeProjectFile(t, ws, "dexaflow.workspace.yaml", "schema_version: \"1.0\"\ndefaults:\n  platforms: [arm64]\n")
	t.Chdir(ws)
	builder, log := argvBuilder(t)
	_, _, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "dexaflow.workspace.yaml") {
		t.Fatalf("err = %v, want the workspace file named", err)
	}
	if argvLines(t, log) != nil {
		t.Error("the builder ran")
	}
}

// dexaflow build checks every project before it builds any, so one bad
// project leaves no partial workspace pushed.
func TestBuildCommandChecksEveryProjectBeforeBuilding(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	ws := t.TempDir()
	writeBuildProject(t, ws, "alpha", "alpha-img")
	bad := writeBuildProject(t, ws, "zeta", "zeta-img")
	writeProjectFile(t, bad, "dexaflow.yaml", "dag_id: zeta\nregistry:\n  url: reg.io/team\n  image_name: zeta-img\nbuild:\n  platforms: [arm64]\n")
	t.Setenv("LEOFLOW_PARSER_CMD", stubParser(t, t.TempDir()))
	builder, log := argvBuilder(t)
	_, _, err := run(t, "build", ws, "--push", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "build.platforms") {
		t.Fatalf("err = %v, want the bad project's platforms refused", err)
	}
	if calls := argvLines(t, log); calls != nil {
		t.Errorf("built before the refusal: %q", calls)
	}
}

// Without --force, the content label covers the platform: a deploy for
// another platform is not "already deployed", it builds. The registry holds
// only an amd64 image, so the skip path cannot serve arm64 either.
func TestDeployForAnotherPlatformBuilds(t *testing.T) {
	dir, _ := platformProjectWith(t, amd64Only, "")
	label := labelOf(t, dir)
	builder, log := argvBuilder(t)
	fake := &deployServer{current: label, registered: []string{label}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder)
	if err != nil || !strings.Contains(out, "already deployed as "+label) || argvLines(t, log) != nil {
		t.Fatalf("the amd64 deploy of the current label: err=%v built=%v\n%s", err, argvLines(t, log) != nil, out)
	}
	out, _, err = run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--builder", builder, "--platform", "linux/arm64")
	if err != nil {
		t.Fatalf("deploy --platform linux/arm64: %v\n%s", err, out)
	}
	calls := argvLines(t, log)
	if len(calls) == 0 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 ") {
		t.Errorf("builder calls = %q, want a build for linux/arm64", calls)
	}
	if strings.Contains(out, "already deployed") || fake.registers != 1 {
		t.Errorf("registers=%d, output:\n%s", fake.registers, out)
	}
}

func TestPlatformFlagRefusesAMalformedValue(t *testing.T) {
	dir := platformProject(t, "")
	builder, log := argvBuilder(t)
	for _, cmd := range [][]string{
		{"compile", dir, "--build", "--builder", builder},
		{"deploy", dir, "--yes", "--server", "http://127.0.0.1:1", "--token", "x", "--builder", builder},
		{"build", filepath.Dir(dir), "--builder", builder},
	} {
		_, _, err := run(t, append(cmd, "--platform", "arm64")...)
		if err == nil || !strings.Contains(err.Error(), "--platform") || !strings.Contains(err.Error(), "os/arch") {
			t.Errorf("%s: err = %v, want the flag and the expected form named", cmd[0], err)
		}
	}
	if argvLines(t, log) != nil {
		t.Error("the builder ran with a malformed platform")
	}
}

func TestBuildCommandTakesThePlatformFlag(t *testing.T) {
	dir := platformProject(t, "")
	builder, log := argvBuilder(t)
	out, _, err := run(t, "build", filepath.Dir(dir), "--builder", builder, "--platform", "linux/arm64")
	if err != nil {
		t.Fatalf("build --platform: %v", err)
	}
	calls := argvLines(t, log)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 ") {
		t.Fatalf("builder calls = %q, want one build for linux/arm64", calls)
	}
	if !strings.Contains(out, "platforms:  linux/arm64 (from --platform)") {
		t.Errorf("output does not name the platform's source:\n%s", out)
	}
}

// The whole deploy path: the platform flag reaches the build, the output names
// it once with its source and names the builder, and the image registered is
// the digest the registry holds for what the builder pushed (ADR 0075).
func TestDeployPlatformFlagAndBuilderLine(t *testing.T) {
	dir, reg := platformProjectWith(t, singleManifest, "")
	builder, log := argvBuilder(t)
	fake := &deployServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--force",
		"--builder", builder, "--platform", "linux/arm64")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if n := linesStartingWith(out, "platforms:  linux/arm64 (from --platform)"); n != 1 {
		t.Errorf("the platforms line appears %d time(s), want once:\n%s", n, out)
	}
	if !strings.Contains(out, "builder:    local-daemon ("+builder+"), fallback") {
		t.Errorf("output does not name the builder path:\n%s", out)
	}
	if want := "image " + reg.host + "/team/etl@" + reg.digest; !strings.Contains(out, want) {
		t.Errorf("the registered image is not the registry's pin %q:\n%s", want, out)
	}
	calls := argvLines(t, log)
	if len(calls) != 2 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 ") || !strings.HasPrefix(calls[1], "push ") {
		t.Errorf("builder calls = %q, want build and push once each, and no inspect", calls)
	}
	if reg.readCount() != 1 {
		t.Errorf("the registry was read %d time(s), want once", reg.readCount())
	}
	if fake.registers != 1 {
		t.Errorf("registered %d time(s), want once", fake.registers)
	}
}

// --skip-build builds nothing: no builder line, the tool never runs, and the
// digest comes from the registry, so an image any tool pushed (a CI job, a
// cloud builder) deploys from a machine that never had it.
func TestDeploySkipBuildPrintsNoBuilder(t *testing.T) {
	dir, reg := platformProjectWith(t, singleManifest, "")
	builder, log := argvBuilder(t)
	fake := &deployServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--force",
		"--builder", builder, "--skip-build")
	if err != nil {
		t.Fatalf("deploy --skip-build: %v\n%s", err, out)
	}
	if strings.Contains(out, "builder:") {
		t.Errorf("--skip-build printed a builder:\n%s", out)
	}
	if calls := argvLines(t, log); calls != nil {
		t.Errorf("builder calls = %q, want none", calls)
	}
	if want := "image " + reg.host + "/team/etl@" + reg.digest; !strings.Contains(out, want) {
		t.Errorf("the registered image is not the registry's pin %q:\n%s", want, out)
	}
}

// --skip-build of an image the registry does not have stops before the
// register, naming the image and where credentials come from.
func TestDeploySkipBuildOfAMissingImageRegistersNothing(t *testing.T) {
	dir := versionProject(t, "registry:\n  url: "+missingRegistry(t)+"/team\n  image_name: etl\n")
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	fake := &deployServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--force", "--skip-build")
	if err == nil || !strings.Contains(err.Error(), "/team/etl:") || !strings.Contains(err.Error(), "docker login") {
		t.Fatalf("err = %v, want the image and the credentials named", err)
	}
	if fake.registers != 0 {
		t.Errorf("registered %d time(s) without a digest", fake.registers)
	}
}

// An explicit empty build.platforms selects the tool's own default, as on
// main: nothing is refused and no --platform reaches the tool.
func TestEmptyProjectPlatformsLeaveTheToolDefault(t *testing.T) {
	dir := platformProject(t, "build:\n  platforms: []\n")
	builder, log := argvBuilder(t)
	if _, _, err := run(t, "compile", dir, "--build", "--builder", builder); err != nil {
		t.Fatalf("compile --build with platforms: []: %v", err)
	}
	if calls := argvLines(t, log); len(calls) != 1 || strings.Contains(calls[0], "--platform") {
		t.Errorf("builder calls = %q, want one build without --platform", calls)
	}
}

// A tool that builds and pushes only part of the requested platforms (podman
// without --manifest keeps the last one under the tag) is caught after the
// push by reading the registry, and nothing is registered.
func TestDeployRefusesAPushMissingAPlatform(t *testing.T) {
	dir, reg := platformProjectWith(t, singleManifest, "")
	builder, log := argvBuilder(t)
	fake := &deployServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	_, _, err := run(t, "deploy", dir, "--server", srv.URL, "--token", "x", "--yes", "--force",
		"--builder", builder, "--platform", "linux/amd64,linux/arm64")
	if err == nil || !strings.Contains(err.Error(), "missing linux/amd64,linux/arm64") {
		t.Fatalf("err = %v, want the missing platforms named", err)
	}
	if fake.registers != 0 {
		t.Errorf("registered %d time(s) an image that does not cover the platforms", fake.registers)
	}
	if calls := argvLines(t, log); len(calls) != 2 || reg.readCount() != 1 {
		t.Errorf("builder calls = %q, registry reads = %d, want build, push and one registry read", calls, reg.readCount())
	}
}
