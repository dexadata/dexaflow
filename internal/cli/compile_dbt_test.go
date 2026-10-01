package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestCompileDbtProject drives `leoflow compile` on a dbt project: a leoflow.yaml
// with a dbt block and a pre-baked manifest.json (no dbt binary needed) must
// produce a valid dag.json with one task per folder group.
func TestCompileDbtProject(t *testing.T) {
	dir := t.TempDir()
	yaml := `schema_version: "1.0"
dag_id: sales
owner: data-team
tags: [analytics, dbt]
dbt:
  project: .
  manifest: manifest.json
  granularity: folder
  schedule: "@daily"
`
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if err != nil {
		t.Fatalf("reading fixture manifest: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	o := compileOptions{output: filepath.Join(dir, "dag.json"), image: "reg/sales:v1", dagVersion: "v1"}
	if rerr := runCompile(cmd, dir, o); rerr != nil {
		t.Fatalf("runCompile (dbt path): %v\noutput:\n%s", rerr, out.String())
	}

	data, err := os.ReadFile(filepath.Join(dir, "dag.json"))
	if err != nil {
		t.Fatalf("dag.json not produced: %v", err)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatalf("dag.json invalid JSON: %v", uerr)
	}
	if spec.DagID != "sales" {
		t.Errorf("dag_id = %q, want sales", spec.DagID)
	}
	if spec.Schedule == nil || *spec.Schedule != "@daily" {
		t.Errorf("schedule = %v, want @daily", spec.Schedule)
	}
	if spec.Owner != "data-team" {
		t.Errorf("owner = %q, want data-team (leoflow.yaml owner must overlay onto the dbt DAG)", spec.Owner)
	}
	if !reflect.DeepEqual(spec.Tags, []string{"analytics", "dbt"}) {
		t.Errorf("tags = %v, want [analytics dbt]", spec.Tags)
	}
	if len(spec.Tasks) != 3 { // folder grouping: seeds, staging, marts
		t.Fatalf("got %d tasks, want 3: %+v", len(spec.Tasks), spec.Tasks)
	}
	for _, ts := range spec.Tasks {
		if ts.Type != domain.TaskTypeBash {
			t.Errorf("task %q type = %q, want bash", ts.TaskID, ts.Type)
		}
	}
}

// A dbt_group placeholder with no matching dbt_groups config gives an actionable
// error (naming the group), not a cryptic invalid-type failure downstream.
func TestExpandDbtGroupWithoutConfigIsActionable(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(`{"schema_version":"1.0","dag_id":"d","dag_version":"v1","image":"img","tasks":[{"task_id":"analytics","type":"dbt_group"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	err := expandDbtGroupsInFile(cmd, dir, out, &domain.LeoflowConfig{DagID: "d"}, false)
	if err == nil || !strings.Contains(err.Error(), "analytics") {
		t.Fatalf("want an actionable error naming the group, got: %v", err)
	}
}

// With no dbt_group placeholders, expansion is a harmless no-op.
func TestExpandDbtGroupsNoOp(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "dag.json")
	original := `{"schema_version":"1.0","dag_id":"d","dag_version":"v1","image":"img","tasks":[{"task_id":"x","type":"python","entrypoint":"m:x"}]}`
	if err := os.WriteFile(out, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	if err := expandDbtGroupsInFile(cmd, dir, out, &domain.LeoflowConfig{DagID: "d"}, false); err != nil {
		t.Fatalf("no-op expansion errored: %v", err)
	}
	data, _ := os.ReadFile(out)
	if string(data) != original {
		t.Errorf("no-op expansion should leave the file untouched")
	}
}

// With a managed connection, the expanded dbt tasks' commands are wrapped with the
// runtime profile-generation step, using the profile name read from dbt_project.yml.
func TestExpandDbtGroupsWithManagedConnection(t *testing.T) {
	dir := t.TempDir()
	dagJSON := `{"schema_version":"1.0","dag_id":"d","dag_version":"v1","image":"img","tasks":[
		{"task_id":"g","type":"dbt_group"}
	]}`
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(dagJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if mkErr := os.MkdirAll(filepath.Join(dir, "proj"), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "proj", "dbt_project.yml"),
		[]byte("name: proj\nprofile: wh_profile\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_chain.json"))
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "proj", "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID: "d",
		DbtGroups: map[string]*domain.DbtConfig{
			"g": {Project: "proj", Manifest: "manifest.json", Granularity: "node", Connection: "warehouse_pg"},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if eerr := expandDbtGroupsInFile(cmd, dir, out, cfg, false); eerr != nil {
		t.Fatalf("expandDbtGroupsInFile: %v", eerr)
	}
	data, _ := os.ReadFile(out)
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatal(uerr)
	}
	want := "python -m leoflow_runtime --dbt-profile warehouse_pg wh_profile && "
	for _, ts := range spec.Tasks {
		if !strings.HasPrefix(ts.Entrypoint, want) {
			t.Errorf("task %q entrypoint = %q, want prefix %q", ts.TaskID, ts.Entrypoint, want)
		}
	}
}

// TestExpandDbtGroupsInFile drives the merge step on the dag.json the parser would
// produce for a mixed DAG (operator + dbt_group placeholder): the placeholder is
// replaced by the namespaced dbt tasks and the downstream is rewired to the leaves.
func TestExpandDbtGroupsInFile(t *testing.T) {
	dir := t.TempDir()
	dagJSON := `{"schema_version":"1.0","dag_id":"sales","dag_version":"v1","image":"img","tasks":[
		{"task_id":"extract","type":"python","entrypoint":"d:extract"},
		{"task_id":"analytics","type":"dbt_group","depends_on":["extract"]},
		{"task_id":"notify","type":"python","entrypoint":"d:notify","depends_on":["analytics"]}
	]}`
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(dagJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if err != nil {
		t.Fatal(err)
	}
	// the manifest lives inside the project dir (manifest path is project-relative)
	if mkErr := os.MkdirAll(filepath.Join(dir, "analytics"), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	if werr := os.WriteFile(filepath.Join(dir, "analytics", "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID: "sales",
		DbtGroups: map[string]*domain.DbtConfig{
			"analytics": {Project: "analytics", Manifest: "manifest.json", Granularity: "node"},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if eerr := expandDbtGroupsInFile(cmd, dir, out, cfg, false); eerr != nil {
		t.Fatalf("expandDbtGroupsInFile: %v", eerr)
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatal(uerr)
	}
	byID := map[string]domain.TaskSpec{}
	for _, ts := range spec.Tasks {
		byID[ts.TaskID] = ts
	}
	if _, ok := byID["analytics"]; ok {
		t.Error("the dbt_group placeholder should be gone after expansion")
	}
	if got, ok := byID["analytics__raw"]; !ok {
		t.Error("missing namespaced task analytics__raw")
	} else if !reflect.DeepEqual(got.DependsOn, []string{"extract"}) {
		t.Errorf("group root deps = %v, want [extract]", got.DependsOn)
	}
	if !reflect.DeepEqual(byID["notify"].DependsOn, []string{"analytics__mart_a", "analytics__mart_b"}) {
		t.Errorf("notify deps = %v, want the two group leaves", byID["notify"].DependsOn)
	}
}

// TestCompileDbtWarnsOnFolderCollision locks the WIRING of #1114's warning. The
// dbt package tests prove the advisory is produced; without this, nothing proves
// it reaches a human — which is the failure mode this whole release has been
// about (a check that exists and is never run, a warning that exists and is
// never delivered).
func TestCompileDbtWarnsOnFolderCollision(t *testing.T) {
	dir := t.TempDir()
	yaml := `schema_version: "1.0"
dag_id: sales
dbt:
  project: .
  manifest: manifest.json
  granularity: folder
`
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	// A folder literally named "models" plus a model at the root of models/.
	manifest := `{"nodes":{
	  "model.shop.a":{"resource_type":"model","name":"a","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","models","a"]},
	  "model.shop.b":{"resource_type":"model","name":"b","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","b"]}}}`
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	o := compileOptions{output: filepath.Join(dir, "dag.json"), image: "reg/sales:v1", dagVersion: "v1"}
	if rerr := runCompile(cmd, dir, o); rerr != nil {
		t.Fatalf("runCompile: %v\n%s", rerr, errOut.String())
	}
	got := errOut.String()
	if !strings.Contains(got, "granularity=folder") || !strings.Contains(got, "ONE task") {
		t.Errorf("the collision advisory did not reach stderr; got:\n%s", got)
	}
	// stdout carries the artifact path that scripts read; a warning there would
	// corrupt it.
	if strings.Contains(out.String(), "granularity=folder") {
		t.Errorf("the advisory went to stdout: %s", out.String())
	}
}

// TestExpandDbtGroupsWarnsOnFolderCollision: the embedded dbt_group path renders
// through the same folder merge as a dbt-only project, so it must deliver the
// same advisory. It did not — Warn was wired on one of the two call sites while
// the CHANGELOG and the docs said "the compile warns", unqualified. A warning
// that exists and is never delivered is the defect, not the missing sentence.
func TestExpandDbtGroupsWarnsOnFolderCollision(t *testing.T) {
	dir := t.TempDir()
	dagJSON := `{"schema_version":"1.0","dag_id":"sales","dag_version":"v1","image":"img","tasks":[
		{"task_id":"analytics","type":"dbt_group"}
	]}`
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(dagJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if mkErr := os.MkdirAll(filepath.Join(dir, "analytics"), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	// A folder literally named "models" plus a model at the root of models/ —
	// the two collapse onto the same group key under granularity=folder.
	manifest := `{"nodes":{
	  "model.shop.a":{"resource_type":"model","name":"a","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","models","a"]},
	  "model.shop.b":{"resource_type":"model","name":"b","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","b"]}}}`
	if werr := os.WriteFile(filepath.Join(dir, "analytics", "manifest.json"), []byte(manifest), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID: "sales",
		DbtGroups: map[string]*domain.DbtConfig{
			"analytics": {Project: "analytics", Manifest: "manifest.json", Granularity: "folder"},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)

	if eerr := expandDbtGroupsInFile(cmd, dir, out, cfg, false); eerr != nil {
		t.Fatalf("expandDbtGroupsInFile: %v\n%s", eerr, stderr.String())
	}
	got := stderr.String()
	if !strings.Contains(got, "granularity=folder") || !strings.Contains(got, "ONE task") {
		t.Fatalf("the collision advisory did not reach stderr; got:\n%s", got)
	}
	// A dag.py DAG can embed several groups; an advisory that does not name one
	// leaves the author guessing which project to look at.
	if !strings.Contains(got, "analytics") {
		t.Errorf("the advisory does not name the group it came from:\n%s", got)
	}
	if strings.Contains(stdout.String(), "granularity=folder") {
		t.Errorf("the advisory went to stdout: %s", stdout.String())
	}
}

// TestCompileDbtCarriesDeclaredSecrets is the CLI-level half of #997, and it
// exists because the unit test in internal/dbt does not cover the wiring:
// removing the two lines that pass cfg.Connections/cfg.Variables into dbt.Meta
// left every CLI test green. The failure being guarded is not a missing JSON
// field — under ADR 0055 scoping the task pod receives NO secrets, so the DAG
// fails inside the task, far from the leoflow.yaml that declared them.
func TestCompileDbtCarriesDeclaredSecrets(t *testing.T) {
	dir := t.TempDir()
	yaml := `schema_version: "1.0"
