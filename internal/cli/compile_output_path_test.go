package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `--output` defaulted to the bare name `dag.json`, which resolves against the
// CURRENT directory rather than the project the command was pointed at. So
// `leoflow compile /tmp/probe --build` from a checkout overwrote that
// checkout's own tracked dag.json: the compile succeeded, the artifact it
// printed was correct, and the damage was to a file the command was never asked
// to touch (#1084). It is recoverable through git when the target happened to be
// tracked, and not necessarily otherwise.
func TestDefaultOutputResolvesAgainstTheProjectNotTheCwd(t *testing.T) {
	for _, tc := range []struct{ name, dir, want string }{
		{name: "an explicit project directory", dir: filepath.FromSlash("/tmp/probe"), want: filepath.FromSlash("/tmp/probe/dag.json")},
		{name: "a relative project directory", dir: filepath.FromSlash("dags/sales"), want: filepath.FromSlash("dags/sales/dag.json")},
		// The common interactive case is compiling the project you are standing
		// in, and that must keep writing ./dag.json exactly as before: it is
		// what every documented pipeline reads back.
		{name: "the current directory", dir: ".", want: "dag.json"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := defaultOutputPath(tc.dir, ""); got != tc.want {
				t.Errorf("defaultOutputPath(%q) = %q, want %q", tc.dir, got, tc.want)
			}
		})
	}
	// An explicit -o is the operator's choice and is never rewritten.
	if got := defaultOutputPath(filepath.FromSlash("/tmp/probe"), "out/mine.json"); got != "out/mine.json" {
		t.Errorf("an explicit --output must be used verbatim, got %q", got)
	}
}

// The end-to-end shape of the accident, which is what the issue asked for: a
// project in a temp dir, compiled from a different working directory, must not
// write anything into that working directory.
func TestCompileLeavesTheWorkingDirectoryAlone(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "probe")
	if _, _, err := run(t, "init", proj); err != nil {
		t.Fatal(err)
	}
	// A committed artifact in the working directory, standing in for the
	// checkout's own tracked dag.json.
	cwd := t.TempDir()
	victim := filepath.Join(cwd, "dag.json")
	const sentinel = `{"dag_id":"someone_elses_dag"}`
	if err := os.WriteFile(victim, []byte(sentinel), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	// The compile has to actually RUN, or this test is a no-op that passes with
	// the bug present: with the fix reverted and the parser unavailable it
	// still goes green, because nothing was written anywhere. Asserting the
	// artifact landed is what makes the assertion below mean something.
	_, stderr, cerr := run(t, "compile", proj)
	if cerr != nil {
		t.Fatalf("compile failed, so this test would prove nothing: %v (%s)", cerr, stderr)
	}
	if _, serr := os.Stat(filepath.Join(proj, "dag.json")); serr != nil {
		t.Fatalf("compile reported success but wrote no artifact next to the project: %v", serr)
	}

	got, rerr := os.ReadFile(victim)
	if rerr != nil {
		t.Fatalf("compile removed a file in the working directory: %v", rerr)
	}
	if string(got) != sentinel {
		t.Errorf("compile overwrote an unrelated dag.json in the working directory:\nwant %s\ngot  %s", sentinel, got)
	}
}

// Moving the artifact into the project directory made a previously working case
// fail: a project directory that is not writable. Before, the write went to the
// (writable) cwd and succeeded. The failure the user saw was a raw
// PermissionError traceback out of the parser, which is exactly the shape the
// troubleshooting page says recent builds stopped producing.
//
// Cases that reach it: a source tree mounted read-only into a build container,
// a checkout owned by another user, a read-only CI volume.
func TestAnUnwritableProjectDirectoryIsExplained(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores the directory mode")
	}
	proj := filepath.Join(t.TempDir(), "ro")
	if _, _, err := run(t, "init", proj); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(proj, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(proj, 0o700) })

	_, stderr, err := run(t, "compile", proj)
	if err == nil {
		t.Fatal("compiling into an unwritable directory must fail")
	}
	combined := err.Error() + "\n" + stderr
	if !strings.Contains(combined, "-o") {
		t.Errorf("the error must point at the way out (-o), got: %s", combined)
	}
	if strings.Contains(combined, "Traceback") || strings.Contains(combined, "PermissionError") {
		t.Errorf("the error is a raw parser traceback rather than something the user can act on: %s", combined)
	}
}

// The default has to hold for every caller of runCompile, not only the one that
// goes through cobra. A compileOptions literal with no output reached a write
// with an empty path and produced a parser traceback.
func TestRunCompileDefaultsTheOutputForNonFlagCallers(t *testing.T) {
	proj := filepath.Join(t.TempDir(), "p")
	if _, _, err := run(t, "init", proj); err != nil {
		t.Fatal(err)
	}
	cmd := devTestCmd()
	cmd.SetContext(context.Background())
	// The compile may fail for want of a parser on this host; what is asserted
	// is that it never tried to write to "".
	_ = runCompile(cmd, proj, compileOptions{image: "x:dev", dagVersion: "v1"})
	if _, err := os.Stat("dag.json"); err == nil {
		t.Error(`runCompile wrote to the working directory for a caller that set no output`)
	}
}

// The artifact now lands in the user's source tree, so the first `git add .`
// after the first compile would commit a build artifact into every DAG repo.
// This repository needed a .gitignore rule for exactly that reason, and the one
// stray `dag.json` it still tracks (a `cyc_demo` scratch file) is what the rule
// was added too late to prevent.
func TestScaffoldIgnoresTheCompiledArtifact(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "p")
	if _, err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	b, rerr := os.ReadFile(filepath.Join(dir, ".gitignore"))
	if rerr != nil {
		t.Fatalf("the scaffold ships no .gitignore, so the first compile leaves an artifact staged: %v", rerr)
	}
	if !strings.Contains(string(b), "dag.json") {
		t.Errorf(".gitignore does not cover the compiled artifact:\n%s", b)
	}
}
