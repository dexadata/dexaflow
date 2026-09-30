package cli

import (
	"fmt"
	"strings"
)

// parserCommandFor points the parser at the interpreter the project declares,
// and returns a warning instead when it cannot.
//
// The parser EXECUTES the DAG: `compiler.py` calls `runpy.run_path(source)`. So
// it compiles the author's syntax with its own interpreter's grammar, while
// `parser_cmd` is baked once at `leoflow setup` from whatever interpreter was
// resolved then and never re-evaluated per project. A project declaring 3.13
// was therefore parsed by a 3.11 parser, and 3.12+ syntax came back as a
// SyntaxError in the user's code, for code the cluster runs correctly (#1095).
//
// The substitution is deliberately timid. `parser_cmd` is operator-configurable
// free-form text, and the shape setup writes is
// `env PYTHONPATH=<dir> <interpreter> -m leoflow_parser`: the interpreter is the
// token before `-m`. When that shape is absent the command is left exactly as it
// is and the risk is named, because rewriting a command we do not understand
// breaks a working setup, which is worse than the skew it would fix.
func parserCommandFor(command, wantVersion string, resolve func(minor int) (string, error)) (cmd, warning string) {
	if wantVersion == "" {
		return command, ""
	}
	want, verr := parsePythonMinor(wantVersion)
	if verr != nil {
		return command, ""
	}

	fields := strings.Fields(command)
	idx := -1
	for i, f := range fields {
		if f == "-m" && i > 0 {
			idx = i - 1
			break
		}
	}
	if idx < 0 {
		return command, fmt.Sprintf(
			"this project declares python_version %s, but the configured parser command names no interpreter this tool can replace (%q), "+
				"so the DAG is parsed by whatever that command runs. A syntax error reported below may be the parser's Python, not yours.",
			wantVersion, command)
	}

	py, rerr := resolve(want)
	if rerr != nil || py == "" {
		return command, fmt.Sprintf(
			"this project declares python_version %s and no python3.%d is installed here, so the DAG is parsed by the interpreter `leoflow setup` picked. "+
				"A syntax error reported below may be that interpreter rejecting %s syntax, not a mistake in your code.",
			wantVersion, want, wantVersion)
	}

	out := make([]string, len(fields))
	copy(out, fields)
	out[idx] = py
	return strings.Join(out, " "), ""
}
