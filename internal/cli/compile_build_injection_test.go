package cli

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/neochaotic/leoflow/internal/domain"
)

// #1066 gave `dependencies` and `system_packages` a line-break guard because a
// newline ends the RUN instruction and turns the rest into a new Dockerfile
// line. The same class lives in every OTHER value the generator interpolates,
// and those reach `FROM` and `COPY`, which no quoting protects: the Dockerfile
// format itself gives a newline meaning, and no shell is involved.
//
// One test per field, because the #1066 review found its own guard had no test
// for `system_packages`, so restricting it to one field was a wrong fix the
// suite accepted.
func TestNewlineInADockerfileValueIsRefused(t *testing.T) {
	const inject = "\nRUN echo surprise"
	cases := []struct {
		name      string
		field     string
		dagSource string
		mutate    func(*domain.LeoflowConfig)
	}{
		{
			name:   "base_image lands in FROM verbatim",
			field:  "base_image",
			mutate: func(c *domain.LeoflowConfig) { c.BaseImage = "python:3.11-slim" + inject },
		},
		{
			name:   "dbt.project lands in COPY",
			field:  "dbt.project",
			mutate: func(c *domain.LeoflowConfig) { c.Dbt = &domain.DbtConfig{Project: "analytics" + inject} },
		},
		{
			name:  "a dbt group's project lands in COPY",
			field: "dbt_groups",
			mutate: func(c *domain.LeoflowConfig) {
				c.DbtGroups = map[string]*domain.DbtConfig{"g": {Project: "analytics" + inject}}
			},
		},
		{
			name:      "the dag_source basename lands in COPY",
			field:     "dag_source",
			dagSource: "dag.py" + inject,
			mutate:    func(_ *domain.LeoflowConfig) {},
		},
		{
			name:   "an include_paths entry lands in COPY",
			field:  "include_paths",
			mutate: func(c *domain.LeoflowConfig) { c.IncludePaths = []string{".", "helpers" + inject} },
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &domain.LeoflowConfig{DagID: "d"}
			cfg.ApplyDefaults()
			tc.mutate(cfg)
			src := tc.dagSource
			if src == "" {
				src = "dag.py"
			}
			df, err := generatedDockerfile(cfg, src)
			if err == nil {
				t.Fatalf("a newline was accepted and rendered an extra instruction:\n%s", df)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the error must name the field %q, got: %v", tc.field, err)
			}
		})
	}
}

// A carriage return is the same defect: Docker's parser ends a line on it too,
// and a CRLF file edited on Windows is the likeliest way it arrives.
func TestCarriageReturnInADockerfileValueIsRefused(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.BaseImage = "python:3.11-slim\rRUN echo surprise"
	if df, err := generatedDockerfile(cfg, "dag.py"); err == nil {
		t.Fatalf("a carriage return was accepted:\n%s", df)
	}
}

// A space cannot inject an instruction, but it silently changes which operands
// Docker sees: COPY takes N sources and one destination, so an unquoted
// two-word path becomes a different copy than the author wrote. Quoting it is
// the fix, and the JSON form is the only quoting COPY understands.
func TestWhitespaceInACopyOperandIsQuoted(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	df, err := generatedDockerfile(cfg, "my dag.py")
	if err != nil {
		t.Fatalf("a path with a space is a legal filename, not an error: %v", err)
	}
	line := ""
	for _, l := range strings.Split(df, "\n") {
		if strings.HasPrefix(l, "COPY") {
			line = l
			break
		}
	}
	if !strings.Contains(line, `["my dag.py"`) {
		t.Errorf("a COPY operand with a space must use the JSON form, got: %q", line)
	}
}

// The counterweight to that: every path WITHOUT whitespace must keep rendering
// exactly as it did. The example Dockerfiles are committed and drift-gated, and
// eight e2e scripts grep for `COPY dag.py`, so switching the form wholesale
// would be churn with a real chance of breaking them.
func TestOrdinaryCopyLinesKeepTheShellForm(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "COPY dag.py /home/leoflow/dag.py\n") {
		t.Errorf("the ordinary COPY form changed, which drifts the committed examples:\n%s", df)
	}
}

