package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

func renderFor(t *testing.T, mutate func(*domain.LeoflowConfig)) string {
	t.Helper()
	cfg := &domain.LeoflowConfig{DagID: "d"}
	mutate(cfg)
	cfg.ApplyDefaults()
	out, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("generatedDockerfile: %v", err)
	}
	return out
}

func runLineWith(t *testing.T, dockerfile, needle string) string {
	t.Helper()
	for _, line := range strings.Split(dockerfile, "\n") {
		if strings.HasPrefix(line, "RUN ") && strings.Contains(line, needle) {
			return line
		}
	}
	t.Fatalf("no RUN line containing %q in:\n%s", needle, dockerfile)
	return ""
}

// TestVersionFloorSurvivesIntoTheRunLine is the #1064 regression.
//
// `RUN` in shell form is /bin/sh -c, so an unquoted `>=` is a REDIRECTION. The
// spec was being joined into the line raw, which meant `setuptools>=80.9.0`
// reached pip as a bare `setuptools` plus a redirect into a file named
// `=80.9.0`. Measured on a real build with the base seeded at
// setuptools==79.0.1: the build exited 0, the installed version stayed 79.0.1,
// and two junk files landed in the workdir carrying pip's stdout.
//
// A version floor is how you remediate a CVE in a transitive dependency, so
// this failed silently in exactly the case someone was relying on it.
func TestVersionFloorSurvivesIntoTheRunLine(t *testing.T) {
	df := renderFor(t, func(c *domain.LeoflowConfig) {
		c.Dependencies = []string{"setuptools>=80.9.0", "msgpack>=1.2.1"}
	})
	line := runLineWith(t, df, "pip install")

	for _, spec := range []string{"setuptools>=80.9.0", "msgpack>=1.2.1"} {
		if !strings.Contains(line, "'"+spec+"'") {
			t.Errorf("the spec %q is not single-quoted, so the shell eats the operator:\n%s", spec, line)
		}
	}
	// The shape of the bug: an operator sitting outside quotes.
	if strings.Contains(line, " setuptools>=") || strings.Contains(line, " msgpack>=") {
		t.Errorf("a redirection survives in the RUN line:\n%s", line)
	}
}

// TestEnvironmentMarkersSurvive. `;` is NOT a metacharacter to reject here — it
// is PEP 508's environment-marker separator, and `requests; python_version <
// "3.9"` is a legal specifier whose marker also contains `<` and double
// quotes. Refusing it (the obvious "block shell metacharacters" reflex) would
// break a valid form; single-quoting carries it through intact, which a real
// build confirms.
func TestEnvironmentMarkersSurvive(t *testing.T) {
	const spec = `requests; python_version < "3.99"`
	df := renderFor(t, func(c *domain.LeoflowConfig) { c.Dependencies = []string{spec} })
	line := runLineWith(t, df, "pip install")
	if !strings.Contains(line, "'"+spec+"'") {
		t.Errorf("a PEP 508 environment marker did not survive quoting:\n%s", line)
	}
}

// TestNothingEscapesTheQuotes. A value carrying its own single quote must not
// be able to close ours and start a new word — the classic quoting escape.
func TestNothingEscapesTheQuotes(t *testing.T) {
	df := renderFor(t, func(c *domain.LeoflowConfig) {
		c.Dependencies = []string{`ev'il`}
	})
	line := runLineWith(t, df, "pip install")
	if !strings.Contains(line, `'ev'\''il'`) {
		t.Errorf("an embedded single quote was not escaped as '\\'':\n%s", line)
	}
}

// TestSystemPackagesAreQuotedToo. The apt line uses the identical raw join.
// An apt name rarely carries a metacharacter, so the practical bug is smaller
// — but the line has the same shape and the field is equally unconstrained.
func TestSystemPackagesAreQuotedToo(t *testing.T) {
	df := renderFor(t, func(c *domain.LeoflowConfig) {
		c.SystemPackages = []string{"curl", "libpq-dev"}
	})
	line := runLineWith(t, df, "apt-get install")
	for _, pkg := range []string{"curl", "libpq-dev"} {
		if !strings.Contains(line, "'"+pkg+"'") {
			t.Errorf("apt package %q is not quoted:\n%s", pkg, line)
		}
	}
	// The shell parts of that line must stay shell.
	if !strings.Contains(line, "&& rm -rf") {
		t.Errorf("quoting swallowed the apt cache cleanup:\n%s", line)
	}
}

