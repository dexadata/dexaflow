package cli

import (
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
