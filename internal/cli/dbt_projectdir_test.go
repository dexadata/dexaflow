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
)

// TestDbtProjectDir: for an image build (Pro) the dbt --project-dir stays
// relative — the image bakes the project at that path. For a local/subprocess
// build (Lite) the task runs on the host from a temp workdir, so it must be the
// ABSOLUTE workspace project path or `dbt --project-dir ./transform` fails with
// "Path './transform' does not exist".
func TestDbtProjectDir(t *testing.T) {
	dag := filepath.FromSlash("/ws/sales")
	cases := []struct {
		name, project string
		local         bool
		want          string
	}{
		{"image build keeps relative", "./transform", false, "./transform"},
		{"local build makes absolute", "./transform", true, filepath.Join(dag, "transform")},
		{"empty project stays empty", "", true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := dbtProjectDir(dag, c.project, c.local); got != c.want {
				t.Errorf("dbtProjectDir(%q, %q, %v) = %q, want %q", dag, c.project, c.local, got, c.want)
			}
		})
	}
}

// TestCompiledEntrypointsAreWiredForTheRightTarget locks the WIRING of #993 and
// #994, through runCompile, because that is where both bugs lived.
//
// The leaf helpers were never wrong: dbtProjectDir computed a correct absolute
// path from a wrong input, and decorateCommands appended a correct flag it was
// never given. So a test of either passes with both bugs fully restored —
// measured, both packages green with `local := !o.build` back. A regression test
// that cannot fail on the actual defect is not one.
//
// #993: `local` decided between an absolute host path for a subprocess executor
// and the relative in-image path for a pod, and it was derived from `!o.build` —
// "we did not build an image this invocation", a different question. A bare
// compile and `deploy --skip-build` both baked the operator's own path into a
// dag.json destined for pods.
//
// #994: the base image aims DBT_PROFILES_DIR at /tmp so dbt's writes stay off
// the read-only project (#852), so dbt never looks inside the project and a
// project shipping its own profiles.yml was unreachable.
func TestCompiledEntrypointsAreWiredForTheRightTarget(t *testing.T) {
	manifest, merr := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if merr != nil {
		t.Fatalf("reading fixture manifest: %v", merr)
	}

	for _, c := range []struct {
		name        string
		local       bool
		hasProfiles bool
		want        []string
		deny        []string
	}{
		{
			name: "an image-bound compile bakes in-image relative paths",
			want: []string{"--project-dir analytics"},
			// The absolute form is the #993 defect; any leading slash after the
			// flag means a host path rode into an artifact destined for a pod.
			deny: []string{"--project-dir /", "--profiles-dir"},
		},
		{
			name:  "a Lite compile bakes the absolute workspace path",
			local: true,
			want:  []string{"--project-dir /"},
		},
		{
			name:        "a project shipping profiles.yml is pointed at it",
			hasProfiles: true,
			want:        []string{"--project-dir analytics", "--profiles-dir analytics"},
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			project := filepath.Join(dir, "analytics")
			if err := os.MkdirAll(project, 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(project, "manifest.json"), manifest, 0o600); err != nil {
				t.Fatal(err)
			}
			if c.hasProfiles {
				if err := os.WriteFile(filepath.Join(project, "profiles.yml"), []byte("analytics:\n  target: dev\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			yaml := "schema_version: \"1.0\"\ndag_id: sales\nowner: t\ndbt:\n  project: analytics\n  manifest: manifest.json\n  schedule: \"@daily\"\n"
			if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
				t.Fatal(err)
			}

			cmd := &cobra.Command{}
			cmd.SetContext(context.Background())
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)

			opts := compileOptions{output: filepath.Join(dir, "dag.json"), image: "reg/sales:v1", dagVersion: "v1", local: c.local}
			if rerr := runCompile(cmd, dir, opts); rerr != nil {
				t.Fatalf("runCompile: %v\noutput:\n%s", rerr, out.String())
			}
			data, rerr := os.ReadFile(filepath.Join(dir, "dag.json"))
			if rerr != nil {
				t.Fatal(rerr)
			}
			var spec struct {
				Tasks []struct {
					TaskID     string `json:"task_id"`
					Entrypoint string `json:"entrypoint"`
				} `json:"tasks"`
			}
			if uerr := json.Unmarshal(data, &spec); uerr != nil {
				t.Fatal(uerr)
			}
			if len(spec.Tasks) == 0 {
				t.Fatal("compiled no tasks — the fixture stopped exercising the dbt path")
			}
			// Every task, not the first: the flags are appended per task and a
			// partial application is exactly the shape that would slip through.
			for _, task := range spec.Tasks {
				for _, w := range c.want {
					if !strings.Contains(task.Entrypoint, w) {
						t.Errorf("task %q entrypoint %q is missing %q", task.TaskID, task.Entrypoint, w)
					}
				}
				for _, d := range c.deny {
					if strings.Contains(task.Entrypoint, d) {
						t.Errorf("task %q entrypoint %q must not contain %q", task.TaskID, task.Entrypoint, d)
					}
				}
			}
		})
	}
}