// TestNewlineInASpecIsRefused. Quoting makes every other character harmless,
// but a newline ends the RUN instruction itself whatever the quoting, so the
// only safe answer is to refuse it — and to say which entry, since a stray
// newline in YAML is invisible.
func TestNewlineInASpecIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, bad string }{
		{"dependency", "requests\nRUN echo surprise"},
		{"carriage return", "requests\rmore"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &domain.LeoflowConfig{DagID: "d"}
			cfg.Dependencies = []string{tc.bad}
			cfg.ApplyDefaults()
			_, err := generatedDockerfile(cfg, "dag.py")
			if err == nil {
				t.Fatal("a newline in a spec was accepted; it terminates the RUN instruction")
			}
			if !strings.Contains(err.Error(), "dependencies") {
				t.Errorf("the error does not name the field: %v", err)
			}
		})
	}
}

// runLineUnderSh executes the RUN line's command body under a real /bin/sh
// with `pip` and `apt-get` replaced by a recorder, and returns the arguments
// each actually received — one per line.
//
// This is the tier the render assertions above cannot reach. They check that
// the spec appears quoted; they cannot check that it is quoted the way SH
// reads, and the difference is where the plausible wrong fixes live. `%q` — the
// reflex fix — emits `"setuptools>=80.9.0"`, which satisfies a "contains the
// spec, in quotes" assertion and is still wrong: inside double quotes the
// shell keeps expanding, so a spec containing `$HOME` becomes a path. Only a
// shell can tell the two apart, and running one needs no Docker, no network
// and no registry.
func runLineUnderSh(t *testing.T, dockerfile, tool string) []string {
	t.Helper()
	line := runLineWith(t, dockerfile, tool+" ")
	body := strings.TrimPrefix(line, "RUN ")

	bin := t.TempDir()
	out := filepath.Join(t.TempDir(), "argv.txt")
	// The recorder writes one argument per line, so an argument containing a
	// space is still distinguishable from two arguments.
	rec := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\" >> " + out + "; done\nexit 0\n"
	for _, name := range []string{"pip", "apt-get", "rm"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(rec), 0o700); err != nil { //nolint:gosec // test fixture must be executable
			t.Fatal(err)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "/bin/sh", "-c", body)
	cmd.Dir = t.TempDir() // so a stray redirection lands here, where we can see it
	cmd.Env = append(os.Environ(), "PATH="+bin, "HOME=/should-not-appear")
	if err := cmd.Run(); err != nil {
		t.Fatalf("running %q under sh: %v", body, err)
	}

	// A redirection would have created a file named after its operand.
	entries, err := os.ReadDir(cmd.Dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		t.Errorf("the shell created %q — an operator escaped the quoting and became a redirection", e.Name())
	}

	recorded, err := os.ReadFile(out)
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(recorded), "\n"), "\n")
}

// TestShellReceivesTheSpecsIntact is the regression that a render assertion
// cannot be. It runs the generated line through a real /bin/sh and checks what
// pip was actually handed.
//
// Against the original raw join this fails three ways at once: pip receives a
// bare `setuptools`, the floor is gone, and a file named `=80.9.0` appears.
// Against a `%q` "fix" it fails on the expansion case alone, which is the
// whole reason this tier exists.
func TestShellReceivesTheSpecsIntact(t *testing.T) {
	specs := []string{
		"setuptools>=80.9.0",
		`requests; python_version < "3.99"`,
		"pkg$HOME",       // must not expand
		"pkg`id`",        // must not run a subshell
		"pkg$(id)",       // nor this spelling
		"pkg&&touch bad", // must not chain a command
	}
	df := renderFor(t, func(c *domain.LeoflowConfig) { c.Dependencies = specs })
	got := runLineUnderSh(t, df, "pip")

	// pip is called as `pip install --no-cache-dir -- <specs...>`. Sliced after
	// the separator rather than at a fixed index, so adding or removing a flag
	// does not silently shift what this asserts.
	sep := -1
	for i, a := range got {
		if a == "--" {
			sep = i
			break
		}
	}
	if sep < 0 {
		t.Fatalf("pip received no `--` separator: %q", got)
	}
	args := got[sep+1:]
	if len(args) != len(specs) {
		t.Fatalf("pip received %d arguments, want %d — the shell split or ate something:\n%q", len(args), len(specs), got)
	}
	for i, want := range specs {
		if args[i] != want {
			t.Errorf("argument %d reached pip as %q, want %q", i, args[i], want)
		}
	}
}

