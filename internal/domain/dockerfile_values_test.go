package domain

import (
	"strings"
	"testing"
)

// dockerfileCfg is a schema-valid dag.py project with the defaults applied, the
// shape every command sees after loading a dexaflow.yaml.
func dockerfileCfg(mutate func(*LeoflowConfig)) *LeoflowConfig {
	c := &LeoflowConfig{SchemaVersion: "1.0", DagID: "sales"}
	c.ApplyDefaults()
	mutate(c)
	return c
}

// The values the generated Dockerfile interpolates used to be refused only by
// the renderer, which runs under `compile --build` alone: `validate` and plain
// `compile` called the project valid, and the refusal first arrived on the CI
// runner about to build it (#1268). Validate is what every command that reads a
// dexaflow.yaml calls, so the refusal belongs there, naming the field.
//
// One case per value #1267's renderer tests exercise.
func TestValidateRefusesWhatTheGeneratedDockerfileCannotCarry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		field  string
		mutate func(*LeoflowConfig)
	}{
		{"base_image newline", "base_image", func(c *LeoflowConfig) { c.BaseImage = "alpine\nVOLUME/x" }},
		{"base_image carriage return", "base_image", func(c *LeoflowConfig) { c.BaseImage = "alpine\rVOLUME/x" }},
		{"base_image vertical tab", "base_image", func(c *LeoflowConfig) { c.BaseImage = "alpine\vAS\vevil" }},
		{"base_image form feed", "base_image", func(c *LeoflowConfig) { c.BaseImage = "alpine\fAS\fevil" }},
		{"base_image whitespace", "base_image", func(c *LeoflowConfig) { c.BaseImage = "alpine AS builder" }},
		{"dag_source newline", "dag_source", func(c *LeoflowConfig) { c.DagSource = "dag.py\nRUN echo surprise" }},
		{"dag_source newline hidden by Base", "dag_source", func(c *LeoflowConfig) { c.DagSource = "x\nRUN evil/dag.py" }},
		{"dag_source vertical tab", "dag_source", func(c *LeoflowConfig) { c.DagSource = "a\vb.py" }},
		{"dag_source form feed", "dag_source", func(c *LeoflowConfig) { c.DagSource = "a\fb.py" }},
		{"dag_source single quote", "dag_source", func(c *LeoflowConfig) { c.DagSource = `d'a't.py` }},
		{"dag_source backslash", "dag_source", func(c *LeoflowConfig) { c.DagSource = `an\alytics.py` }},
		{"dag_source double quote", "dag_source", func(c *LeoflowConfig) { c.DagSource = `a"b.py` }},
		{"dag_source dollar", "dag_source", func(c *LeoflowConfig) { c.DagSource = `$HOME.py` }},
		{"dbt.project newline", "dbt.project", func(c *LeoflowConfig) {
			c.Dbt = &DbtConfig{Project: "analytics\nRUN echo surprise"}
		}},
		{"dbt.project newline hidden by Clean", "dbt.project", func(c *LeoflowConfig) {
			c.Dbt = &DbtConfig{Project: "evil\nstuff/.."}
		}},
		{"dbt.project dollar", "dbt.project", func(c *LeoflowConfig) { c.Dbt = &DbtConfig{Project: "raw/$schema"} }},
		{"dbt group newline", "dbt_groups.g.project", func(c *LeoflowConfig) {
			c.DbtGroups = map[string]*DbtConfig{"g": {Project: "analytics\nRUN echo surprise"}}
		}},
		{"dbt group hidden behind a dot sibling", "dbt_groups.b.project", func(c *LeoflowConfig) {
			c.DbtGroups = map[string]*DbtConfig{"a": {Project: "."}, "b": {Project: "p\n!secrets.env\nq"}}
		}},
		{"dbt group backslash", "dbt_groups.g.project", func(c *LeoflowConfig) {
			c.DbtGroups = map[string]*DbtConfig{"g": {Project: `sql\queries`}}
		}},
		{"include_paths newline", "include_paths", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "helpers\nRUN x"} }},
		{"include_paths leading newline", "include_paths", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "\nhelpers"} }},
		{"include_paths flag lookalike", "include_paths", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "--from=alpine"} }},
		{"include_paths heredoc", "include_paths", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "<<EOF"} }},
		{"include_paths heredoc mid-path", "include_paths", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "1<<EOF"} }},
		{"include_paths quote", "include_paths", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "it's"} }},
		{"exclude_paths newline", "exclude_paths", func(c *LeoflowConfig) { c.ExcludePaths = []string{"foo\n!secrets.env"} }},
		{"dependencies newline", "dependencies", func(c *LeoflowConfig) { c.Dependencies = []string{"six\nRUN evil"} }},
		{"system_packages newline", "system_packages", func(c *LeoflowConfig) { c.SystemPackages = []string{"curl\rRUN evil"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dockerfileCfg(tc.mutate).Validate()
			if err == nil {
				t.Fatal("Validate accepted a value the generated Dockerfile cannot carry")
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the error must name %q, got: %v", tc.field, err)
			}
		})
	}
}

// The counterweight: every shape the renderer quotes rather than refuses, and
// the PEP 508 forms #1064 made sure survive, must stay valid. Refusing any of
// these would break a project that builds today.
func TestValidateKeepsWhatTheGeneratedDockerfileCanCarry(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*LeoflowConfig)
	}{
		{"defaults", func(*LeoflowConfig) {}},
		{"a space in dag_source", func(c *LeoflowConfig) { c.DagSource = "my dag.py" }},
		{"a tab in dag_source", func(c *LeoflowConfig) { c.DagSource = "a\tb.py" }},
		{"a leading bracket", func(c *LeoflowConfig) { c.DagSource = "[archive].py" }},
		{"a glob character class", func(c *LeoflowConfig) { c.IncludePaths = []string{".", "data/[0-9]*.csv"} }},
		{"a nested dag_source", func(c *LeoflowConfig) { c.DagSource = "dags/sales.py" }},
		{"a digest-pinned base_image", func(c *LeoflowConfig) {
			c.BaseImage = "ghcr.io/dexadata/dexaflow-runtime:py3.11@sha256:" + strings.Repeat("a", 64)
		}},
		{"a PEP 508 marker", func(c *LeoflowConfig) {
			c.Dependencies = []string{`requests; python_version < "3.9"`}
		}},
		{"a dot dbt group", func(c *LeoflowConfig) { c.DbtGroups = map[string]*DbtConfig{"a": {Project: "."}} }},
		{"a dbt project", func(c *LeoflowConfig) { c.Dbt = &DbtConfig{Project: "transform/analytics"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := dockerfileCfg(tc.mutate).Validate(); err != nil {
				t.Fatalf("Validate refused a value the generated Dockerfile carries: %v", err)
			}
		})
	}
}
