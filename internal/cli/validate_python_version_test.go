package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// `leoflow validate` checked dag.py's syntax under whatever interpreter it
// found, never consulting python_version (#1094). The failure is the annoying
// direction: valid 3.12+ code is REJECTED, because `type X[T]` is a SyntaxError
// on 3.11, so validate fails a DAG the cluster runs fine and the author has no
// way to tell the complaint is about the checker.
//
// It now asks the same question `leoflow dev` asks, with the same exemptions.
func TestValidateEnforcesTheDeclaredPythonVersion(t *testing.T) {
	cases := []struct {
		name string
		cfg  *domain.LeoflowConfig
		want string
	}{
		{
			name: "a declared version is honored",
			cfg:  &domain.LeoflowConfig{PythonVersion: "3.13"},
			want: "3.13",
		},
		{
			name: "a defaulted version is not: the author said nothing, so any supported interpreter is fair",
			cfg:  &domain.LeoflowConfig{PythonVersion: "3.11", PythonVersionDefaulted: true},
			want: "",
		},
		{
			name: "base_image wins: python_version is inert when the FROM is chosen by hand",
			cfg:  &domain.LeoflowConfig{PythonVersion: "3.13", BaseImage: "ghcr.io/x/y:tag"},
			want: "",
		},
		{
			name: "no config at all",
			cfg:  nil,
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := validateEnforcedPythonVersion(tc.cfg); got != tc.want {
				t.Errorf("validateEnforcedPythonVersion = %q, want %q", got, tc.want)
			}
		})
	}
}

// A deprecated version must not be enforced: the project is on its way off it,
// and refusing to lint until the author installs an interpreter we are telling
// them to abandon helps nobody. Same exemption `leoflow dev` makes.
func TestValidateDoesNotEnforceADeprecatedVersion(t *testing.T) {
	// 3.10 is the version the schema marks deprecated (see .changie/the schema's
	// x-leoflow-python-deprecations); skip if that ever stops being true rather
	// than asserting against a moving list.
	const dep = "3.10"
	if _, ok := domain.DeprecatedPythonVersion(dep); !ok {
		t.Skipf("%s is no longer marked deprecated", dep)
	}
	if got := validateEnforcedPythonVersion(&domain.LeoflowConfig{PythonVersion: dep}); got != "" {
		t.Errorf("a deprecated version (%s) must not be enforced, got %q", dep, got)
	}
}

// The skip warning has to name an interpreter the reader can actually install.
// The first version of it interpolated the full `python_version` where the
// minor belonged and told people to `install python3.3.13`, which does not
// exist. A message that sends someone looking for a package that was never
// published is worse than no message, because it reads as authoritative.
func TestValidateSkipWarningNamesAnInstallableInterpreter(t *testing.T) {
	cmd := devTestCmd()
	cmd.SetContext(context.Background())
	// A minor nothing on any machine reports, so the unreachable branch is the
	// one under test regardless of what the host has installed.
	const unreachable = "3.99"
	if err := checkDagSyntaxUnder(cmd, filepath.Join(t.TempDir(), "dag.py"), unreachable); err != nil {
		t.Fatalf("an unreachable interpreter must warn, not fail: %v", err)
	}
	got := cmd.ErrOrStderr().(*bytes.Buffer).String()
	if !strings.Contains(got, "python3.99") {
		t.Errorf("warning must name the interpreter to install, got:\n%s", got)
	}
	if strings.Contains(got, "python3.3.99") {
		t.Errorf("warning names a nonexistent interpreter (python3.<full version>), got:\n%s", got)
	}
	// It must still point at the declared version itself, so the reader can
	// connect the warning to the line in their dexaflow.yaml.
	if !strings.Contains(got, "python_version 3.99") {
		t.Errorf("warning must quote the declared python_version, got:\n%s", got)
	}
}

// The asymmetry that keeps the fix from becoming a bigger bug than the one it
// fixes. Python's grammar grows, so linting under an OLDER interpreter than the
// project declares is what produced #1094; linting under a NEWER one accepts
// everything the declared minor accepts, so a syntax error there is real.
func TestSyntaxCheckTrustsAnInterpreterAtLeastAsNew(t *testing.T) {
	cases := []struct {
		want, have int
		trust      bool
		why        string
	}{
		{want: 11, have: 11, trust: true, why: "the declared minor itself"},
		{want: 11, have: 13, trust: true, why: "newer accepts everything 3.11 accepts"},
		{want: 13, have: 11, trust: false, why: "#1094: 3.11 rejects valid 3.13 syntax"},
		{want: 13, have: 12, trust: false, why: "one minor behind is still behind"},
		{want: 11, have: 9, trust: false, why: "an unsupported interpreter is not a second opinion"},
	}
	for _, c := range cases {
		if got := syntaxCheckIsTrustworthy(c.want, c.have); got != c.trust {
			t.Errorf("syntaxCheckIsTrustworthy(want=3.%d, have=3.%d) = %v, want %v (%s)",
				c.want, c.have, got, c.trust, c.why)
		}
	}
}

