package dbt

import (
	"fmt"
	"reflect"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// EmbedGroup namespaces a rendered dbt group's tasks and wires its external edges:
// upstream task_ids become dependencies of the group's roots, and it returns the
// namespaced leaf ids so the caller can wire them into downstream tasks.
func TestEmbedGroupNamespacesAndWires(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "raw", Type: domain.TaskTypeBash, Entrypoint: "dbt seed --select raw"},
		{TaskID: "stg", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select stg", DependsOn: []string{"raw"}},
		{TaskID: "mart", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select mart", DependsOn: []string{"stg"}},
	}

	embedded, leaves, err := EmbedGroup("transform", tasks, []string{"extract"})
	if err != nil {
		t.Fatalf("EmbedGroup() error: %v", err)
	}
	byID := tasksByID(embedded)

	for _, id := range []string{"transform__raw", "transform__stg", "transform__mart"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing namespaced task %q (have %v)", id, ids(byID))
		}
	}
	// a root inherits the external upstream
	if !reflect.DeepEqual(byID["transform__raw"].DependsOn, []string{"extract"}) {
		t.Errorf("root deps = %v, want [extract]", byID["transform__raw"].DependsOn)
	}
	// internal edges are namespaced
	if !reflect.DeepEqual(byID["transform__stg"].DependsOn, []string{"transform__raw"}) {
		t.Errorf("stg deps = %v, want [transform__raw]", byID["transform__stg"].DependsOn)
	}
	// the leaf is reported for downstream wiring
	if !reflect.DeepEqual(leaves, []string{"transform__mart"}) {
		t.Errorf("leaves = %v, want [transform__mart]", leaves)
	}
}

// An empty group name is a loud error (a group must be identifiable).
func TestEmbedGroupRejectsEmptyName(t *testing.T) {
	if _, _, err := EmbedGroup("", []domain.TaskSpec{{TaskID: "x"}}, nil); err == nil {
		t.Fatal("expected an error for an empty group name")
	}
}

// ExpandGroups replaces a dbt_group placeholder with the rendered+embedded dbt
// tasks and rewires downstream dependents from the placeholder onto the leaves.
func TestExpandGroups(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "transform", Type: domain.TaskTypeDbtGroup, DependsOn: []string{"extract"}},
		{TaskID: "notify", Type: domain.TaskTypePython, DependsOn: []string{"transform"}},
	}
	render := func(group string) ([]domain.TaskSpec, error) {
		if group != "transform" {
			t.Fatalf("unexpected group %q", group)
		}
		return []domain.TaskSpec{
			{TaskID: "raw", Type: domain.TaskTypeBash, Entrypoint: "dbt seed --select raw"},
			{TaskID: "stg", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select stg", DependsOn: []string{"raw"}},
			{TaskID: "mart", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select mart", DependsOn: []string{"stg"}},
		}, nil
	}

	out, err := ExpandGroups(tasks, render)
	if err != nil {
		t.Fatalf("ExpandGroups() error: %v", err)
	}
	byID := tasksByID(out)

	if _, ok := byID["transform"]; ok {
		t.Error("the dbt_group placeholder should be gone after expansion")
	}
	for _, id := range []string{"extract", "notify", "transform__raw", "transform__stg", "transform__mart"} {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing task %q (have %v)", id, ids(byID))
		}
	}
	if !reflect.DeepEqual(byID["transform__raw"].DependsOn, []string{"extract"}) {
		t.Errorf("group root deps = %v, want [extract]", byID["transform__raw"].DependsOn)
	}
	if !reflect.DeepEqual(byID["notify"].DependsOn, []string{"transform__mart"}) {
		t.Errorf("downstream deps = %v, want [transform__mart] (rewired to the leaf)", byID["notify"].DependsOn)
	}
}