// The first version of these tests passed with the whole line-break guard
// deleted: every payload ended in "RUN echo surprise", which contains a SPACE,
// so fromOperand's whitespace check caught the FROM cases before the line-break
// check ever ran. The FROM path had no line-break coverage at all, and the
// carriage return had none anywhere. Whitespace-free payloads fix that.
func TestFromRefusesALineBreakIndependentlyOfWhitespace(t *testing.T) {
	for _, bad := range []string{
		"alpine\nVOLUME/x", // newline, no space anywhere
		"alpine\rVOLUME/x", // carriage return
		"alpine\vAS\vevil", // vertical tab: a BuildKit word separator
		"alpine\fAS\fevil", // form feed: likewise
	} {
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		cfg.BaseImage = bad
		df, err := generatedDockerfile(cfg, "dag.py")
		if err == nil {
			t.Errorf("base_image %q was accepted:\n%s", bad, df)
		}
	}
}

// BuildKit splits a Dockerfile line on `[\t\v\f\r ]+`, so a vertical tab or a
// form feed is a word separator exactly like a space. Measured: `COPY a<VT>b.py
// /home/leoflow/a<VT>b.py` is read as three sources and the destination "b",
// which hands an attacker an arbitrary destination inside the image.
func TestCopyRefusesTheOtherWordSeparators(t *testing.T) {
	for _, bad := range []string{"a\vb.py", "a\fb.py"} {
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		if df, err := generatedDockerfile(cfg, bad); err == nil {
			t.Errorf("dag_source %q was accepted:\n%s", bad, df)
		}
	}
}

// The JSON form does NOT protect a COPY operand. After parsing, every operand
// goes through shell.Lex.ProcessWord, which strips quotes, eats backslashes and
// expands $VAR, in the JSON form as much as the shell form. Measured against
// BuildKit v0.28.1:
//
//	COPY ["d'a't.py", "/home/leoflow/d'a't.py"]  ->  copies dat.py
//
// So these are refused. The first version of this guard quoted them, which
// moved `"` and `\` to a form that does not help and left `'` and `$` to
// silently copy a different path.
func TestCopyRefusesWhatTheOperandLexerRewrites(t *testing.T) {
	for _, bad := range []string{`d'a't.py`, `an\alytics.py`, `a"b.py`, `$HOME.py`} {
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		df, err := generatedDockerfile(cfg, bad)
		if err == nil {
			t.Errorf("dag_source %q was accepted; the lexer would rewrite it:\n%s", bad, df)
			continue
		}
		if !strings.Contains(err.Error(), "dag_source") {
			t.Errorf("the error for %q does not name the field: %v", bad, err)
		}
	}
}

// A leading `--` is read as a COPY flag (--from, --chown, --link), which eats
// the operand and leaves Docker complaining that COPY needs two arguments.
// Naming the entry is the whole point of validating it here.
func TestCopyRefusesAFlagLookalikePath(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.IncludePaths = []string{".", "--from=alpine"}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err == nil {
		t.Fatalf("a `--` path was accepted:\n%s", df)
	}
	if !strings.Contains(err.Error(), "include_paths") {
		t.Errorf("the error does not name the field: %v", err)
	}
}

// fromOperand's whitespace rule is the only genuinely new RULE this change
// adds, and nothing covered it: deleting the check entirely left the suite
// green, because every other test's payload also carried a newline.
func TestFromRefusesWhitespaceEvenWithoutALineBreak(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.BaseImage = "alpine AS builder"
	if df, err := generatedDockerfile(cfg, "dag.py"); err == nil {
		t.Fatalf("a FROM with whitespace was accepted, which names a build stage:\n%s", df)
	}
}

