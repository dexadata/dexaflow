package dbt

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// Ephemeral models are inlined by dbt (no table), so they must not become tasks;
// a downstream task is re-parented through them onto their executable ancestors
// (chain raw -> stg[ephemeral] -> mart yields tasks {raw, mart} with mart<-raw).
func TestRenderSkipsEphemeralWithReparenting(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_ephemeral.json"), Options{})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	byID := tasksByID(tasks)
	if _, ok := byID["stg"]; ok {
		t.Error("ephemeral model stg must not become a task")
	}
	if len(tasks) != 2 {
		t.Fatalf("got %d tasks, want 2 (raw, mart): %v", len(tasks), ids(byID))
	}
	mart, ok := byID["mart"]
	if !ok {
		t.Fatal("mart task missing")
	}
	if !reflect.DeepEqual(mart.DependsOn, []string{"raw"}) {
		t.Errorf("mart depends_on = %v, want [raw] (re-parented through ephemeral stg)", mart.DependsOn)
	}
}

// Two dbt nodes that share a name (e.g. the same model name across installed
// packages) would silently produce duplicate task_ids. That is rejected loudly.
func TestRenderRejectsDuplicateName(t *testing.T) {
	_, err := Render(loadManifest(t, "manifest_collision.json"), Options{})
	if err == nil {
		t.Fatal("expected duplicate-name rejection, got nil")
	}
	if !strings.Contains(err.Error(), "stg_orders") {
		t.Errorf("error %q should name the colliding node", err)
	}
}

// A manifest with no executable nodes is a loud error, not a silent empty DAG.
func TestRenderEmptyManifestErrors(t *testing.T) {
	for _, in := range []string{`{}`, `{"nodes":{}}`} {
		if _, err := Render([]byte(in), Options{}); err == nil {
			t.Errorf("Render(%s) = nil error, want an error for no executable nodes", in)
		}
	}
}

// With a managed connection, each task's dbt command is prefixed with the runtime
// step that generates profiles.yml from the connection (ADR 0043 #2).
func TestRenderWrapsManagedConnection(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode,
		Connection:  "warehouse_pg",
		Profile:     "transform",
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	got := tasksByID(tasks)["stg"].Entrypoint
	want := "python -m leoflow_runtime --dbt-profile warehouse_pg transform && dbt run --select stg"
	if got != want {
		t.Errorf("entrypoint = %q, want %q", got, want)
	}
	// The managed connection the profile step reads must also be DECLARED on every
	// task, or the agent never injects AIRFLOW_CONN_<conn> under enforce scoping /
	// an external backend and the profile step fails "not delivered" (#10).
	for _, task := range tasks {
		if !contains(task.Connections, "warehouse_pg") {
			t.Errorf("task %q does not declare the managed connection warehouse_pg: %v", task.TaskID, task.Connections)
		}
	}
}

// Without a managed connection, tasks declare none (the local/duckdb and
// baked-profile paths must not invent a declaration).
func TestRenderNoConnectionDeclaresNone(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode, Profile: "transform", Local: true,
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	for _, task := range tasks {
		if len(task.Connections) != 0 {
			t.Errorf("task %q declared connections with no managed connection: %v", task.TaskID, task.Connections)
		}
	}
}

func contains(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

// TestRenderDefaultDuckdbGatedOnLocal: with no connection, a local build prefixes the
// default-duckdb step (L4 zero-config); a non-local build — including one whose Local
// the compiler gated off because the project ships its own profiles.yml — adds NO
// prefix, so a user-configured warehouse is never overridden (regression guard).
func TestRenderDefaultDuckdbGatedOnLocal(t *testing.T) {
	local, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode, Profile: "transform", Local: true,
	})
	if err != nil {
		t.Fatalf("Render(local) error: %v", err)
	}
	got := tasksByID(local)["stg"].Entrypoint
	want := "python -m leoflow_runtime --dbt-default-duckdb transform leoflow_local.duckdb && dbt run --select stg"
	if got != want {
		t.Errorf("local entrypoint = %q, want %q", got, want)
	}

	nolocal, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode, Profile: "transform", Local: false,
	})
	if err != nil {
		t.Fatalf("Render(!local) error: %v", err)
	}
	if got := tasksByID(nolocal)["stg"].Entrypoint; got != "dbt run --select stg" {
		t.Errorf("non-local entrypoint = %q, want no default-duckdb prefix", got)
	}
}

// A subdir dbt project (ProjectDir != ".") scopes each command with --project-dir
// so dbt finds dbt_project.yml regardless of the pod's working dir (#401).
func TestRenderScopesSubdirProject(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode, ProjectDir: "transform",
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	got := tasksByID(tasks)["stg"].Entrypoint
	want := "dbt run --select stg --project-dir transform"
	if got != want {
		t.Errorf("entrypoint = %q, want %q", got, want)
	}
}

// A root-level project (ProjectDir "." or empty) adds no --project-dir.
func TestRenderRootProjectNoProjectDir(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_chain.json"), Options{ProjectDir: "."})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if got := tasksByID(tasks)["stg"].Entrypoint; got != "dbt run --select stg" {
		t.Errorf("entrypoint = %q, want no --project-dir", got)
	}
}