dag_id: sales
connections: [warehouse]
variables: [env]
dbt:
  project: .
  manifest: manifest.json
  granularity: folder
`
	if err := os.WriteFile(filepath.Join(dir, "leoflow.yaml"), []byte(yaml), 0o600); err != nil {
		t.Fatal(err)
	}
	manifest, err := os.ReadFile(filepath.Join("..", "dbt", "testdata", "manifest_wide.json"))
	if err != nil {
		t.Fatalf("reading fixture manifest: %v", err)
	}
	if werr := os.WriteFile(filepath.Join(dir, "manifest.json"), manifest, 0o600); werr != nil {
		t.Fatal(werr)
	}

	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)

	o := compileOptions{output: filepath.Join(dir, "dag.json"), image: "reg/sales:v1", dagVersion: "v1"}
	if rerr := runCompile(cmd, dir, o); rerr != nil {
		t.Fatalf("runCompile (dbt path): %v\noutput:\n%s", rerr, out.String())
	}
	data, rerr := os.ReadFile(filepath.Join(dir, "dag.json"))
	if rerr != nil {
		t.Fatalf("dag.json not produced: %v", rerr)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatalf("parsing dag.json: %v", uerr)
	}
	if len(spec.Connections) != 1 || spec.Connections[0] != "warehouse" {
		t.Errorf("dag.json connections = %v, want [warehouse] — the task pod gets nothing without it", spec.Connections)
	}
	if len(spec.Variables) != 1 || spec.Variables[0] != "env" {
		t.Errorf("dag.json variables = %v, want [env]", spec.Variables)
	}
}

// TestExpandDbtGroupsKeepsTheDagsConnections: the embedded dbt_group path
// renders through the same code that stamps the managed connection onto every
// task — and a non-empty task-level list is what reaches the pod. So a dag.py
// DAG using dbt_group with a managed connection lost every connection it
// declared, exactly as a dbt-only project did.
//
// Fixing the dbt-only call site and leaving this one is the defect this release
// keeps finding: something wired on one of two paths, described as wired.
func TestExpandDbtGroupsKeepsTheDagsConnections(t *testing.T) {
	dir := t.TempDir()
	// The parser emits the top-level connections: into dag.json; this is that file.
	dagJSON := `{"schema_version":"1.0","dag_id":"sales","dag_version":"v1","image":"img",
		"connections":["warehouse_pg","reporting"],
		"tasks":[{"task_id":"analytics","type":"dbt_group"}]}`
	out := filepath.Join(dir, "dag.json")
	if err := os.WriteFile(out, []byte(dagJSON), 0o600); err != nil {
		t.Fatal(err)
	}
	if mkErr := os.MkdirAll(filepath.Join(dir, "analytics"), 0o750); mkErr != nil {
		t.Fatal(mkErr)
	}
	manifest := `{"nodes":{"model.shop.a":{"resource_type":"model","name":"a","depends_on":{"nodes":[]},"config":{"materialized":"table"},"fqn":["shop","a"]}}}`
	if werr := os.WriteFile(filepath.Join(dir, "analytics", "manifest.json"), []byte(manifest), 0o600); werr != nil {
		t.Fatal(werr)
	}
	// A managed connection makes the profile name come from dbt_project.yml.
	if werr := os.WriteFile(filepath.Join(dir, "analytics", "dbt_project.yml"), []byte("name: shop\nprofile: shop\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := &domain.LeoflowConfig{
		DagID:       "sales",
		Connections: []string{"warehouse_pg", "reporting"},
		DbtGroups: map[string]*domain.DbtConfig{
			"analytics": {Project: "analytics", Manifest: "manifest.json", Granularity: "node", Connection: "managed_wh"},
		},
	}
	cmd := &cobra.Command{}
	cmd.SetContext(context.Background())

	if eerr := expandDbtGroupsInFile(cmd, dir, out, cfg, false); eerr != nil {
		t.Fatalf("expandDbtGroupsInFile: %v", eerr)
	}
	data, rerr := os.ReadFile(out)
	if rerr != nil {
		t.Fatal(rerr)
	}
	var spec domain.DAGSpec
	if uerr := json.Unmarshal(data, &spec); uerr != nil {
		t.Fatal(uerr)
	}
	rendered := 0
	for _, task := range spec.Tasks {
		if task.Type == domain.TaskTypeDbtGroup {
			t.Fatalf("the placeholder survived expansion: %+v", task)
		}
		rendered++
		if !slices.Contains(task.Connections, "managed_wh") {
			t.Errorf("task %s: managed connection missing from %v", task.TaskID, task.Connections)
		}
		for _, want := range []string{"warehouse_pg", "reporting"} {
			if !slices.Contains(task.Connections, want) {
				t.Errorf("task %s: the DAG declared %q and the task list shadows it: %v", task.TaskID, want, task.Connections)
			}
		}
	}
	if rendered == 0 {
		t.Fatal("no dbt task was rendered; the assertions above were vacuous")
	}
}
