package domain

import (
	"encoding/json"
	"testing"
)

// wiringReminder is the single instruction repeated by every failing assertion
// in this file: it names everything that must change together when the dormant
// warm-pool seam is finally exposed to authors, so a partial wiring can never
// pass by fixing only one half.
const wiringReminder = "the min_idle_workers seam is DORMANT and must stay unwired: " +
	"absent from the authoring schema (leoflow-yaml-schema.json, " +
	"additionalProperties:false), never emitted by the parser, so " +
	"spec.MinIdleWorkers is always 0. To expose it later you must do ALL of: " +
	"add min_idle_workers to the authoring schema AND emit it from the parser; " +
	"keep the staging exclusion (staging.enabled => dedicated pod, never warm); " +
	"and lead the operator docs with the safe default (0 = scale-to-zero)."

// TestMinIdleWorkersAbsentFromCompiledArtifactIsZero locks the value half of
// the dormant contract: a compiled artifact as the parser emits it (no
// min_idle_workers key — the parser never writes one) unmarshals to a DAGSpec
// whose MinIdleWorkers is 0. The downstream (EffectiveMinIdle, scheduler store)
// is intentionally pre-wired, so this zero is what keeps warm pools inert until
// the seam is deliberately exposed.
func TestMinIdleWorkersAbsentFromCompiledArtifactIsZero(t *testing.T) {
	// A representative parser-emitted dag.json: the parser never writes the
	// min_idle_workers key, so it is simply absent here.
	const compiled = `{
		"schema_version": "1.0",
		"dag_id": "simple_linear",
		"dag_version": "test:v1",
		"image": "test:v1",
		"tasks": [{"task_id": "extract", "type": "python", "entrypoint": "simple_linear:extract"}]
	}`

	var spec DAGSpec
	if err := json.Unmarshal([]byte(compiled), &spec); err != nil {
		t.Fatalf("unmarshal compiled artifact: %v", err)
	}
	if spec.MinIdleWorkers != 0 {
		t.Fatalf("MinIdleWorkers = %d, want 0; %s", spec.MinIdleWorkers, wiringReminder)
	}
}

// TestMinIdleWorkersRejectedFromAuthoringSchema locks the author-entry-point
// half of the dormant contract: an author who writes min_idle_workers into
// dexaflow.yaml is rejected by schema validation (additionalProperties:false),
// rather than having it silently accepted-and-dropped. This is what makes the
// seam honest — there is no author knob today, and an attempt to invent one
// fails loudly at compile instead of looking like it worked.
func TestMinIdleWorkersRejectedFromAuthoringSchema(t *testing.T) {
	s, err := schemas()
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	// A raw dexaflow.yaml an author might write to request warmth. The struct
	// cannot even hold this key, so validate the raw instance against the
	// authoring schema directly.
	inst := map[string]any{
		"dag_id":           "sales",
		"min_idle_workers": 1,
	}
	if verr := validateAgainst(s.leoflow, inst); verr == nil {
		t.Fatalf("authoring schema accepted min_idle_workers; want rejection. %s", wiringReminder)
	}
}
