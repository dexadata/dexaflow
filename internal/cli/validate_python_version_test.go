package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
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
	// connect the warning to the line in their leoflow.yaml.
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
