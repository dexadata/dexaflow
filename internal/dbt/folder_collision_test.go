package dbt

import (
	"encoding/json"
	"strings"
	"testing"
)

// manifestOf builds a minimal manifest from id -> fqn, all nodes independent.
func manifestOf(nodes map[string][]string) []byte {
	m := map[string]any{"nodes": map[string]any{}}
	nm := m["nodes"].(map[string]any)
	for id, fqn := range nodes {
		nm[id] = map[string]any{
			"resource_type": "model",
			"name":          fqn[len(fqn)-1],
			"depends_on":    map[string]any{"nodes": []string{}},
			"config":        map[string]any{"materialized": "table"},
			"fqn":           fqn,
		}
	}
	b, _ := json.Marshal(m)
	return b
}

// TestFolderGroupCollisionIsAnnouncedLoudly covers the measured half of #1114
// that changes behavior rather than wording.
//
// `granularity: folder` keys a group on the first folder segment (`fqn[1]`) and
// falls back to the resource-type plural for a node with no folder at all. A
// project with a folder literally named `models` AND a model at the root of
// `models/` therefore produces the key `models` twice — and the two sets merge
// into ONE task with their dependencies combined.
//
// dbt still orders the models inside that task, so the data is not wrong. What
// is lost is Dexaflow-level parallelism and per-model failure isolation, silently
// and with no way to notice: the author sees one task named after a folder and
// no indication that a root-level model was folded into it.
func TestFolderGroupCollisionIsAnnouncedLoudly(t *testing.T) {
	collide := manifestOf(map[string][]string{
		"model.shop.a": {"shop", "models", "a"}, // inside a folder named "models"
		"model.shop.b": {"shop", "b"},           // at the root of models/
	})

	t.Run("the collision is reported, naming the key and both sides", func(t *testing.T) {
		var warnings []string
		tasks, err := Render(collide, Options{
			Granularity: GranularityFolder,
			Warn:        func(msg string) { warnings = append(warnings, msg) },
		})
		if err != nil {
			t.Fatalf("Render: %v", err)
		}
		if len(tasks) != 1 {
			t.Fatalf("expected the merge to still happen (1 task), got %d", len(tasks))
		}
		if len(warnings) == 0 {
			t.Fatal("the merge happened silently; it must be announced")
		}
		joined := strings.Join(warnings, "\n")
		for _, want := range []string{"models", "a", "b", "granularity"} {
			if !strings.Contains(joined, want) {
				t.Errorf("the warning must name the group, both members and the knob; missing %q in:\n%s", want, joined)
			}
		}
	})

	t.Run("a project without the collision says nothing", func(t *testing.T) {
		// A warning that fires on ordinary projects is a warning nobody reads.
		clean := manifestOf(map[string][]string{
			"model.shop.x": {"shop", "staging", "x"},
			"model.shop.y": {"shop", "marts", "y"},
		})
		var warnings []string
		if _, err := Render(clean, Options{
			Granularity: GranularityFolder,
			Warn:        func(msg string) { warnings = append(warnings, msg) },
		}); err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 0 {
			t.Errorf("no collision here; got %v", warnings)
		}
	})

	t.Run("two models in the same real folder are not a collision", func(t *testing.T) {
		// Same folder means the same group by design — that is what the
		// granularity is for, and calling it a collision would cry wolf on
		// every project.
		same := manifestOf(map[string][]string{
			"model.shop.x": {"shop", "staging", "x"},
			"model.shop.y": {"shop", "staging", "y"},
		})
		var warnings []string
		if _, err := Render(same, Options{
			Granularity: GranularityFolder,
			Warn:        func(msg string) { warnings = append(warnings, msg) },
		}); err != nil {
			t.Fatal(err)
		}
		if len(warnings) != 0 {
			t.Errorf("same folder is the intended grouping; got %v", warnings)
		}
	})

	t.Run("a nil Warn is safe", func(t *testing.T) {
		// Every existing caller passes no Warn.
		if _, err := Render(collide, Options{Granularity: GranularityFolder}); err != nil {
			t.Fatalf("Render with no Warn: %v", err)
		}
	})

	t.Run("other granularities cannot collide this way", func(t *testing.T) {
		for _, g := range []Granularity{GranularityNode, GranularityLevel} {
			var warnings []string
			if _, err := Render(collide, Options{Granularity: g, Warn: func(m string) { warnings = append(warnings, m) }}); err != nil {
				t.Fatal(err)
			}
			if len(warnings) != 0 {
				t.Errorf("granularity %s reported a folder collision: %v", g, warnings)
			}
		}
	})
}
