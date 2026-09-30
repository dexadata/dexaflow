package cli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/neochaotic/leoflow/internal/domain"
)

func newValidateCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate leoflow.yaml and the DAG source against the schema.",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "."
			if len(args) == 1 {
				dir = args[0]
			}
			cfg, err := loadProjectConfig(dir)
			if err != nil {
				return err
			}
			// Shared with `compile`/`deploy` rather than open-coded, which is
			// what this was: schema validation, the dbt-block-with-dag.py
			// conflict (#1015), and the deprecated-Python warning. Open-coding
			// the first two meant `validate` silently skipped the third — and
			// `validate` is the sub-second command an author runs in a loop
			// with leoflow.yaml open, so it is where a warning about a field
			// in that file has the best chance of being acted on.
			//
			// A pure-dbt project has no dag.py: the dbt project IS the DAG
			// (ADR 0042). Stat'ing the source unconditionally made the whole
			// mode unvalidatable — the command that exists to say "this is
			// fine" always said it was not (#996). The check is scoped, not
			// removed: a dag.py DAG with a missing source still fails below.
			if perr := checkProjectPreconditions(cmd, dir, cfg); perr != nil {
				return perr
			}
			if cfg.Dbt != nil {
				// Scoping the dag.py check to a dag.py DAG left the dbt lane with
				// NOTHING checked, so `validate` answered "is valid" for a dbt:
				// block pointing at a directory that does not exist — the exact
				// shape of #15, reintroduced by #15's own fix. The project's
				// dbt_project.yml is the dbt equivalent of the DAG source.
				proj := filepath.Join(dir, cfg.Dbt.Project)
				if _, serr := os.Stat(filepath.Join(proj, "dbt_project.yml")); serr != nil {
					return fmt.Errorf("dbt project not found: no dbt_project.yml under %s (dbt.project = %q): %w", proj, cfg.Dbt.Project, serr)
				}
			}
			if cfg.Dbt == nil {
				dagSrc := dagSourcePath(dir, cfg)
				if _, serr := os.Stat(dagSrc); serr != nil {
					return fmt.Errorf("DAG source not found: %w", serr)
				}
				if perr := checkDagPythonSyntax(cmd, dagSrc, cfg); perr != nil {
					return perr
				}
			}
			if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "%s is valid\n", projectConfigPath(dir)); werr != nil {
				return werr
			}
			return nil
		},
	}
}

// checkDagPythonSyntax runs `python -m py_compile` on the DAG source to catch
// syntax errors before push (issue #D8 — validate used to lie about a broken
// dag.py). The check is best-effort: when no Python interpreter is reachable
// (managed or system), we warn instead of failing — a fresh install that has
// not yet run `leoflow setup` should still be able to lint its leoflow.yaml.
// validateEnforcedPythonVersion returns the interpreter minor `validate` must
// lint under, or empty when any supported one will do.
//
// Same rule and same three exemptions as `leoflow dev` (#1092): a version the
// author did not write is not a statement, `base_image` makes the field inert
// because the FROM is chosen by hand, and a deprecated version is one we are
// asking them to leave rather than one we should demand an interpreter for.
//
// It is a separate function from devEnforcedPythonVersion only because that one
// prints to the dev banner; the decision is deliberately identical, and a test
// covers each exemption so the two cannot drift silently.
func validateEnforcedPythonVersion(cfg *domain.LeoflowConfig) string {
	if cfg == nil || cfg.PythonVersionDefaulted || cfg.BaseImage != "" {
		return ""
	}
	if _, deprecated := domain.DeprecatedPythonVersion(cfg.PythonVersion); deprecated {
		return ""
	}
	return cfg.PythonVersion
}

func checkDagPythonSyntax(cmd *cobra.Command, dagPath string, cfg *domain.LeoflowConfig) error {
	// A declared python_version is the author's statement about the interpreter
	// their DAG runs on, and the cluster honors it through the task base image.
	// Linting under a different minor produces the WRONG answer in the more
	// annoying direction: `type X[T]` is a SyntaxError on 3.11, so validate
	// rejected a DAG that compiles and runs correctly, with a message that reads
	// as a complaint about the author's code (#1094).
	if want := validateEnforcedPythonVersion(cfg); want != "" {
		return checkDagSyntaxUnder(cmd, dagPath, want)
	}
	// Unified precedence with `leoflow dev` (#742): managed pinned build, then a
	// host python3.11/python3 that reports >= 3.11. A present-but-unsupported
	// interpreter is a hard error (validate must not lint under 3.9 a DAG that
	// will run under 3.11), while no interpreter at all is a soft skip so a fresh
	// install that has not run `leoflow setup` can still lint its leoflow.yaml.
	py, err := resolvePython3(cmd.Context(), leoflowManagedPython(), exec.LookPath, pythonVersion)
	if err != nil {
		return err
	}
	if py == "" {
		if _, werr := fmt.Fprintln(cmd.ErrOrStderr(),
			"warning: skipping dag.py syntax check (no python3 found; run `leoflow setup` to provision one)"); werr != nil {
			return werr
		}
		return nil
	}
	return runPyCompile(cmd, py, dagPath)
}

// runPyCompile reports a syntax error from `python -m py_compile`, with the
// interpreter's own message rather than a summary of it.
func runPyCompile(cmd *cobra.Command, py, dagPath string) error {
	out, err := exec.CommandContext(cmd.Context(), py, "-m", "py_compile", dagPath).CombinedOutput() //nolint:gosec // py + dagPath are both validated inputs from this CLI's own setup/user-arg path
	if err == nil {
		return nil
	}
	msg := strings.TrimSpace(string(out))
	if msg == "" {
		msg = err.Error()
	}
	return fmt.Errorf("dag.py has a syntax error: %s", msg)
}

// checkDagSyntaxUnder lints the DAG under one specific interpreter minor.
//
// When that minor is not installed it SKIPS with a warning rather than linting
// under another one or refusing outright. Linting under another minor is the
// defect this exists to fix, and refusing would block someone from validating
// their leoflow.yaml over an interpreter they do not need locally: the cluster
// runs the DAG on the base image, not on their laptop. A check that cannot be
// trusted is worth less than no check, and saying so is worth more than both.
func checkDagSyntaxUnder(cmd *cobra.Command, dagPath, wantVersion string) error {
	want, verr := parsePythonMinor(wantVersion)
	if verr != nil {
		return verr
	}
	py, rerr := resolvePythonFor(cmd.Context(), want, leoflowManagedPython(), exec.LookPath, pythonVersion)
	if rerr != nil || py == "" {
		_, werr := fmt.Fprintf(cmd.ErrOrStderr(),
			"warning: skipping dag.py syntax check: this project declares python_version %s and no python3.%s is reachable here "+
				"(run `leoflow setup`, or install python3.%s). Checking under a different interpreter would report %s syntax "+
				"as an error in your code.\n",
			wantVersion, wantVersion, wantVersion, wantVersion)
		return werr
	}
	return runPyCompile(cmd, py, dagPath)
}