// A leading `[` is what makes the shell form look like the JSON form to
// Docker's parser. A `[` anywhere else is an ordinary glob character class and
// must keep working: refusing it, or quoting it, would break a legitimate
// include_paths entry.
func TestGlobCharacterClassIsNotDisturbed(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.IncludePaths = []string{".", "data/[0-9]*.csv"}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err != nil {
		t.Fatalf("a glob character class is a legal path: %v", err)
	}
	if !strings.Contains(df, "COPY data/[0-9]*.csv /home/leoflow/data/[0-9]*.csv\n") {
		t.Errorf("a glob path should keep the ordinary form:\n%s", df)
	}
}

// exclude_paths reaches the generated .dockerignore, which is line-oriented, so
// a newline there injects its own lines. The damaging shape is a negation:
// expandPattern deliberately never emits one, because leoflow's block is
// appended AFTER the author's own lines and a `!` could resurrect a path they
// excluded. An injected `!secrets.env` does exactly that, and
// warnDroppedNegations does not see it because it only checks the entry's
// prefix.
func TestNewlineInAnExcludePathIsRefused(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.ExcludePaths = []string{"foo\n!secrets.env"}

	// Through the real entry point, not the helper. Calling checkExcludePaths
	// directly proved only that a pure function returns an error: unwiring the
	// call site in ensureDockerignore left the suite green.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("secrets.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup, _, err := ensureDockerignore(io.Discard, dir, cfg, false)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		got, _ := os.ReadFile(filepath.Join(dir, ".dockerignore"))
		t.Fatalf("a newline in exclude_paths was accepted; the .dockerignore became:\n%s", got)
	}
	if !strings.Contains(err.Error(), "exclude_paths") {
		t.Errorf("the error does not name the field: %v", err)
	}
}

// The guard has to sit before the value is trimmed, not after. A LEADING
// newline was stripped by TrimSpace on the way in, so the entry produced no
// injection but also no named refusal: the author got a Docker "not found"
// instead of being told what was wrong with their yaml.
func TestALeadingNewlineIsRefusedRatherThanTrimmed(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.IncludePaths = []string{".", "\nhelpers"}
	df, err := generatedDockerfile(cfg, "dag.py")
	if err == nil {
		t.Fatalf("a leading newline was trimmed instead of refused:\n%s", df)
	}
	if !strings.Contains(err.Error(), "include_paths") {
		t.Errorf("the error does not name the field: %v", err)
	}
}

// A source STARTING with `[` is what makes the shell form look like the JSON
// form to Docker's parser, so it has to be quoted. Nothing covered the leading
// case: dropping `[` from the trigger set left the suite green.
func TestALeadingBracketPathIsQuoted(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	df, err := generatedDockerfile(cfg, "[archive].py")
	if err != nil {
		t.Fatalf("a leading bracket is a legal filename: %v", err)
	}
	if !strings.Contains(df, `COPY ["[archive].py"`) {
		t.Errorf("a path starting with `[` must use the JSON form, or Docker reads the line as one:\n%s", df)
	}
}

// The .dockerignore guard has to cover what is EMITTED, not one input field.
// checkExcludePaths guarded cfg.ExcludePaths, and one line later
// dbtBuildArtifacts interpolated every dbt project path into the same pattern
// list, so a poisoned dbt group reached the file unvalidated.
//
// The `.` group is what makes it reachable: writeDagSourceCopies short-circuits
// on it and returns after `COPY . /home/leoflow/`, so no group is validated
// there either. And `COPY .` is what then bakes the resurrected file into the
// image.
func TestNewlineInADbtProjectCannotReachTheDockerignore(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.DbtGroups = map[string]*domain.DbtConfig{
		"a": {Project: "."},
		"b": {Project: "p\n!secrets.env\nq"},
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".dockerignore"), []byte("secrets.env\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cleanup, _, err := ensureDockerignore(io.Discard, dir, cfg, false)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		got, _ := os.ReadFile(filepath.Join(dir, ".dockerignore"))
		t.Fatalf("a dbt project path injected .dockerignore lines:\n%s", got)
	}
}