// A configured dbt target schema is passed to the runtime profile step.
func TestRenderWrapsManagedConnectionWithSchema(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_chain.json"), Options{
		Granularity: GranularityNode, Connection: "wh", Profile: "p", Schema: "marts",
	})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	got := tasksByID(tasks)["stg"].Entrypoint
	want := "python -m leoflow_runtime --dbt-profile wh p marts && dbt run --select stg"
	if got != want {
		t.Errorf("entrypoint = %q, want %q", got, want)
	}
}

// TestRenderNodeGranularity feeds a real (trimmed) dbt manifest.json and asserts
// the renderer emits one flat bash task per executable node, with the scoped dbt
// command and the dependency edges carried over from depends_on.nodes.
func TestRenderNodeGranularity(t *testing.T) {
	manifest, err := os.ReadFile("testdata/manifest_chain.json")
	if err != nil {
		t.Fatalf("reading testdata: %v", err)
	}

	tasks, err := Render(manifest, Options{Granularity: GranularityNode})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	if len(tasks) != 4 {
		t.Fatalf("Render() returned %d tasks, want 4", len(tasks))
	}

	byID := make(map[string]domain.TaskSpec, len(tasks))
	for _, ts := range tasks {
		byID[ts.TaskID] = ts
	}

	want := map[string]struct {
		entrypoint string
		deps       []string
	}{
		"raw":            {"dbt seed --select raw", nil},
		"stg":            {"dbt run --select stg", []string{"raw"}},
		"mart":           {"dbt run --select mart", []string{"stg"}},
		"unique_mart_id": {"dbt test --select unique_mart_id", []string{"mart"}},
	}
	for id, w := range want {
		got, ok := byID[id]
		if !ok {
			t.Errorf("task %q missing from render output", id)
			continue
		}
		if got.Type != domain.TaskTypeBash {
			t.Errorf("task %q type = %q, want %q", id, got.Type, domain.TaskTypeBash)
		}
		if got.Entrypoint != w.entrypoint {
			t.Errorf("task %q entrypoint = %q, want %q", id, got.Entrypoint, w.entrypoint)
		}
		if !reflect.DeepEqual(got.DependsOn, w.deps) {
			t.Errorf("task %q depends_on = %v, want %v", id, got.DependsOn, w.deps)
		}
	}
}

// The managed-connection declaration must reach grouped-granularity tasks too
// (folder/level), not just per-node — they share decorateCommands, but lock it
// so the grouped path can't regress (#10, review nit).
func TestRenderGroupedDeclaresManagedConnection(t *testing.T) {
	for _, gran := range []Granularity{GranularityFolder, GranularityLevel} {
		tasks, err := Render(loadManifest(t, "manifest_wide.json"), Options{
			Granularity: gran, Connection: "warehouse_pg", Profile: "transform",
		})
		if err != nil {
			t.Fatalf("Render(%v) error: %v", gran, err)
		}
		if len(tasks) == 0 {
			t.Fatalf("Render(%v) emitted no tasks", gran)
		}
		for _, task := range tasks {
			if !contains(task.Connections, "warehouse_pg") {
				t.Errorf("gran=%v task %q does not declare warehouse_pg: %v", gran, task.TaskID, task.Connections)
			}
		}
	}
}

// TestDecorateCommandsHonorsAProjectBakedProfilesYml pins #994.
//
// The base image sets DBT_PROFILES_DIR=/tmp/leoflow/dbt so dbt's writes land on
// an ephemeral dir instead of the read-only project (#852). The side effect is
// that dbt never looks inside the project — so a dbt task with no managed
// connection, whose project ships its own profiles.yml, failed in the pod:
//
//	Invalid value for '--profiles-dir': Path '/tmp/leoflow/dbt' does not exist
//	(and once created) Could not find profile named 'transform'
//
// while the default branch's comment claimed it was using "the image's baked
// profiles.yml". Reading a profiles.yml out of the project is safe against the
// read-only posture — only dbt's writes needed redirecting, and DBT_TARGET_PATH
// and DBT_LOG_PATH still carry them to /tmp.
func TestDecorateCommandsHonorsAProjectBakedProfilesYml(t *testing.T) {
	for _, c := range []struct {
		name string
		opts Options
		want string
		deny string
	}{
		{
			name: "no connection, project ships profiles.yml",
			opts: Options{ProjectDir: "analytics", ProfilesDir: "analytics"},
			want: "--profiles-dir analytics",
		},
		{
			name: "project is the DAG dir",
			opts: Options{ProfilesDir: "."},
			want: "--profiles-dir .",
		},
		{
			name: "no profiles.yml — nothing is added, DBT_PROFILES_DIR still governs",
			opts: Options{ProjectDir: "analytics"},
			deny: "--profiles-dir",
		},
		{
			// A managed connection writes its generated profile to
			// DBT_PROFILES_DIR; overriding that would send dbt to the project's
			// checked-in file instead of the operator's credentials.
			name: "a managed connection wins over a baked profiles.yml",
			opts: Options{ProjectDir: "analytics", ProfilesDir: "analytics", Connection: "warehouse", Profile: "analytics"},
			deny: "--profiles-dir",
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			tasks := []domain.TaskSpec{{TaskID: "mart", Entrypoint: "dbt run --select mart"}}
			decorateCommands(tasks, c.opts)
			got := tasks[0].Entrypoint
			if c.want != "" && !strings.Contains(got, c.want) {
				t.Errorf("entrypoint %q does not contain %q", got, c.want)
			}
			if c.deny != "" && strings.Contains(got, c.deny) {
				t.Errorf("entrypoint %q must not contain %q", got, c.deny)
			}
		})
	}
}
