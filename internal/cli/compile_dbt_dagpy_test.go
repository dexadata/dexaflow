package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// dbtPlusDagPyProject writes a project carrying BOTH a top-level dbt: block and
// a dag.py. Compiling it used to route on cfg.Dbt before the parser was ever
// consulted, so the Python was discarded with no diagnostic (#1001).
func dbtPlusDagPyProject(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	yaml := `schema_version: "1.0"
dag_id: sales
owner: data-team
dbt:
  project: .
  manifest: manifest.json
  granularity: folder
  schedule: "@daily"
`
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if err != nil {
		t.Fatalf("reading fixture manifest: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}
	dbtProj := "name: sales\nversion: \"1.0\"\nprofile: sales\n"
	if werr := os.WriteFile(filepath.Join(dir, "dbt_project.yml"), []byte(dbtProj), 0o600); werr != nil {
		t.Fatal(werr)
	}
	dagPy := "from airflow import DAG\n" +
		"from airflow.providers.standard.operators.python import PythonOperator\n"
	if werr := os.WriteFile(filepath.Join(dir, "dag.py"), []byte(dagPy), 0o600); werr != nil {
		t.Fatal(werr)
	}
	return dir
}

// TestCompileRefusesDbtBlockWithDagPy pins the refusal. The failure this locks
// is not a crash: without it `compile` SUCCEEDS and writes a dag.json holding
// only the dbt nodes, so the author ships a DAG missing every Python task with
// a green exit code.
func TestCompileRefusesDbtBlockWithDagPy(t *testing.T) {
	dir := dbtPlusDagPyProject(t)
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	out2 := filepath.Join(dir, "dag.json")
	err := runCompile(cmd, dir, compileOptions{output: out2, image: "reg/sales:v1", dagVersion: "v1"})
	if err == nil {
		t.Fatalf("compile accepted a dbt: block alongside dag.py and silently dropped the Python (#1001)\noutput:\n%s", out.String())
	}
	for _, want := range []string{"dbt:", "dag.py", "dbt_groups"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q, so it does not tell the author what to do: %v", want, err)
		}
	}
	// A refusal that still emitted the truncated artifact would be worse than
	// none: a later step could pick it up.
	if _, serr := os.Stat(out2); serr == nil {
		t.Errorf("refused but still wrote %s", out2)
	}
}

// TestValidateRefusesDbtBlockWithDagPy pins the same refusal in `validate`,
// which is the command whose whole job is to answer "is this project sane".
// It reported "is valid" for this shape.
func TestValidateRefusesDbtBlockWithDagPy(t *testing.T) {
	dir := dbtPlusDagPyProject(t)
	cmd := newValidateCommand()
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{dir})

	err := cmd.Execute()
	if err == nil {
		t.Fatalf("validate called a dbt: block alongside dag.py valid (#1001)\noutput:\n%s", out.String())
	}
	// Assert on the message. An earlier draft of this test passed with no
	// implementation at all, because the fixture was missing dbt_project.yml and
	// validate errored on THAT — a green that observed nothing.
	for _, want := range []string{"dbt:", "dag.py"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("validate errored for some other reason, not the #1001 conflict (missing %q): %v", want, err)
		}
	}
}

// TestCompileDbtOnlyStillWorks is the guard on the guard: the refusal keys on
// the dag.py EXISTING, so a genuine dbt-only project must stay compilable.
// Without this a stricter-than-intended check would take the whole mode down,
// which is how #996 happened.
func TestCompileDbtOnlyStillWorks(t *testing.T) {
	dir := dbtPlusDagPyProject(t)
	if err := os.Remove(filepath.Join(dir, "dag.py")); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	spec := filepath.Join(dir, "dag.json")
	if err := runCompile(cmd, dir, compileOptions{
		output: spec, image: "reg/sales:v1", dagVersion: "v1",
	}); err != nil {
		t.Fatalf("dbt-only project no longer compiles: %v\noutput:\n%s", err, out.String())
	}
	// A nil error is not the contract — the artifact is. Its sibling asserts the
	// refusal wrote nothing; without the mirror image here, a change that returns
	// nil while producing no dag.json keeps this "guard on the guard" green.
	data, rerr := os.ReadFile(spec)
	if rerr != nil {
		t.Fatalf("compile returned nil but wrote no dag.json: %v", rerr)
	}
	var got domain.DAGSpec
	if uerr := json.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("dag.json is not a DAGSpec: %v", uerr)
	}
	if len(got.Tasks) == 0 {
		t.Fatalf("dbt-only compile produced a spec with no tasks: %s", data)
	}
}

// TestErrDbtBlockWithDagSourceTable drives the predicate directly. The
// round-trip tests above all use the DEFAULT dag.py, so replacing
// dagSourcePath(dir, cfg) with a hardcoded "dag.py" left every one of them
// green — and that is the likeliest mutation to actually get written, because
// discovery deliberately hardcodes the name (discover.go dagSourceFile) and a
// future "make these consistent" pass would silently un-protect every project
// with a custom dag_source.
func TestErrDbtBlockWithDagSourceTable(t *testing.T) {
	write := func(t *testing.T, dir, name string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x = 1\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	cases := []struct {
		name    string
		source  string // cfg.DagSource
		create  string // file to create, "" for none
		mkdir   string // directory to create, "" for none
		dbt     bool
		wantErr bool
	}{
		{name: "no config", dbt: false, wantErr: false},
		{name: "dbt only, no source", source: "dag.py", dbt: true, wantErr: false},
		{name: "dbt plus default source", source: "dag.py", create: "dag.py", dbt: true, wantErr: true},
		// The mutation guard: a hardcoded "dag.py" makes this case pass silently.
		{name: "dbt plus custom source", source: "pipeline.py", create: "pipeline.py", dbt: true, wantErr: true},
		// A custom source that is absent must not be caught by a stray dag.py.
		{name: "custom source absent, stray dag.py", source: "pipeline.py", create: "dag.py", dbt: true, wantErr: false},
		// dag_source has no pattern in the schema, so "." is legal and stats as
		// the project dir. A plain existence check refuses every dbt-only project.
		{name: "dag_source is a directory", source: ".", dbt: true, wantErr: false},
		{name: "directory named like the source", source: "dag.py", mkdir: "dag.py", dbt: true, wantErr: false},
		{name: "source present but no dbt block", source: "dag.py", create: "dag.py", dbt: false, wantErr: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			if tc.create != "" {
				write(t, dir, tc.create)
			}
			if tc.mkdir != "" {
				if err := os.Mkdir(filepath.Join(dir, tc.mkdir), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			var cfg *domain.LeoflowConfig
			if tc.name != "no config" {
				cfg = &domain.LeoflowConfig{DagSource: tc.source}
				if tc.dbt {
					cfg.Dbt = &domain.DbtConfig{Project: "."}
				}
			}
			err := errDbtBlockWithDagSource(dir, cfg)
			if tc.wantErr && err == nil {
				t.Fatalf("want a refusal, got nil")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("want nil, got: %v", err)
			}
			if tc.wantErr {
				// The message is the whole deliverable here, so pin the two
				// escape routes rather than only that something failed.
				for _, want := range []string{"dbt_groups.<name>:", `dbt_group("<name>")`, tc.create} {
					if !strings.Contains(err.Error(), want) {
						t.Errorf("message is missing %q: %v", want, err)
					}
				}
			}
		})
	}
}