// A project that ships its own Dockerfile never reaches generatedDockerfile at
// all, so the COPY guards never run, but the .dockerignore is still generated
// from the same config.
func TestTheDockerignoreIsGuardedEvenWithAProjectDockerfile(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.Dbt = &domain.DbtConfig{Project: "p\n!secrets.env\nq"}
	dir := t.TempDir()
	cleanup, _, err := ensureDockerignore(io.Discard, dir, cfg, true)
	if cleanup != nil {
		cleanup()
	}
	if err == nil {
		t.Fatal("with a project-shipped Dockerfile the .dockerignore was generated unvalidated")
	}
}

// A tab is a legal character in a JSON string only when ESCAPED. Building the
// literal by hand emitted a raw one, which is invalid JSON, so Docker falls
// back to the shell form and then splits the line on the tab it was quoting.
// The trigger set said a tab needs the JSON form and the emitter then produced
// a JSON form that does not parse.
func TestATabbedPathSurvivesAsValidJSON(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	df, err := generatedDockerfile(cfg, "a\tb.py")
	if err != nil {
		t.Fatalf("a tab is quotable, not a refusal: %v", err)
	}
	line := ""
	for _, l := range strings.Split(df, "\n") {
		if strings.HasPrefix(l, "COPY") {
			line = l
			break
		}
	}
	operands := strings.TrimPrefix(line, "COPY ")
	var got []string
	if jerr := json.Unmarshal([]byte(operands), &got); jerr != nil {
		t.Fatalf("the emitted COPY is not valid JSON, so Docker falls back to the shell form and splits on the tab: %v\nline: %q", jerr, line)
	}
	if got[0] != "a\tb.py" {
		t.Errorf("the path did not round-trip: %q", got[0])
	}
}

// `<<` makes COPY a heredoc at the parser level, and `<` is the fourth entry in
// the operand lexer's character table, which the refusal set missed. An
// unterminated heredoc swallows every generated line after it, including the
// non-root USER drop, and then fails the parse.
func TestAHeredocLookalikePathIsRefused(t *testing.T) {
	for _, bad := range []string{"<<EOF", "1<<EOF"} {
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		cfg.IncludePaths = []string{".", bad}
		if df, err := generatedDockerfile(cfg, "dag.py"); err == nil {
			t.Errorf("include_paths %q was accepted; COPY reads it as a heredoc:\n%s", bad, df)
		}
	}
}

// dag_source and dbt.project were guarded AFTER the transform that hides the
// bad character: filepath.Base eats everything before the last slash and
// filepath.Clean turns "evil\nstuff/.." into ".". Neither injects, but neither
// is a named refusal either, and `COPY . /home/leoflow/.` quietly copies the
// whole context instead.
func TestTheGuardRunsBeforeTheTransformThatHidesIt(t *testing.T) {
	t.Run("dag_source", func(t *testing.T) {
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		if df, err := generatedDockerfile(cfg, "x\nRUN evil/dag.py"); err == nil {
			t.Errorf("filepath.Base hid the newline instead of it being refused:\n%s", df)
		}
	})
	t.Run("dbt.project", func(t *testing.T) {
		cfg := &domain.LeoflowConfig{DagID: "d"}
		cfg.ApplyDefaults()
		cfg.Dbt = &domain.DbtConfig{Project: "evil\nstuff/.."}
		if df, err := generatedDockerfile(cfg, "dag.py"); err == nil {
			t.Errorf("filepath.Clean collapsed the value to `.` instead of it being refused:\n%s", df)
		}
	})
}

// The compile generator's own check before the `.` short-circuit. The
// .dockerignore guard happens to catch this value too, which is why removing
// this one left the suite green: the exploit path was covered, the defense in
// depth was not. The dev generator has the same assertion, and the two must not
// drift.
func TestADotGroupDoesNotLetASiblingSkipValidation(t *testing.T) {
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.DbtGroups = map[string]*domain.DbtConfig{
		"a": {Project: "."},
		"b": {Project: "p\nRUN evil"},
	}
	if df, err := generatedDockerfile(cfg, "dag.py"); err == nil {
		t.Fatalf("a `.` group returned before the poisoned sibling was looked at:\n%s", df)
	}
}
