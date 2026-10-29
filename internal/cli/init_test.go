package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestInitCommandPrintsNextStep covers #D6 from the Lite dogfood audit (#212):
// after scaffolding a project, the user has no idea what command to run next.
// The success message must surface at least one concrete next-step hint
// (typically `dexaflow lite <path>` or `dexaflow validate <path>`) so the
// onboarding loop terminates at a useful instruction rather than dead air.
func TestInitCommandPrintsNextStep(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "myproj")
	cmd := newInitCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{target})
	if err := cmd.ExecuteContext(testCtx(nil)); err != nil {
		t.Fatalf("init: %v", err)
	}

	got := stdout.String()
	if !strings.Contains(got, "Initialized Dexaflow project") {
		t.Errorf("missing success line; got: %q", got)
	}
	if !strings.Contains(got, "dexaflow lite") && !strings.Contains(got, "dexaflow validate") {
		t.Errorf("D6: expected a next-step suggestion (dexaflow lite / dexaflow validate); got: %q", got)
	}
}

// TestScaffoldedFilesHaveNoEmDash locks the text `init` and `new` write into a
// user's project: the dag.py and dexaflow.yaml they scaffold carry no em dash,
// the house style of every text the project ships (#1709).
func TestScaffoldedFilesHaveNoEmDash(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "orders_report")
	if _, err := scaffoldProject(dir); err != nil {
		t.Fatalf("scaffoldProject: %v", err)
	}
	for _, name := range []string{"dag.py", "dexaflow.yaml"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if strings.ContainsRune(string(b), '—') {
			t.Errorf("%s has an em dash:\n%s", name, b)
		}
	}
}

// TestInitDbtScaffoldsTheHybridShape covers `dexaflow init --dbt` (#1002): one
// flag, one shape, the hybrid one the docs teach. A dag.py with Python tasks
// around dbt_group("transform"), a dexaflow.yaml that declares the group and
// installs dbt itself, and a duckdb project with no profiles.yml, so Lite runs
// it with no warehouse to set up.
func TestInitDbtScaffoldsTheHybridShape(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "orders")
	cmd := newInitCommand()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"--dbt", target})
	if err := cmd.ExecuteContext(testCtx(nil)); err != nil {
		t.Fatalf("init --dbt: %v", err)
	}

	cfg, err := loadProjectConfig(target)
	if err != nil {
		t.Fatalf("the scaffolded dexaflow.yaml does not load: %v", err)
	}
	if cfg.DagID != "orders" {
		t.Errorf("dag_id = %q, want orders", cfg.DagID)
	}
	if cfg.Dbt != nil {
		t.Error("the scaffold must use dbt_groups (hybrid), not a top-level dbt: block")
	}
	group, ok := cfg.DbtGroups["transform"]
	if !ok || group.Project != "transform" {
		t.Fatalf("dbt_groups.transform = %+v, want project transform", group)
	}
	// Without the adapter in dependencies the image has no dbt in it: the
	// failure the docs snippets had (#1002).
	if !slices.ContainsFunc(cfg.Dependencies, func(d string) bool { return strings.HasPrefix(d, "dbt-duckdb") }) {
		t.Errorf("dependencies %v do not install dbt-duckdb", cfg.Dependencies)
	}

	dag, err := os.ReadFile(filepath.Join(target, "dag.py"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"from dexaflow import dbt_group", `dbt_group("transform")`, "@task", `DAG("orders"`} {
		if !strings.Contains(string(dag), want) {
			t.Errorf("dag.py does not contain %q:\n%s", want, dag)
		}
	}
	for _, name := range []string{"transform/dbt_project.yml", "transform/seeds/orders.csv",
		"transform/models/stg_orders.sql", "transform/models/revenue_by_region.sql"} {
		if _, serr := os.Stat(filepath.Join(target, filepath.FromSlash(name))); serr != nil {
			t.Errorf("missing %s: %v", name, serr)
		}
	}
	// A profiles.yml in the project would switch off the zero-config duckdb
	// profile Lite writes, and send the first run looking for a warehouse.
	if _, serr := os.Stat(filepath.Join(target, "transform", "profiles.yml")); serr == nil {
		t.Error("the scaffold must not ship a profiles.yml")
	}
	if !strings.Contains(stdout.String(), "dexaflow lite") {
		t.Errorf("no next step printed: %q", stdout.String())
	}
}

// TestInitWithoutDbtWritesNoDbtProject keeps the default scaffold unchanged.
func TestInitWithoutDbtWritesNoDbtProject(t *testing.T) {
	t.Parallel()
	dir := filepath.Join(t.TempDir(), "plain")
	if _, err := scaffoldProject(dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(dir, "transform")); err == nil {
		t.Error("the default scaffold wrote a dbt project")
	}
}

// TestDbtExampleMatchesTheInitScaffold keeps examples/dbt_hybrid and
// `dexaflow init --dbt` the same project (#1003): the example is what the
// scaffold writes, under its own dag_id, so neither can drift into a shape the
// other does not teach.
func TestDbtExampleMatchesTheInitScaffold(t *testing.T) {
	t.Parallel()
	example := filepath.Join("..", "..", "examples", "dbt_hybrid")
	dir := filepath.Join(t.TempDir(), "dbt_hybrid")
	if _, err := scaffoldDbtProject(dir); err != nil {
		t.Fatalf("scaffoldDbtProject: %v", err)
	}
	for name := range dbtScaffoldFiles("dbt_hybrid") {
		want, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(example, filepath.FromSlash(name)))
		if err != nil {
			t.Errorf("examples/dbt_hybrid has no %s: %v", name, err)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("examples/dbt_hybrid/%s differs from what init --dbt writes; regenerate it with `dexaflow init --dbt`", name)
		}
	}
}
