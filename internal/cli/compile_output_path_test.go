package cli

import (
	"os"
	"path/filepath"
	"testing"
)

// `--output` defaulted to the bare name `dag.json`, which resolves against the
// CURRENT directory rather than the project the command was pointed at. So
// `leoflow compile /tmp/probe --build` from a checkout overwrote that
// checkout's own tracked dag.json: the compile succeeded, the artifact it
// printed was correct, and the damage was to a file the command was never asked
// to touch. This repo alone carries 22 committed dag.json files, and the docs'
// own quickstart tells you to compile from a checkout (#1084).
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

	// The compile itself may fail for want of a parser on this host; what is
	// under test is that the working directory is untouched either way.
	_, _, _ = run(t, "compile", proj)

	got, rerr := os.ReadFile(victim)
	if rerr != nil {
		t.Fatalf("compile removed a file in the working directory: %v", rerr)
	}
	if string(got) != sentinel {
		t.Errorf("compile overwrote an unrelated dag.json in the working directory:\nwant %s\ngot  %s", sentinel, got)
	}
}
