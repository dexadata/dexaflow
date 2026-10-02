package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// writeBuildProject lays down a minimal buildable project: a dag.py and a
// dexaflow.yaml with a registry, which is all DiscoverProjects needs.
func writeBuildProject(t *testing.T, ws, name, imageName string) string {
	t.Helper()
	dir := filepath.Join(ws, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("# dag\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	yaml := "dag_id: " + name + "\nregistry:\n  url: reg.io/team\n  image_name: " + imageName + "\n"
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

// stubParser writes the dag.json the real Python parser would, so the compile
// step this command reuses can run without an interpreter.
func stubParser(t *testing.T, dir string) string {
	t.Helper()
	p := filepath.Join(dir, "parser.sh")
	body := "#!/usr/bin/env bash\n" +
		"out=\"\"; dagid=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in --output) out=\"$2\"; shift 2;; --source) dagid=\"$(basename \"$(dirname \"$2\")\")\"; shift 2;; *) shift;; esac; done\n" +
		"cat > \"$out\" <<JSON\n" +
		"{\"schema_version\":\"1.0\",\"dag_id\":\"$dagid\",\"dag_version\":\"v0\",\"image\":\"i:1\"," +
		"\"tasks\":[{\"task_id\":\"t\",\"type\":\"python\",\"entrypoint\":\"dag:t\"}]}\n" +
		"JSON\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	return p
}

// stubBuilder stands in for docker and appends one line per invocation:
// `<-t value>\t<-f value>\t<file|dir|missing>`. It exits non-zero for any image
// whose name contains failFor, which is how the stop-on-failure path is driven.
func stubBuilder(t *testing.T, dir, log, failFor string) string {
	t.Helper()
	p := filepath.Join(dir, "builder.sh")
	fail := "false"
	if failFor != "" {
		fail = "[[ \"$tag\" == *" + failFor + "* ]]"
	}
	body := "#!/usr/bin/env bash\n" +
		"tag=\"\"; file=\"\"\n" +
		"while [ $# -gt 0 ]; do case \"$1\" in -t) tag=\"$2\"; shift 2;; -f) file=\"$2\"; shift 2;; *) shift;; esac; done\n" +
		"kind=missing; [ -d \"$file\" ] && kind=dir; [ -f \"$file\" ] && kind=file\n" +
		"printf '%s\\t%s\\t%s\\n' \"$tag\" \"$file\" \"$kind\" >> " + log + "\n" +
		"if " + fail + "; then echo 'builder refused' >&2; exit 1; fi\n"
	if err := os.WriteFile(p, []byte(body), 0o755); err != nil { //nolint:gosec // test fixture must be executable
		t.Fatal(err)
	}
	return p
}

// runBuildCmd drives the real root command, which is the point: buildTargets can
// derive a perfect reference and the command still shell out with a broken one.
func runBuildCmd(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	root := NewRootCommand()
	var out, errOut bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), err
}

// TestBuildCommandDrivesTheBuilder locks what `leoflow build` actually hands the
// builder — the wiring, not the helper. buildTargets can derive a perfect
// reference and the command still shell out with a broken one, which is exactly
// what happened: the raw (empty) --dag-version/--sha flags reached
// deployImageRef, the default tag strategy resolved both to "", and every image
// went out as `<url>/<name>:` — a reference docker refuses. And with no
// dockerfile named, ensureDockerfile stat'd the project directory, found it, and
// passed a DIRECTORY as -f, so the generated Dockerfile was never produced.
//
// Both defects are invisible to any test that stops at buildTargets.
func TestBuildCommandDrivesTheBuilder(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	ws := t.TempDir()
	bin := t.TempDir()
	writeBuildProject(t, ws, "alpha", "alpha-img")
	writeBuildProject(t, ws, "beta", "beta-img")
	log := filepath.Join(bin, "argv.log")
	t.Setenv("LEOFLOW_PARSER_CMD", stubParser(t, bin))

	out, err := runBuildCmd(t, "build", ws,
		"--builder", stubBuilder(t, bin, log, ""),
		"--dag-version", "v9.9.9")
	if err != nil {
		t.Fatalf("build: %v (out=%s)", err, out)
	}

	lines := nonEmptyLines(t, log)
	if len(lines) != 2 {
		t.Fatalf("builder ran %d time(s), want one per project: %q", len(lines), lines)
	}
	for _, ln := range lines {
		f := strings.Split(ln, "\t")
		if len(f) != 3 {
			t.Fatalf("malformed stub line %q", ln)
		}
		tag, dockerfile, kind := f[0], f[1], f[2]
		if strings.HasSuffix(tag, ":") || !strings.HasPrefix(tag, "reg.io/team/") {
			t.Errorf("-t %q: want reg.io/team/<name>:<version>, never an empty tag", tag)
		}
		if !strings.HasSuffix(tag, ":v9.9.9") {
			t.Errorf("-t %q: the resolved --dag-version must reach the tag", tag)
		}
		if kind != "file" {
			t.Errorf("-f %q resolved to a %s; the builder needs a Dockerfile, not the project directory", dockerfile, kind)
		}
	}
}

// TestBuildCommandResolvesVersionFromGitWhenNotGiven: with no --dag-version the
// command must fall back exactly as `leoflow deploy` does. Without the fallback
// the tag is empty for every tag strategy, which is the default invocation.
func TestBuildCommandResolvesVersionFromGitWhenNotGiven(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	ws := t.TempDir()
	bin := t.TempDir()
	writeBuildProject(t, ws, "alpha", "alpha-img")
	log := filepath.Join(bin, "argv.log")
	t.Setenv("LEOFLOW_PARSER_CMD", stubParser(t, bin))

	if _, err := runBuildCmd(t, "build", ws, "--builder", stubBuilder(t, bin, log, "")); err != nil {
		t.Fatalf("build: %v", err)
	}
	tag := strings.Split(nonEmptyLines(t, log)[0], "\t")[0]
	if strings.HasSuffix(tag, ":") {
		t.Fatalf("-t %q has an empty tag: the default invocation must resolve a version (git describe, else dev)", tag)
	}
}

// TestBuildCommandStopsAndNamesTheFailingProject: a half-built workspace is
// worse than a clear failure because afterwards nothing distinguishes a new
// image from a stale one. The run must stop, name the project, and say what it
// already built.
func TestBuildCommandStopsAndNamesTheFailingProject(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	ws := t.TempDir()
	bin := t.TempDir()
	writeBuildProject(t, ws, "alpha", "alpha-img")
	writeBuildProject(t, ws, "beta", "beta-img")
	writeBuildProject(t, ws, "gamma", "gamma-img")
	log := filepath.Join(bin, "argv.log")
	t.Setenv("LEOFLOW_PARSER_CMD", stubParser(t, bin))

	// alpha builds, beta refuses, gamma must never be attempted.
	out, err := runBuildCmd(t, "build", ws,
		"--builder", stubBuilder(t, bin, log, "beta-img"),
		"--dag-version", "v1")
	if err == nil {
		t.Fatal("a refused build must fail the command")
	}
	if !strings.Contains(err.Error(), "beta") {
		t.Errorf("error %q does not name the failing project", err)
	}
	lines := nonEmptyLines(t, log)
	if len(lines) != 2 {
		t.Fatalf("builder ran %d time(s); the run must stop at beta and never reach gamma: %q", len(lines), lines)
	}
	if strings.Contains(strings.Join(lines, "\n"), "gamma") {
		t.Error("gamma was built after beta failed")
	}
	// Anchored on the summary line, not on "alpha" anywhere in the output: the
	// per-project progress line already prints alpha before the build starts,
	// so a bare Contains(out, "alpha") passes with no summary at all.
	summary := ""
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, "built before the failure:") {
			summary = l
		}
	}
	if summary == "" {
		t.Fatalf("the failure output must say which images were already built; got %q", out)
	}
	if !strings.Contains(summary, "alpha") || strings.Contains(summary, "beta") {
		t.Errorf("summary %q must name alpha (built) and not beta (failed)", summary)
	}
}

