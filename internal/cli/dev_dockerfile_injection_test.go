package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// `dexaflow lite --executor=k8s` has its own Dockerfile generator, and it had the
// whole #1070 class untouched: a newline in dag_source or a dbt group's project
// injected an instruction, and dependencies were joined raw, which is #1064 in a
// second place.
//
// It is the worse of the two, because ensureProjectDockerfile WRITES the result
// to <project>/Dockerfile and never removes it. ensureDockerfile then honors a
// project-shipped Dockerfile verbatim, so one `dexaflow lite` run would persist
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

// The `.` group short-circuit in the dev generator had no test: neutering it
// left the whole package green, while the compile-side equivalent was covered.
// It matters twice over, because `COPY . /home/leoflow/` copies the entire
// build context, and because returning early is what let a poisoned sibling
// group skip validation.
func TestDevDockerfileHandlesTheDotGroup(t *testing.T) {
	df, err := devDockerfile("img", "dag.py", nil, []string{".", "transform"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(df, "COPY . /home/leoflow/\n") {
		t.Errorf("a `.` group must collapse to one whole-context COPY:\n%s", df)
	}
	if strings.Contains(df, "COPY dag.py") || strings.Contains(df, "COPY transform") {
		t.Errorf("the whole-context COPY already carries these; they must not be repeated:\n%s", df)
	}
	// And a poisoned sibling is still refused, rather than skipped because the
	// `.` entry returned first.
	if _, derr := devDockerfile("img", "dag.py", nil, []string{".", "p\nRUN evil"}); derr == nil {
		t.Error("a `.` group let a poisoned sibling group through unvalidated")
	}
}

// The dev-side dag_source guard had no coverage: the existing case uses a value
// with no slash, so filepath.Base is the identity and the downstream COPY guard
// catches it anyway. Only a value WITH a slash tells guarded from unguarded, and
// that is the value the compile side already uses.
func TestDevDockerfileRefusesADagSourceWhoseNewlineBaseWouldHide(t *testing.T) {
	if _, err := devDockerfile("img", "x\nRUN evil/dag.py", nil, nil); err == nil {
		t.Error("filepath.Base hid the newline instead of it being refused")
	}
}

// devDockerfile only ever receives the CLEANED group list, and filepath.Clean
// turns "evil\nstuff/.." into ".", so the raw guard has to sit at the caller.
// Removing it left the suite green: the loop inside devDockerfile looks like
// coverage and is not, for exactly the value that needs it.
func TestEnsureProjectDockerfileRefusesARawDbtGroupPath(t *testing.T) {
	dir := t.TempDir()
	cfg := &domain.LeoflowConfig{DagID: "d"}
	cfg.ApplyDefaults()
	cfg.DbtGroups = map[string]*domain.DbtConfig{"b": {Project: "evil\nstuff/.."}}
	if err := ensureProjectDockerfile(devTestCmd(), dir, cfg); err == nil {
		got, _ := os.ReadFile(filepath.Join(dir, "Dockerfile"))
		t.Fatalf("a Clean-collapsing group path was written to the project Dockerfile:\n%s", got)
	}
	if _, serr := os.Stat(filepath.Join(dir, "Dockerfile")); serr == nil {
		t.Error("a refused generation still left a Dockerfile behind, and a project-shipped Dockerfile is afterwards honored verbatim")
	}
}
