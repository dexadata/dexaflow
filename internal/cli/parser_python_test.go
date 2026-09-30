package cli

import (
	"fmt"
	"strings"
	"testing"
)

// The parser EXECUTES the user's dag.py: compiler.py calls
// `runpy.run_path(source)`. So it compiles their syntax with whatever
// interpreter it runs on, and `parser_cmd` is baked at setup time from whatever
// `leoflow setup` resolved, never re-evaluated per project (#1095).
//
// A project declaring 3.13 therefore had its DAG parsed by a 3.11 parser, and
// 3.12+ syntax came back as a SyntaxError in the user's code. Verified with
// ast.parse: `type Alias[T] = list[T]` is "Type parameter lists are only
// supported in Python 3.12 and greater" under the 3.11 grammar and fine after.
func TestParserCommandUsesTheDeclaredInterpreter(t *testing.T) {
	const baked = "env PYTHONPATH=/home/u/.leoflow/pysrc/parser /usr/bin/python3.11 -m leoflow_parser"
	resolve := func(minor int) (string, error) {
		return fmt.Sprintf("/opt/py/python3.%d", minor), nil
	}

	got, warn := parserCommandFor(baked, "3.13", resolve)
	if warn != "" {
		t.Errorf("unexpected warning: %s", warn)
	}
	if !strings.Contains(got, "/opt/py/python3.13 -m leoflow_parser") {
		t.Errorf("the declared interpreter was not substituted:\n  %s", got)
	}
	if strings.Contains(got, "python3.11") {
		t.Errorf("the baked interpreter survived; the DAG would still be parsed under it:\n  %s", got)
	}
	if !strings.Contains(got, "PYTHONPATH=/home/u/.leoflow/pysrc/parser") {
		t.Errorf("the parser's own PYTHONPATH was lost, so it would not import:\n  %s", got)
	}
}

// No declared version means the author said nothing; the baked command stands.
func TestParserCommandUntouchedWithoutADeclaredVersion(t *testing.T) {
	const baked = "env PYTHONPATH=/p /usr/bin/python3.11 -m leoflow_parser"
	got, warn := parserCommandFor(baked, "", nil)
	if got != baked || warn != "" {
		t.Errorf("command changed with no declared version: %q %q", got, warn)
	}
}

// An operator can put anything in parser_cmd. When the interpreter cannot be
// identified, the command is left ALONE and the risk is named: silently
// rewriting a command we do not understand is worse than parsing under the
// wrong minor, because it breaks a working setup.
func TestParserCommandWarnsRatherThanGuessing(t *testing.T) {
	for _, baked := range []string{
		"/opt/wrapper/run-parser.sh",
		"env PYTHONPATH=/p python-wrapper leoflow_parser",
	} {
		got, warn := parserCommandFor(baked, "3.13", func(int) (string, error) { return "/opt/py/python3.13", nil })
		if got != baked {
			t.Errorf("a command with no identifiable interpreter was rewritten: %q -> %q", baked, got)
		}
		if warn == "" {
			t.Errorf("no warning for %q; a SyntaxError from the parser would be blamed on the user's code", baked)
		}
	}
}

// The declared minor may not be installed. Leave the command alone and say what
// that means, rather than failing a compile over an interpreter the cluster
// does not need the author to have.
func TestParserCommandWarnsWhenTheInterpreterIsMissing(t *testing.T) {
	const baked = "env PYTHONPATH=/p /usr/bin/python3.11 -m leoflow_parser"
	got, warn := parserCommandFor(baked, "3.13", func(int) (string, error) { return "", fmt.Errorf("not found") })
	if got != baked {
		t.Errorf("command changed despite no interpreter: %q", got)
	}
	if !strings.Contains(warn, "3.13") {
		t.Errorf("the warning must name the declared version so a syntax error can be attributed: %q", warn)
	}
}