// TestBuildCommandRepeatsSkipsInTheSummary: the skip warnings are printed before
// minutes of builder output, so a closing "built N image(s)" on its own reads as
// "the workspace is built".
func TestBuildCommandRepeatsSkipsInTheSummary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell stubs are POSIX-only")
	}
	ws := t.TempDir()
	bin := t.TempDir()
	writeBuildProject(t, ws, "alpha", "alpha-img")
	noReg := filepath.Join(ws, "orphan")
	if err := os.MkdirAll(noReg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noReg, "dag.py"), []byte("# dag\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(noReg, "dexaflow.yaml"), []byte("dag_id: orphan\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LEOFLOW_PARSER_CMD", stubParser(t, bin))

	out, err := runBuildCmd(t, "build", ws,
		"--builder", stubBuilder(t, bin, filepath.Join(bin, "argv.log"), ""),
		"--dag-version", "v1")
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if !strings.Contains(out, "skipped") || !strings.Contains(out, "orphan") {
		t.Errorf("final summary %q must repeat what was not built", out)
	}
}

// TestBuildCommandIsRegistered: the command is only reachable if root wires it.
func TestBuildCommandIsRegistered(t *testing.T) {
	for _, c := range NewRootCommand().Commands() {
		if c.Name() == "build" {
			return
		}
	}
	t.Fatal("`leoflow build` is not registered on the root command")
}

func nonEmptyLines(t *testing.T, path string) []string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("builder was never invoked: %v", err)
	}
	var out []string
	for _, l := range strings.Split(string(data), "\n") {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}
