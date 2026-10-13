package cli

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// argvBuilder stands in for docker: it appends every argv to log, one call per
// line, and answers `inspect` with a digest-pinned reference of the image, so
// a deploy can pin what it pushed.
func argvBuilder(t *testing.T) (builder, log string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	dir := t.TempDir()
	builder = filepath.Join(dir, "docker")
	log = filepath.Join(dir, "argv.log")
	script := "#!/bin/sh\n" +
		"echo \"$*\" >> \"" + log + "\"\n" +
		"if [ \"$1\" = inspect ]; then img=\"$4\"; printf '%s@sha256:0123abcd\\n' \"${img%:*}\"; fi\n" +
		"exit 0\n"
	if err := os.WriteFile(builder, []byte(script), 0o700); err != nil { //nolint:gosec // a test fixture that must be executable
		t.Fatal(err)
	}
	return builder, log
}

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

// platformProject is a project with a registry whose parser is stubbed.
func platformProject(t *testing.T, extraYAML string) string {
	t.Helper()
	dir := versionProject(t, "registry:\n  url: reg.io/team\n  image_name: etl\n"+extraYAML)
	t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, filepath.Join(t.TempDir(), "parser.log")))
	return dir
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
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 -t reg.io/team/etl:") {
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

// Several platforms on the local daemon fail before the parser or the builder
// runs, with the platforms and the way out named.
func TestCompileRefusesSeveralPlatformsOnTheLocalDaemon(t *testing.T) {
	for _, args := range [][]string{
		{"--platform", "linux/amd64,linux/arm64"},
		{"--platform", "linux/amd64", "--platform", "linux/arm64"},
	} {
		dir := platformProject(t, "")
		builder, log := argvBuilder(t)
		parserLog := filepath.Join(t.TempDir(), "parser.log")
		t.Setenv("LEOFLOW_PARSER_CMD", recordingParser(t, parserLog))
		_, _, err := run(t, append([]string{"compile", dir, "--build", "--builder", builder}, args...)...)
		if err == nil {
			t.Fatalf("%v: a multi-platform build on the local daemon was accepted", args)
		}
		for _, want := range []string{"linux/amd64,linux/arm64", "--platform", "local-daemon"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: error %q does not name %q", args, err, want)
			}
		}
		if argvLines(t, log) != nil {
			t.Errorf("%v: the builder ran", args)
		}
		if _, serr := os.Stat(parserLog); serr == nil {
			t.Errorf("%v: the parser ran before the refusal", args)
		}
	}
}

// The same list from dexaflow.yaml is refused the same way.
func TestCompileRefusesSeveralPlatformsFromTheProject(t *testing.T) {
	dir := platformProject(t, "build:\n  platforms: [linux/amd64, linux/arm64]\n")
	builder, log := argvBuilder(t)
	_, _, err := run(t, "compile", dir, "--build", "--builder", builder)
	if err == nil || !strings.Contains(err.Error(), "linux/amd64,linux/arm64") {
		t.Fatalf("err = %v, want the multi-platform refusal", err)
	}
	if argvLines(t, log) != nil {
		t.Error("the builder ran")
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
// the digest the builder's result carried.
func TestDeployPlatformFlagAndBuilderLine(t *testing.T) {
	dir := platformProject(t, "")
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
	if !strings.Contains(out, "image reg.io/team/etl@sha256:0123abcd") {
		t.Errorf("the registered image is not the pinned result:\n%s", out)
	}
	calls := argvLines(t, log)
	if len(calls) != 3 || !strings.HasPrefix(calls[0], "build --platform linux/arm64 ") ||
		!strings.HasPrefix(calls[1], "push ") || !strings.HasPrefix(calls[2], "inspect ") {
		t.Errorf("builder calls = %q, want build, push, inspect once each", calls)
	}
	if fake.registers != 1 {
		t.Errorf("registered %d time(s), want once", fake.registers)
	}
}

// --skip-build builds nothing: no builder line, and the digest still comes
// from the image the tool already pushed.
func TestDeploySkipBuildPrintsNoBuilder(t *testing.T) {
	dir := platformProject(t, "")
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
	calls := argvLines(t, log)
	if len(calls) != 1 || !strings.HasPrefix(calls[0], "inspect ") {
		t.Errorf("builder calls = %q, want one inspect", calls)
	}
	if !strings.Contains(out, "image reg.io/team/etl@sha256:0123abcd") {
		t.Errorf("the registered image is not pinned:\n%s", out)
	}
}
