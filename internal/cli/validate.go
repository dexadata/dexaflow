package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/dbt"
	"github.com/dexadata/dexaflow/internal/domain"
)

// validateOptions holds the resolved flags for a validate run.
type validateOptions struct {
	// parse imports dag.py through the parser, the way compile does (#1680).
	parse     bool
	parserCmd string
}

func newValidateCommand() *cobra.Command {
	var o validateOptions
	cmd := &cobra.Command{
		Use:   "validate [path]",
		Short: "Validate dexaflow.yaml and the DAG source against the schema.",
		Long: `Validate dexaflow.yaml against the schema and check the DAG source.

By default the check is static: dexaflow.yaml is validated and dag.py is
checked for syntax errors, without being imported. A dag.py that is valid
Python but fails when it runs (an AttributeError, a missing import) still
passes, and the error appears at compile time.

Pass --parse to also import dag.py through the parser, as compile does, and
validate the DAG it declares. It needs the parser and the DAG's dependencies
on this host, takes a fraction of a second per DAG, and leaves no dag.json
behind.`,
		Args: cobra.MaximumNArgs(1),
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
			// with dexaflow.yaml open, so it is where a warning about a field
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
			if o.parse {
				if perr := checkDagParses(cmd, dir, cfg, o.parserCmd); perr != nil {
					return perr
				}
			}
			if _, werr := fmt.Fprintf(cmd.OutOrStdout(), "%s is valid\n", projectConfigPath(dir)); werr != nil {
				return werr
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&o.parse, "parse", false, "also import dag.py through the parser, as compile does, and validate the DAG it declares")
	cmd.Flags().StringVar(&o.parserCmd, "parser-cmd", "", "override the parser command used by --parse (default from config)")
	return cmd
}

// checkDagParses runs the DAG through the same parser compile uses, into a
// scratch file, and validates what it produced (#1680).
//
// It is opt-in rather than part of every validate. The static checks run on
// any host, including a CI lint step with no Python dependencies installed;
// importing dag.py does not, so making it the default would turn passing CI
// runs into failures for a reason that has nothing to do with the project.
//
// A dbt project has no dag.py to import: the dbt project is the DAG (ADR 0042)
// and its manifest is already checked for existence above. Running `dbt parse`
// here would need dbt on the host, which is the same trade, so it is not done.
func checkDagParses(cmd *cobra.Command, dir string, cfg *domain.LeoflowConfig, parserCmd string) error {
	if cfg.Dbt != nil {
		_, werr := fmt.Fprintln(cmd.ErrOrStderr(),
			"note: --parse has no dag.py to import in a dbt project; the dbt project is checked by compile")
		return werr
	}
	ensurePysrc(cmd)
	command, err := resolveParserCommand(cmd, parserCmd)
	if err != nil {
		return err
	}
	tmp, err := os.MkdirTemp("", "dexaflow-validate-*")
	if err != nil {
		return fmt.Errorf("creating a scratch directory for --parse: %w", err)
	}
	defer os.RemoveAll(tmp) //nolint:errcheck // best-effort cleanup of a scratch directory
	out := filepath.Join(tmp, "dag.json")
	if rerr := parseForDeclaredPython(cmd, command, parserArgs{
		source:        dagSourcePath(dir, cfg),
		config:        projectConfigPath(dir),
		output:        out,
		image:         validateParseImage,
		dagVersion:    "validate",
		projectConfig: cfg,
		stdout:        io.Discard,
	}); rerr != nil {
		return rerr
	}
	return validateParsedDAG(out, dagSourcePath(dir, cfg), cfg)
}

// validateParsedDAG applies compile's post-parser checks to the scratch
// dag.json: the dexaflow.yaml overlay, the schema and graph, the schedule and
// the operator providers. Errors name the DAG source rather than the scratch
// file, which the user never sees.
//
// An embedded dbt project (a dbt_group placeholder, ADR 0043) is expanded by
// compile from its dbt manifest, which needs dbt on the host. Here each group
// is checked against dexaflow.yaml with compile's own message, then stands in
// as a single task so the rest of the graph is still validated.
func validateParsedDAG(path, source string, cfg *domain.LeoflowConfig) error {
	spec, err := readDAGFile(path)
	if err != nil {
		return err
	}
	tasks, err := dbt.ExpandGroups(spec.Tasks, func(group string) ([]domain.TaskSpec, error) {
		if _, ok := cfg.DbtGroups[group]; !ok {
			return nil, fmt.Errorf("dag uses dbt_group(%q) but dexaflow.yaml has no dbt_groups.%s", group, group)
		}
		return []domain.TaskSpec{{TaskID: "dbt", Type: domain.TaskTypePython, Entrypoint: "dbt:group"}}, nil
	})
	if err != nil {
		return fmt.Errorf("%s: %w", source, err)
	}
	spec.Tasks = tasks
	if werr := writeDAGFile(path, spec); werr != nil {
		return werr
	}
	if oerr := overlayProject(path, cfg); oerr != nil {
		return oerr
	}
	if spec, err = readDAGFile(path); err != nil {
		return err
	}
	if verr := spec.Validate(); verr != nil {
		return fmt.Errorf("%s declares an invalid DAG: %w", source, verr)
	}
	if verr := spec.ValidateSchedule(); verr != nil {
		return fmt.Errorf("%s declares an invalid DAG: %w", source, verr)
	}
	return validateOperatorProviders(spec, cfg)
}

// readDAGFile decodes a dag.json the parser wrote.
func readDAGFile(path string) (*domain.DAGSpec, error) {
	data, err := os.ReadFile(path) //nolint:gosec // G304: a scratch file this command created
	if err != nil {
		return nil, fmt.Errorf("reading parser output: %w", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		return nil, fmt.Errorf("parsing parser output: %w", uerr)
	}
	return &spec, nil
}

// validateParseImage is the image reference handed to the parser by validate
// --parse. The parser requires one, and the value never leaves the scratch file.
const validateParseImage = "validate.invalid/dexaflow-validate:parse"

// validateEnforcedPythonVersion returns the interpreter minor `validate` must
// lint under, or empty when any supported one will do.
//
// Same rule and same three exemptions as `dexaflow lite` (#1092): a version the
// author did not write is not a statement, `base_image` makes the field inert
// because the FROM is chosen by hand, and a deprecated version is one we are
// asking them to leave rather than one we should demand an interpreter for.
//
// It is a separate function from devEnforcedPythonVersion only because that one
// prints to the dev banner; the decision is deliberately identical, and a test
// runs both over the same configs so the two cannot drift silently.
func validateEnforcedPythonVersion(cfg *domain.LeoflowConfig) string {
	if cfg == nil || cfg.PythonVersionDefaulted || cfg.BaseImage != "" {
		return ""
	}
	if _, deprecated := domain.DeprecatedPythonVersion(cfg.PythonVersion); deprecated {
		return ""
	}
	return cfg.PythonVersion
}

// checkDagPythonSyntax runs `python -m py_compile` on the DAG source to catch
// syntax errors before push (issue #D8: validate used to lie about a broken
// dag.py). The check is best-effort: when no Python interpreter is reachable
// (managed or system), we warn instead of failing, because a fresh install that
// has not yet run `dexaflow setup` should still be able to lint its dexaflow.yaml.
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
	// Unified precedence with `dexaflow lite` (#742): managed pinned build, then a
	// host python3.11/python3 that reports >= 3.11. A present-but-unsupported
	// interpreter is a hard error (validate must not lint under 3.9 a DAG that
	// will run under 3.11), while no interpreter at all is a soft skip so a fresh
	// install that has not run `dexaflow setup` can still lint its dexaflow.yaml.
	py, err := resolvePython3(cmd.Context(), leoflowManagedPython(), exec.LookPath, pythonVersion)
	if err != nil {
		return err
	}
	if py == "" {
		if _, werr := fmt.Fprintln(cmd.ErrOrStderr(),
			"warning: skipping dag.py syntax check (no python3 found; run `dexaflow setup` to provision one)"); werr != nil {
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

// syntaxCheckIsTrustworthy says whether a syntax verdict from an interpreter
// reporting minor `have` can be trusted for a project declaring minor `want`.
//
// Python's grammar grows, so the two directions are not equally wrong. An
// OLDER interpreter rejects syntax the declared one accepts (`type Alias[T]`
// is valid from 3.12 and a SyntaxError on 3.11), which is exactly #1094: a
// rejection of code the cluster runs correctly, phrased as the author's
// mistake. A NEWER one accepts everything the declared minor accepts, so a
// SyntaxError from it is the author's and worth failing on.
func syntaxCheckIsTrustworthy(want, have int) bool { return have >= want }

// warnSyntaxCheckSkipped explains a skip in terms of what the reader can fix.
func warnSyntaxCheckSkipped(cmd *cobra.Command, wantVersion string, want int, found string) error {
	// %d, not the full version: the package a reader installs is
	// `python3.13`, and naming `python3.3.13` sends them after something
	// that was never published.
	_, werr := fmt.Fprintf(cmd.ErrOrStderr(),
		"warning: skipping dag.py syntax check: this project declares python_version %s and no python3.%d is reachable here%s "+
			"(run `dexaflow setup`, or install python3.%d). Checking under an older interpreter would report %s syntax "+
			"as an error in your code.\n",
		wantVersion, want, found, want, wantVersion)
	return werr
}

// checkDagSyntaxUnder lints the DAG under the interpreter the project declares,
// and falls back deliberately when that exact minor is absent.
//
// The fallback is asymmetric, per syntaxCheckIsTrustworthy, and the reason it
// exists at all is that skipping outright would have been a bigger bug than
// #1094. `dexaflow init` writes python_version explicitly, so EVERY scaffolded
// project takes this path, and most hosts carry a python3 newer than the 3.11
// it writes. Skipping whenever the exact minor is missing would have stopped
// validate catching a broken dag.py for most users, which is the entire reason
// the check exists. CI is one such host, and it caught this.
//
// A too-old interpreter is a skip with a warning rather than the hard error
// resolvePython3 would return, because with a declared version the project is
// not asking to run on that interpreter: failing here would reject a
// well-formed dexaflow.yaml over a Python the author never claimed to use.
func checkDagSyntaxUnder(cmd *cobra.Command, dagPath, wantVersion string) error {
	want, verr := parsePythonMinor(wantVersion)
	if verr != nil {
		return verr
	}
	ctx := cmd.Context()
	if py, rerr := resolvePythonFor(ctx, want, leoflowManagedPython(), exec.LookPath, pythonVersion); rerr == nil && py != "" {
		return runPyCompile(cmd, py, dagPath)
	}
	py, rerr := resolvePython3(ctx, leoflowManagedPython(), exec.LookPath, pythonVersion)
	if rerr != nil || py == "" {
		return warnSyntaxCheckSkipped(cmd, wantVersion, want, "")
	}
	_, have, herr := pythonVersion(ctx, py)
	if herr != nil {
		// An interpreter that will not report its version cannot be weighed
		// against the declared one, so it is not a second opinion either.
		return warnSyntaxCheckSkipped(cmd, wantVersion, want, "")
	}
	if !syntaxCheckIsTrustworthy(want, have) {
		return warnSyntaxCheckSkipped(cmd, wantVersion, want, fmt.Sprintf(", only python3.%d", have))
	}
	return runPyCompile(cmd, py, dagPath)
}
