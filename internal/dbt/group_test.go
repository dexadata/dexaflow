package dbt

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func loadManifest(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	return b
}

func tasksByID(tasks []domain.TaskSpec) map[string]domain.TaskSpec {
	m := make(map[string]domain.TaskSpec, len(tasks))
	for _, ts := range tasks {
		m[ts.TaskID] = ts
	}
	return m
}

func ids(m map[string]domain.TaskSpec) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// assertGroups checks each expected group's bash command and quotient deps.
func assertGroups(t *testing.T, tasks []domain.TaskSpec, want map[string]struct {
	entry string
	deps  []string
}) {
	t.Helper()
	byID := tasksByID(tasks)
	if len(tasks) != len(want) {
		t.Fatalf("got %d tasks %v, want %d", len(tasks), ids(byID), len(want))
	}
	for id, w := range want {
		got, ok := byID[id]
		if !ok {
			t.Errorf("group %q missing (have %v)", id, ids(byID))
			continue
		}
		if got.Type != domain.TaskTypeBash {
			t.Errorf("group %q type = %q, want %q", id, got.Type, domain.TaskTypeBash)
		}
		if got.Entrypoint != w.entry {
			t.Errorf("group %q entrypoint = %q, want %q", id, got.Entrypoint, w.entry)
		}
		if !reflect.DeepEqual(got.DependsOn, w.deps) {
			t.Errorf("group %q depends_on = %v, want %v", id, got.DependsOn, w.deps)
		}
	}
}

// Folder grouping collapses the wide project (raw -> {stg_a,stg_b} ->
// {mart_a,mart_b}) into one task per folder; the quotient is acyclic.
func TestRenderFolderGrouping(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_wide.json"), Options{Granularity: GranularityFolder})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	assertGroups(t, tasks, map[string]struct {
		entry string
		deps  []string
	}{
		"seeds":   {"dbt build --select raw", nil},
		"staging": {"dbt build --select stg_a stg_b", []string{"seeds"}},
		"marts":   {"dbt build --select mart_a mart_b", []string{"staging"}},
	})
}

// Level grouping is the topological wave: one task per depth.
func TestRenderLevelGrouping(t *testing.T) {
	tasks, err := Render(loadManifest(t, "manifest_wide.json"), Options{Granularity: GranularityLevel})
	if err != nil {
		t.Fatalf("Render() error: %v", err)
	}
	assertGroups(t, tasks, map[string]struct {
		entry string
		deps  []string
	}{
		"level_0": {"dbt build --select raw", nil},
		"level_1": {"dbt build --select stg_a stg_b", []string{"level_0"}},
		"level_2": {"dbt build --select mart_a mart_b", []string{"level_1"}},
	})
}

// A clean node-DAG (raw -> stg_a -> mart_a -> stg_b) becomes a cross-group cycle
// when grouped by folder (staging <-> marts). The renderer must reject it loudly,
// naming the offending groups.
func TestRenderFolderCycleRejected(t *testing.T) {
	_, err := Render(loadManifest(t, "manifest_cycle.json"), Options{Granularity: GranularityFolder})
	if err == nil {
		t.Fatal("expected a cross-group cycle rejection, got nil")
	}
	if !strings.Contains(err.Error(), "cycle") {
		t.Errorf("error %q should mention the cycle", err)
	}
	for _, g := range []string{"staging", "marts"} {
		if !strings.Contains(err.Error(), g) {
			t.Errorf("error %q should name the group %q in the cycle", err, g)
		}
	}
}

// Level grouping is acyclic by construction, so the same trap project is accepted.
func TestRenderLevelAcyclicOnCycleProject(t *testing.T) {
	if _, err := Render(loadManifest(t, "manifest_cycle.json"), Options{Granularity: GranularityLevel}); err != nil {
		t.Errorf("level granularity is construction-safe, got error: %v", err)
	}
}
