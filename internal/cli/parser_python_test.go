package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestParserInterpreterOnlyMatchesTheShapeSetupWrites: the shapes #1266's
// rule rewrote silently are left alone, so `-u` can no longer turn the
// interpreter into a script argument (#1095).
func TestParserInterpreterOnlyMatchesTheShapeSetupWrites(t *testing.T) {
	cases := []struct {
		cmd  string
		want int
		ok   bool
	}{
		{"env PYTHONPATH=/h/.dexaflow/pysrc/parser /usr/bin/python3.11 -m leoflow_parser", 2, true},
		{"python3 -m leoflow_parser", 0, true},
		{"python -m leoflow_parser", 0, true},
		{"/opt/py/python.exe -m leoflow_parser", 0, true},
		{"python3 -u -m leoflow_parser", 0, false},
		{"python3 -X utf8 -m leoflow_parser", 0, false},
		{"python3 -W ignore -m leoflow_parser", 0, false},
		{"py -3.13 -m leoflow_parser", 0, false},
		{"docker run --rm img python3 -m leoflow_parser", 0, false},
		{"env -i python3 -m leoflow_parser", 0, false},
		{"python3 -m other_parser", 0, false},
		{"python3 -m", 0, false},
		{"", 0, false},
	}
	for _, c := range cases {
		i, ok := parserInterpreter(strings.Fields(c.cmd))
		if ok != c.ok || (ok && i != c.want) {
			t.Errorf("parserInterpreter(%q) = %d, %v; want %d, %v", c.cmd, i, ok, c.want, c.ok)
		}
	}
}

// fakeParser writes an executable named name into dir that answers like the
// parser: it writes the --output file and exits 0, or prints stderr and exits
// 1. It also appends its own name to calls.log so a test sees which ran.
func fakeParser(t *testing.T, dir, name, stderr string) string {
	t.Helper()
	body := "#!/bin/sh\necho " + name + " >> " + filepath.Join(dir, "calls.log") + "\n"
	if stderr == "" {
		body += `while [ $# -gt 0 ]; do if [ "$1" = --output ]; then echo '{}' > "$2"; fi; shift; done` + "\nexit 0\n"
	} else {
		body += "cat >&2 <<'EOF'\n" + stderr + "\nEOF\nexit 1\n"
	}
	p := filepath.Join(dir, name)
	writeExecutable(t, p, body)
	return p
}

// TestParseForDeclaredPython (#1095): a project declaring a newer minor than
// the configured parser's is parsed on that minor; a missing module there falls
// back to the configured parser once, with a warning; and every other case runs
// the configured command alone.
func TestParseForDeclaredPython(t *testing.T) {
	const missing = "Traceback (most recent call last):\nValueError: failed to import dag.py: module 'pandas': not supported by Dexaflow (supported: Bash, Http, Python/@task; no dynamic task mapping or task groups)"
	const syntax = "SyntaxError: Type parameter lists are only supported in Python 3.12 and greater"
	run := func(t *testing.T, oldErr, newErr, declared string, defaulted bool) (calls, stderr string, err error) {
		t.Helper()
		dir := t.TempDir()
		old := fakeParser(t, dir, "python3.11", oldErr)
		newer := fakeParser(t, dir, "python3.13", newErr)
		d := declaredParser{
			lookPath: func(name string) (string, error) {
				if name == "python3.13" {
					return newer, nil
				}
				return "", errors.New("not found")
			},
			runVersion: func(_ context.Context, p string) (int, int, error) {
				switch filepath.Base(p) {
				case "python3.11":
					return 3, 11, nil
				case "python3.13":
					return 3, 13, nil
				}
				return 0, 0, errors.New("unknown")
			},
		}
		// Separate buffers: exec copies the parser's stdout and stderr from two
		// goroutines, so one shared bytes.Buffer would race.
		var out, discard bytes.Buffer
		cmd := &cobra.Command{}
		cmd.SetOut(&discard)
		cmd.SetErr(&out)
		cmd.SetContext(context.Background())
		cfg := &domain.LeoflowConfig{PythonVersion: declared, PythonVersionDefaulted: defaulted}
		err = parseForDeclaredPythonWith(cmd, d, "env PYTHONPATH=/p "+old+" -m leoflow_parser", parserArgs{
			source: "dag.py", config: "dexaflow.yaml", output: filepath.Join(dir, "dag.json"),
			image: "img", dagVersion: "v", projectConfig: cfg,
		})
		log, _ := os.ReadFile(filepath.Join(dir, "calls.log")) //nolint:errcheck // absent means nothing ran
		return strings.TrimSpace(string(log)), out.String(), err
	}

	t.Run("declared newer minor parses there", func(t *testing.T) {
		calls, _, err := run(t, syntax, "", "3.13", false)
		if err != nil || calls != "python3.13" {
			t.Errorf("calls = %q, err = %v; want python3.13 alone and success", calls, err)
		}
	})
	t.Run("a module missing on the declared minor falls back once", func(t *testing.T) {
		calls, stderr, err := run(t, "", missing, "3.13", false)
		if err != nil || calls != "python3.13\npython3.11" {
			t.Fatalf("calls = %q, err = %v; want python3.13 then python3.11, and success", calls, err)
		}
		for _, want := range []string{"module 'pandas'", "python3.11", "python_version 3.13"} {
			if !strings.Contains(stderr, want) {
				t.Errorf("warning lacks %q: %q", want, stderr)
			}
		}
	})
	t.Run("a module missing on both reports the declared minor's error", func(t *testing.T) {
		calls, _, err := run(t, missing, missing, "3.13", false)
		if err == nil || calls != "python3.13\npython3.11" || !strings.Contains(err.Error(), "pandas") {
			t.Errorf("calls = %q, err = %v; want both tried and the missing module reported", calls, err)
		}
	})
	t.Run("a syntax error on the declared minor is the author's", func(t *testing.T) {
		calls, _, err := run(t, "", syntax, "3.13", false)
		if err == nil || calls != "python3.13" {
			t.Errorf("calls = %q, err = %v; want python3.13 alone and its error", calls, err)
		}
	})
	t.Run("a defaulted python_version runs the configured parser", func(t *testing.T) {
		if calls, _, _ := run(t, "", "", "3.13", true); calls != "python3.11" {
			t.Errorf("calls = %q; want the configured python3.11 alone", calls)
		}
	})
	t.Run("a declared minor not newer than the parser runs the configured parser", func(t *testing.T) {
		if calls, _, _ := run(t, "", "", "3.11", false); calls != "python3.11" {
			t.Errorf("calls = %q; want the configured python3.11 alone", calls)
		}
	})
	t.Run("no interpreter of the declared minor warns and runs the configured parser", func(t *testing.T) {
		calls, stderr, _ := run(t, "", "", "3.12", false)
		if calls != "python3.11" || !strings.Contains(stderr, "no python3.12 was found") {
			t.Errorf("calls = %q, stderr = %q; want the configured parser and a warning", calls, stderr)
		}
	})
}
