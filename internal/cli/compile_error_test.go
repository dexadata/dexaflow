package cli

import (
	"strings"
	"testing"
)

// TestParserErrorSummary covers #D9 from the Lite dogfood audit (#212): when
// the parser fails on a broken dag.py the user previously got only the raw
// Python traceback (with our internal parser paths in it) dumped to the
// terminal. parserErrorSummary lifts the meaningful `*Error: message` line
// out of the traceback so the compile/validate caller can lead with a clean,
// one-line summary before the bounded traceback tail.
func TestParserErrorSummary(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		stderr string
		want   string
	}{
		{
			name: "syntax error",
			stderr: `  File "/tmp/dag.py", line 1
    this_is_not_python
                     ^
SyntaxError: invalid syntax`,
			want: "SyntaxError: invalid syntax",
		},
		{
			name: "name error wraps deep traceback",
			stderr: `Traceback (most recent call last):
  File "parser/leoflow_parser/compiler.py", line 73, in compile
    user_module = importlib.import_module(...)
  File "/tmp/dag.py", line 8, in <module>
    do_stuff(undefined)
NameError: name 'undefined' is not defined`,
			want: "NameError: name 'undefined' is not defined",
		},
		{
			name: "import error",
			stderr: `Traceback (most recent call last):
  File "/tmp/dag.py", line 1, in <module>
    from nonexistent import thing
ImportError: No module named 'nonexistent'`,
			want: "ImportError: No module named 'nonexistent'",
		},
		{
			name:   "no python error pattern → empty",
			stderr: "some non-python output\nrandom line",
			want:   "",
		},
		{
			name:   "empty input",
			stderr: "",
			want:   "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parserErrorSummary(tc.stderr)
			if got != tc.want {
				t.Errorf("parserErrorSummary mismatch\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

// TestParserErrorSummaryPrefersUserError pins the precedence: when both the
// user's dag.py file and an internal parser file appear in the traceback, the
// summary should reflect the *last* error line — that is the actual cause,
// not an intermediate wrapper.
func TestParserErrorSummaryPrefersUserError(t *testing.T) {
	t.Parallel()
	stderr := `Traceback (most recent call last):
  File "parser/wrapper.py", line 12, in run
    raise WrapError("inner") from None
WrapError: inner
During handling of the above exception, another exception occurred:
  File "/tmp/dag.py", line 3, in <module>
    1/0
ZeroDivisionError: division by zero`
	got := parserErrorSummary(stderr)
	if !strings.Contains(got, "ZeroDivisionError") {
		t.Errorf("expected ZeroDivisionError (the deepest cause); got: %q", got)
	}
}