// A namespaced group task that collides with an existing task_id is a loud error.
func TestExpandGroupsRejectsCollision(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "g", Type: domain.TaskTypeDbtGroup},
		{TaskID: "g__raw", Type: domain.TaskTypePython}, // collides with the group's namespaced raw
	}
	render := func(string) ([]domain.TaskSpec, error) {
		return []domain.TaskSpec{{TaskID: "raw", Type: domain.TaskTypeBash}}, nil
	}
	if _, err := ExpandGroups(tasks, render); err == nil {
		t.Fatal("expected a task_id collision to be rejected")
	}
}

// An error from render (e.g. no matching group config) is propagated loudly.
func TestExpandGroupsRenderError(t *testing.T) {
	tasks := []domain.TaskSpec{{TaskID: "g", Type: domain.TaskTypeDbtGroup}}
	_, err := ExpandGroups(tasks, func(string) ([]domain.TaskSpec, error) {
		return nil, fmt.Errorf("no such group")
	})
	if err == nil {
		t.Fatal("expected the render error to propagate")
	}
}

// TestExpandGroupsTwoDomainsNoConflict: two dbt projects for different business
// domains (ADR 0044) with IDENTICALLY-named models (both ship raw + stg) expand
// side by side WITHOUT colliding — each group namespaces its nodes under its own
// name (sales__stg vs marketing__stg), keeps its own internal wiring, and both
// roots inherit the shared upstream. This is the multi-project-by-domain guarantee.
func TestExpandGroupsTwoDomainsNoConflict(t *testing.T) {
	tasks := []domain.TaskSpec{
		{TaskID: "extract", Type: domain.TaskTypePython},
		{TaskID: "sales", Type: domain.TaskTypeDbtGroup, DependsOn: []string{"extract"}},
		{TaskID: "marketing", Type: domain.TaskTypeDbtGroup, DependsOn: []string{"extract"}},
	}
	// Both domains ship the same model names — the cross-domain collision scenario.
	render := func(string) ([]domain.TaskSpec, error) {
		return []domain.TaskSpec{
			{TaskID: "raw", Type: domain.TaskTypeBash, Entrypoint: "dbt seed --select raw"},
			{TaskID: "stg", Type: domain.TaskTypeBash, Entrypoint: "dbt run --select stg", DependsOn: []string{"raw"}},
		}, nil
	}

	out, err := ExpandGroups(tasks, render)
	if err != nil {
		t.Fatalf("ExpandGroups() error: %v", err)
	}
	byID := tasksByID(out)

	// Both domains expanded, namespaced, with no collision and nothing collapsed.
	want := []string{"extract", "sales__raw", "sales__stg", "marketing__raw", "marketing__stg"}
	for _, id := range want {
		if _, ok := byID[id]; !ok {
			t.Errorf("missing task %q (have %v)", id, ids(byID))
		}
	}
	if len(out) != len(want) {
		t.Errorf("got %d tasks %v, want %d — same-named models across domains must not collapse", len(out), ids(byID), len(want))
	}
	// Each domain keeps its own internal edge (no cross-domain wiring).
	if !reflect.DeepEqual(byID["sales__stg"].DependsOn, []string{"sales__raw"}) {
		t.Errorf("sales__stg deps = %v, want [sales__raw]", byID["sales__stg"].DependsOn)
	}
	if !reflect.DeepEqual(byID["marketing__stg"].DependsOn, []string{"marketing__raw"}) {
		t.Errorf("marketing__stg deps = %v, want [marketing__raw]", byID["marketing__stg"].DependsOn)
	}
	// Each domain's root inherits the shared upstream.
	if !reflect.DeepEqual(byID["sales__raw"].DependsOn, []string{"extract"}) {
		t.Errorf("sales__raw deps = %v, want [extract]", byID["sales__raw"].DependsOn)
	}
	if !reflect.DeepEqual(byID["marketing__raw"].DependsOn, []string{"extract"}) {
		t.Errorf("marketing__raw deps = %v, want [extract]", byID["marketing__raw"].DependsOn)
	}
}
