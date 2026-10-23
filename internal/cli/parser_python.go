package cli

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// The parser runs dag.py (runpy.run_path), so it judges the author's code with
// its own interpreter's grammar. parser_cmd is written once by `dexaflow setup`
// from whatever interpreter setup found, while python_version says which minor
// the DAG runs on. A parser older than the declared minor reports newer syntax
// (`type X[T]` before 3.12) as a SyntaxError in code the cluster runs fine
// (#1095). parseForDeclaredPython runs the parse on the declared minor instead,
// under the constraints the first attempt (#1266) taught:
//
//   - Only a parser_cmd of the shape setup writes is retargeted (see
//     parserInterpreter). Swapping "the token before -m" in any other shape
//     turned `python3 -u -m leoflow_parser` into a command that ran the
//     interpreter as a script.
//   - Switching interpreters switches which imports resolve, and the parser
//     reports a missing module as "not supported by Dexaflow". When the
//     retargeted parse fails on a missing module, the configured parser runs
//     once more: if it succeeds, the module is installed only for it, and the
//     parse it produced stands, with a warning naming both interpreters.
//   - A parser at least as new as the declared minor accepts all of its
//     syntax, so it is left alone.

// parserInterpreterRe matches an interpreter's base name: python, python3,
// python3.N, with an optional .exe.
var parserInterpreterRe = regexp.MustCompile(`^python(3(\.\d+)?)?(\.exe)?$`)

// envAssignRe matches a VAR=value word of an `env` prefix.
var envAssignRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*=`)

// parserMissingModuleRe matches the parser's report of a module dag.py imports
// that the parser's interpreter cannot import (compiler.py, _unsupported).
var parserMissingModuleRe = regexp.MustCompile(`module '[^']+': not supported by Dexaflow`)

// parserInterpreter returns the index of the interpreter word in a parser_cmd
// of the shape `dexaflow setup` writes: an optional `env` followed only by
// VAR=value words, then the interpreter, then `-m leoflow_parser`. Any other
// shape returns false and is run exactly as configured.
func parserInterpreter(fields []string) (int, bool) {
	i := 0
	if len(fields) > 0 && fields[0] == "env" {
		i = 1
		for i < len(fields) && envAssignRe.MatchString(fields[i]) {
			i++
		}
	}
	if i+2 >= len(fields) {
		return 0, false
	}
	if !parserInterpreterRe.MatchString(filepath.Base(fields[i])) {
		return 0, false
	}
	if fields[i+1] != "-m" || fields[i+2] != "leoflow_parser" {
		return 0, false
	}
	return i, true
}

// declaredParser is the decision of parseForDeclaredPython, with the host
// lookups injected for tests.
type declaredParser struct {
	managed    string
	lookPath   func(string) (string, error)
	runVersion func(context.Context, string) (int, int, error)
}

// hostDeclaredParser looks up interpreters on this host.
func hostDeclaredParser() declaredParser {
	return declaredParser{managed: leoflowManagedPython(), lookPath: exec.LookPath, runVersion: pythonVersion}
}

// pick returns the parser command to try first for a project, and the
// configured parser's minor. It returns "" when the configured command is the
// one to run: no declared python_version, a parser_cmd of another shape, a
// parser at least as new as the declared minor, or no interpreter of that
// minor on this host. A warning for the last two of those goes to w.
func (d declaredParser) pick(ctx context.Context, w io.Writer, command string, cfg *domain.LeoflowConfig) (alt string, have int) {
	if cfg == nil || cfg.PythonVersion == "" || cfg.PythonVersionDefaulted {
		return "", 0
	}
	want, err := parsePythonMinor(cfg.PythonVersion)
	if err != nil {
		return "", 0
	}
	fields := strings.Fields(command)
	i, ok := parserInterpreter(fields)
	if !ok {
		return "", 0
	}
	current := fields[i]
	if !filepath.IsAbs(current) {
		if current, err = d.lookPath(current); err != nil {
			return "", 0
		}
	}
	major, have, err := d.runVersion(ctx, current)
	if err != nil || major != 3 || have >= want {
		return "", 0
	}
	py, err := resolvePythonFor(ctx, want, d.managed, d.lookPath, d.runVersion)
	if err != nil || py == "" || strings.ContainsAny(py, " \t") {
		//nolint:errcheck // a warning that cannot be written changes nothing
		fmt.Fprintf(w, "warning: the parser runs on python3.%d and this project declares python_version %s, "+
			"and no python3.%d was found here: syntax newer than 3.%d in dag.py is reported as an error. "+
			"Install python3.%d to parse with it.\n", have, cfg.PythonVersion, want, have, want)
		return "", have
	}
	fields[i] = py
	return strings.Join(fields, " "), have
}

// parseForDeclaredPython runs the parser for a project on the interpreter of
// its declared python_version when the configured parser is older, and
// otherwise exactly as configured. See the comment at the top of this file.
func parseForDeclaredPython(cmd *cobra.Command, command string, a parserArgs) error {
	return parseForDeclaredPythonWith(cmd, hostDeclaredParser(), command, a)
}

func parseForDeclaredPythonWith(cmd *cobra.Command, d declaredParser, command string, a parserArgs) error {
	errOut := cmd.ErrOrStderr()
	if a.stderr != nil {
		errOut = a.stderr
	}
	alt, have := d.pick(cmdContext(cmd), errOut, command, a.projectConfig)
	if alt == "" {
		return runParser(cmd, command, a)
	}
	var held bytes.Buffer
	first := a
	first.stderr = &held
	err := runParser(cmd, alt, first)
	if err == nil {
		//nolint:errcheck // streaming the parser's own output, as runParser does
		io.Copy(errOut, &held)
		return nil
	}
	if !parserMissingModuleRe.MatchString(err.Error()) {
		//nolint:errcheck // streaming the parser's own output, as runParser does
		io.Copy(errOut, &held)
		return err
	}
	if rerr := runParser(cmd, command, a); rerr != nil {
		return err
	}
	//nolint:errcheck // a warning that cannot be written changes nothing
	fmt.Fprintf(errOut, "warning: %s\n  so dag.py was parsed by the configured parser (python3.%d) instead of python_version %s: "+
		"the task image must provide that module, and syntax newer than 3.%d is reported as an error.\n",
		firstLine(err.Error()), have, a.projectConfig.PythonVersion, have)
	return nil
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}