// The regression this pair exists to stop: `leoflow init` writes python_version
// explicitly, so EVERY scaffolded project takes the strict path. Skipping the
// lint whenever that exact minor is missing would have stopped validate
// catching a broken dag.py on any host that simply has a newer python3, which
// is most of them. CI is one: it has no python3.11, and this is the shape that
// failed there.
func TestSyntaxCheckStillCatchesABrokenDagUnderANewerInterpreter(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; this asserts the fallback, which needs one")
	}
	dag := filepath.Join(t.TempDir(), "dag.py")
	if err := os.WriteFile(dag, []byte("this is not valid python\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := devTestCmd()
	cmd.SetContext(context.Background())
	// A minor far below anything installed, so the exact-match resolution is
	// guaranteed to miss and the fallback is the path under test.
	if err := checkDagSyntaxUnder(cmd, dag, "3.5"); err == nil {
		t.Fatal("a broken dag.py must still be rejected when only a NEWER interpreter is installed")
	}
}

// The other half: a declared minor NEWER than anything installed must not fail
// the build on a syntax error that the older interpreter may simply not
// understand. Warn and skip, which is #1094's fix.
func TestSyntaxCheckSkipsRatherThanJudgeUnderAnOlderInterpreter(t *testing.T) {
	dag := filepath.Join(t.TempDir(), "dag.py")
	if err := os.WriteFile(dag, []byte("this is not valid python\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := devTestCmd()
	cmd.SetContext(context.Background())
	if err := checkDagSyntaxUnder(cmd, dag, "3.99"); err != nil {
		t.Fatalf("an unreachable newer minor must warn, not fail: %v", err)
	}
	if got := cmd.ErrOrStderr().(*bytes.Buffer).String(); !strings.Contains(got, "skipping dag.py syntax check") {
		t.Errorf("expected a skip warning, got: %q", got)
	}
}

// The WIRING, not the helper. An adversarial review no-oped the call site in
// checkDagPythonSyntax and the whole package stayed green: every test drove the
// new functions directly, so what they proved was that a pure function returns
// the right string, not that validate consults python_version at all.
//
// This drives the real command against a stub interpreter that reports 3.12 and
// rejects everything it is asked to compile, with the project declaring 3.13.
// With the wiring, validate sees an interpreter older than the declared minor
// and skips with a warning, so the command succeeds. Without it, validate falls
// through to that same interpreter and the stub's refusal becomes a syntax
// error. The two outcomes are opposite, which is what makes the test sensitive
// to the wiring rather than to the helper.
func TestValidateActuallyConsultsPythonVersion(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("sh stub is POSIX-only")
	}
	dir := filepath.Join(t.TempDir(), "proj")
	if _, _, err := run(t, "init", dir); err != nil {
		t.Fatal(err)
	}
	cfg := filepath.Join(dir, "dexaflow.yaml")
	b, rerr := os.ReadFile(cfg)
	if rerr != nil {
		t.Fatal(rerr)
	}
	out := strings.ReplaceAll(string(b), `python_version: "3.11"`, `python_version: "3.13"`)
	if out == string(b) {
		t.Fatalf("the scaffold no longer declares python_version 3.11; this test needs it to:\n%s", b)
	}
	if err := os.WriteFile(cfg, []byte(out), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "dag.py"), []byte("this is not valid python\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// The only interpreter reachable is one minor behind what the project
	// declares, and it refuses whatever it is asked to compile.
	stubDir := t.TempDir()
	stub := "#!/bin/sh\ncase \"$1\" in --version) echo 'Python 3.12.0'; exit 0;; esac\n" +
		"echo 'SyntaxError: invalid syntax' >&2\nexit 1\n"
	if err := os.WriteFile(filepath.Join(stubDir, "python3"), []byte(stub), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", stubDir)
	t.Setenv("HOME", t.TempDir()) // no managed interpreter either

	_, stderr, err := run(t, "validate", dir)
	if err != nil {
		t.Fatalf("validate must skip the lint rather than judge the DAG under an older interpreter: %v (%s)", err, stderr)
	}
	if !strings.Contains(stderr, "skipping dag.py syntax check") || !strings.Contains(stderr, "3.13") {
		t.Errorf("expected a skip warning naming the declared version, got: %q", stderr)
	}
}

// The comment on validateEnforcedPythonVersion claims the two functions cannot
// drift silently. Nothing enforced that: every test drove one of them. Run both
// over the same configs so the claim is true.
func TestValidateAndDevAgreeOnTheEnforcedVersion(t *testing.T) {
	cfgs := []*domain.LeoflowConfig{
		{PythonVersion: "3.13"},
		{PythonVersion: "3.12"},
		{PythonVersion: "3.11", PythonVersionDefaulted: true},
		{PythonVersion: "3.13", BaseImage: "ghcr.io/x/y:tag"},
		{PythonVersion: "3.10"},
		{PythonVersion: "3.10", PythonVersionDefaulted: true},
		{PythonVersion: ""},
		nil,
	}
	for _, cfg := range cfgs {
		// devEnforcedPythonVersion also prints to the dev banner, which is the
		// only difference between the two and is discarded here.
		want := devEnforcedPythonVersion(devTestCmd(), cfg)
		if got := validateEnforcedPythonVersion(cfg); got != want {
			t.Errorf("validate and dev disagree for %+v: validate=%q dev=%q", cfg, got, want)
		}
	}
}