// TestDbtParseBinDoesNotDependOnTheRuntimeTarget separates the last piece of the
// #993 conflation. Which dbt parses the manifest was gated on `local` — "the DAG
// will run on this host" — but that is a different question from "which dbt on
// this host is the better parser". A per-DAG venv dbt is that DAG's own, pinned
// to the adapter its dexaflow.yaml declares; PATH's is whatever the operator
// happens to have installed. When both exist the venv one is strictly better,
// whether the compiled artifact ends up in a pod or in a subprocess — and after
// #993 a bare `leoflow compile` stopped being "local", so it silently lost
// access to the venv dbt it had been using.
func TestDbtParseBinDoesNotDependOnTheRuntimeTarget(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".dexaflow", "dev", "venvs", "sales", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	venvDbt := filepath.Join(binDir, "dbt")
	if err := os.WriteFile(venvDbt, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil { //nolint:gosec // test stub must be executable
		t.Fatal(err)
	}

	if got := dbtParseBinAt(home, "sales"); got != venvDbt {
		t.Errorf("dbtParseBinAt = %q, want the DAG's own venv dbt %q", got, venvDbt)
	}
	// No venv for this DAG: fall back to PATH, which is what the not-found error
	// downstream is written about.
	if got := dbtParseBinAt(home, "other"); got != "dbt" {
		t.Errorf("dbtParseBinAt with no venv = %q, want \"dbt\"", got)
	}
	if got := dbtParseBinAt("", "sales"); got != "dbt" {
		t.Errorf("dbtParseBinAt with no home = %q, want \"dbt\"", got)
	}
}

// TestManifestParseUsesTheVenvDbtEvenForAnImageBoundCompile locks the WIRING of
// the parse-binary split, not the leaf. dbtParseBinAt has a table test above;
// re-gating loadDbtManifest's call to it on the runtime target leaves that table
// green, which is round one's lesson repeating one layer down: the leaf was
// never wrong, the wiring was.
//
// It also cannot be reached through TestCompiledEntrypointsAreWiredForTheRightTarget,
// because every case there pins `manifest:` and loadDbtManifest short-circuits
// before choosing a binary. This one deliberately leaves the manifest unpinned.
func TestManifestParseUsesTheVenvDbtEvenForAnImageBoundCompile(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, ".dexaflow", "dev", "venvs", "sales", "bin")
	if err := os.MkdirAll(binDir, 0o750); err != nil {
		t.Fatal(err)
	}
	// Exits 3 so the error is unmistakably THIS binary and not PATH's.
	if err := os.WriteFile(filepath.Join(binDir, "dbt"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil { //nolint:gosec // a test stub must be executable
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", t.TempDir()) // no dbt on PATH at all

	dir := t.TempDir()
	project := filepath.Join(dir, "analytics")
	if err := os.MkdirAll(project, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "dbt_project.yml"), []byte("name: a\nversion: \"1.0\"\nprofile: a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	// No `manifest:` — this is the path that actually runs `dbt parse`.
	yaml := "schema_version: \"1.0\"\ndag_id: sales\nowner: t\ndbt:\n  project: analytics\n  schedule: \"@daily\"\n"
	if err := os.WriteFile(filepath.Join(dir, "dexaflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	// local:false — an IMAGE-bound compile. It must still prefer the venv dbt:
	// that is the DAG's own, pinned to the adapter its dexaflow.yaml declares.
	err := runCompile(cmd, dir, compileOptions{output: filepath.Join(dir, "dag.json"), image: "reg/s:v1", dagVersion: "v1"})
	if err == nil {
		t.Fatal("expected the stub dbt to fail the parse")
	}
	if strings.Contains(err.Error(), "not on PATH") {
		t.Fatalf("an image-bound compile fell back to PATH instead of the DAG's own venv dbt: %v", err)
	}
	if !strings.Contains(err.Error(), "exit status 3") {
		t.Errorf("error %q does not show the venv stub ran", err)
	}
}