// TestAptLineStillWorksAsAShellCommand. Quoting the package names must not
// quote the parts that are meant to be shell: the `&&` chaining and the cache
// cleanup are load-bearing, since they keep the install and the cleanup in one
// layer.
func TestAptLineStillWorksAsAShellCommand(t *testing.T) {
	df := renderFor(t, func(c *domain.LeoflowConfig) {
		c.SystemPackages = []string{"curl", "libpq-dev"}
	})
	got := runLineUnderSh(t, df, "apt-get")
	joined := strings.Join(got, " ")
	for _, want := range []string{"curl", "libpq-dev", "update", "install"} {
		if !strings.Contains(joined, want) {
			t.Errorf("apt-get never received %q; it got %q", want, got)
		}
	}
	// `rm` is on the recorder PATH too, so the `&&` chain having run at all is
	// visible: if quoting had swallowed the chaining, rm would never be called.
	//
	// Asserted on the DIRECTORY, not on the literal `/var/lib/apt/lists/*`. The
	// glob is unquoted on purpose — it is meant to be shell — so whether it
	// expands depends on the machine: on a runner with apt installed it becomes
	// eighty real paths, on a Mac without /var/lib/apt/lists it stays literal.
	// The first version of this assertion looked for the literal and passed
	// locally while failing in CI, which is a test measuring the filesystem
	// rather than the behavior.
	if !strings.Contains(joined, "-rf") || !strings.Contains(joined, "/var/lib/apt/lists") {
		t.Errorf("the cache cleanup did not run, so the && chain was broken: %q", got)
	}
}

// TestOptionInjectionIsStoppedByTheSeparator. Quoting guarantees "one argv
// element"; it does not guarantee "a package". A leading dash is still an
// OPTION to the tool being run, and both tools take dangerous ones:
//
//   - `dependencies: ["--dry-run", "six"]` built green with six absent —
//     confirmed on a real build. That is the identical silent-failure shape as
//     the bug this whole change exists to fix.
//   - `system_packages: ["-o", "DPkg::Pre-Invoke::=<cmd>", "hello"]` runs <cmd>
//     as root during the build.
//
// `--` ends option parsing in both, so either becomes a loud refusal instead.
func TestOptionInjectionIsStoppedByTheSeparator(t *testing.T) {
	for _, tc := range []struct {
		name, tool string
		mutate     func(*domain.LeoflowConfig)
	}{
		{"pip", "pip", func(c *domain.LeoflowConfig) { c.Dependencies = []string{"--dry-run", "six"} }},
		{"apt-get", "apt-get", func(c *domain.LeoflowConfig) { c.SystemPackages = []string{"-o", "DPkg::Pre-Invoke::=id", "hello"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			df := renderFor(t, tc.mutate)
			line := runLineWith(t, df, tc.tool+" ")
			if !strings.Contains(line, " -- ") {
				t.Fatalf("no `--` separator, so a leading-dash entry is still an option:\n%s", line)
			}
			// Everything the author supplied must come after it.
			sep := strings.Index(line, " -- ")
			for _, entry := range []string{"--dry-run", "-o", "DPkg::Pre-Invoke::=id"} {
				if i := strings.Index(line, "'"+entry+"'"); i >= 0 && i < sep {
					t.Errorf("%q appears before the `--`, so it is still parsed as an option:\n%s", entry, line)
				}
			}
		})
	}
}

// TestNewlineIsRefusedInSystemPackagesToo. The newline guard is the one thing
// quoting cannot substitute for, and there was no test that it applied to
// system_packages — so restricting it to `dependencies` was a wrong fix the
// suite accepted.
func TestNewlineIsRefusedInSystemPackagesToo(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.SystemPackages = []string{"curl\nRUN echo surprise"}
	cfg.ApplyDefaults()
	_, err := generatedDockerfile(cfg, "dag.py")
	if err == nil {
		t.Fatal("a newline in system_packages was accepted; it terminates the RUN instruction")
	}
	if !strings.Contains(err.Error(), "system_packages") {
		t.Errorf("the error does not name the field: %v", err)
	}
}

// TestAptChainSurvivesQuotingWithTheOriginalBug guards the guard: the apt case
// above asserts the `&&` chain still runs, and that assertion passes with the
// ORIGINAL raw join in place — it is a "did I break the shell parts" check, not
// a regression test for the quoting. This one is the regression test.
func TestAptPackagesAreQuotedNotJoinedRaw(t *testing.T) {
	df := renderFor(t, func(c *domain.LeoflowConfig) {
		c.SystemPackages = []string{"pkg;touch bad"}
	})
	got := runLineUnderSh(t, df, "apt-get")
	joined := strings.Join(got, "\x00")
	if !strings.Contains(joined, "pkg;touch bad") {
		t.Errorf("the entry did not reach apt-get as one argument: %q", got)
	}
}
