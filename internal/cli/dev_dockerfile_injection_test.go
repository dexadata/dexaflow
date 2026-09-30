package cli

import (
	"strings"
	"testing"
)

// `leoflow lite --executor=k8s` has its own Dockerfile generator, and it had the
// whole #1070 class untouched: a newline in dag_source or a dbt group's project
// injected an instruction, and dependencies were joined raw, which is #1064 in a
// second place.
//
// It is the worse of the two, because ensureProjectDockerfile WRITES the result
// to <project>/Dockerfile and never removes it. ensureDockerfile then honors a
// project-shipped Dockerfile verbatim, so one `leoflow lite` run would persist
// the poisoned file and every later `compile --build` would use it, bypassing
// every guard the generated path has.
func TestDevDockerfileRefusesTheSameInjections(t *testing.T) {
	const inject = "\nRUN echo surprise"
	cases := []struct {
		name      string
		field     string
		base      string
		dagSource string
		groups    []string
	}{
		{name: "base image", field: "base_image", base: "img" + inject, dagSource: "dag.py"},
		{name: "dag source", field: "dag_source", base: "img", dagSource: "dag.py" + inject},
		{name: "dbt group project", field: "dbt_groups", base: "img", dagSource: "dag.py", groups: []string{"analytics" + inject}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			df, err := devDockerfile(tc.base, tc.dagSource, nil, tc.groups)
			if err == nil {
				t.Fatalf("the dev generator accepted an injected instruction:\n%s", df)
			}
			if !strings.Contains(err.Error(), tc.field) {
				t.Errorf("the error must name the field %q, got: %v", tc.field, err)
			}
		})
	}
}

// The #1064 half. compile_build.go quotes every pip specifier and terminates the
// option list with `--`; the dev generator joined them raw, so a specifier
// beginning with a dash stayed an OPTION to pip. `--dry-run` is the documented
// silent-failure case: the build goes green with the package absent.
func TestDevDockerfileQuotesDependenciesLikeCompileDoes(t *testing.T) {
	df, err := devDockerfile("img", "dag.py", []string{"duckdb==1.1.3", "--dry-run"}, nil)
	if err != nil {
		t.Fatalf("a dash-leading specifier is pip's problem to reject, not a render error: %v", err)
	}
	line := ""
	for _, l := range strings.Split(df, "\n") {
		if strings.Contains(l, "pip install") {
			line = l
			break
		}
	}
	if !strings.Contains(line, " -- ") {
		t.Errorf("pip's option list is not terminated, so `--dry-run` stays an option:\n%s", line)
	}
	if !strings.Contains(line, "'duckdb==1.1.3'") {
		t.Errorf("the specifier is not quoted:\n%s", line)
	}
}
