package domain

import (
	"encoding/json"
	"testing"
)

// The declared secret set (ADR 0055 / ADR 0045) rides the DAG spec: per-DAG on
// DAGSpec and, for narrowing, per-task on TaskSpec. It serializes under the
// Airflow-native keys `variables` / `connections`, and is additive — absent
// declarations round-trip as empty, so a DAG that declares nothing is unchanged.

func TestDAGSpecDeclaredSecretsRoundTrip(t *testing.T) {
	spec := validDAGSpec()
	spec.Variables = []string{"greeting"}
	spec.Connections = []string{"warehouse"}
	spec.Tasks[0].Variables = []string{"greeting"}
	spec.Tasks[0].Connections = []string{"warehouse"}

	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	// The keys must be the Airflow-native words, not connectors (ADR 0038).
	var raw map[string]any
	if uerr := json.Unmarshal(data, &raw); uerr != nil {
		t.Fatalf("Unmarshal to map = %v", uerr)
	}
	if _, ok := raw["variables"]; !ok {
		t.Errorf("marshaled DAGSpec missing %q key: %s", "variables", data)
	}
	if _, ok := raw["connections"]; !ok {
		t.Errorf("marshaled DAGSpec missing %q key: %s", "connections", data)
	}

	var got DAGSpec
	if uerr := json.Unmarshal(data, &got); uerr != nil {
		t.Fatalf("Unmarshal = %v", uerr)
	}
	if len(got.Variables) != 1 || got.Variables[0] != "greeting" {
		t.Errorf("DAGSpec.Variables = %v, want [greeting]", got.Variables)
	}
	if len(got.Connections) != 1 || got.Connections[0] != "warehouse" {
		t.Errorf("DAGSpec.Connections = %v, want [warehouse]", got.Connections)
	}
	if len(got.Tasks[0].Variables) != 1 || got.Tasks[0].Variables[0] != "greeting" {
		t.Errorf("TaskSpec.Variables = %v, want [greeting]", got.Tasks[0].Variables)
	}
	if len(got.Tasks[0].Connections) != 1 || got.Tasks[0].Connections[0] != "warehouse" {
		t.Errorf("TaskSpec.Connections = %v, want [warehouse]", got.Tasks[0].Connections)
	}
}

// A declaration is optional: an absent one marshals to no key (omitempty) and a
// spec declaring nothing still validates. This is the Lite/back-compat safety —
// every existing DAG declares nothing, so none changes shape.
func TestDAGSpecDeclaredSecretsAbsentIsEmpty(t *testing.T) {
	spec := validDAGSpec()
	data, err := json.Marshal(spec)
	if err != nil {
		t.Fatalf("Marshal() = %v", err)
	}
	var raw map[string]any
	if uerr := json.Unmarshal(data, &raw); uerr != nil {
		t.Fatalf("Unmarshal to map = %v", uerr)
	}
	if _, ok := raw["variables"]; ok {
		t.Errorf("absent declaration should omit %q key: %s", "variables", data)
	}
	if _, ok := raw["connections"]; ok {
		t.Errorf("absent declaration should omit %q key: %s", "connections", data)
	}
	if verr := spec.Validate(); verr != nil {
		t.Fatalf("Validate() with no declaration = %v, want nil", verr)
	}
}

// dexaflow.yaml may declare variables/connections per-DAG and per-task; the
// author-facing config validates against the dexaflow.yaml schema (the source
// keys the compiler carries into dag.json).
func TestLeoflowConfigValidateAcceptsDeclaredSecrets(t *testing.T) {
	cfg := validLeoflowConfig()
	cfg.Connections = []string{"warehouse"}
	cfg.Variables = []string{"greeting"}
	cfg.Tasks = map[string]*TaskConfig{
		"extract": {Connections: []string{"warehouse"}, Variables: []string{"greeting"}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with declarations = %v, want nil", err)
	}
}

// A spec declaring variables/connections at both DAG and task level passes
// schema validation (the schema grew the two array fields at both scopes).
func TestDAGSpecValidateAcceptsDeclaredSecrets(t *testing.T) {
	spec := validDAGSpec()
	spec.Variables = []string{"greeting"}
	spec.Connections = []string{"warehouse"}
	spec.Tasks[0].Variables = []string{"greeting"}
	spec.Tasks[0].Connections = []string{"warehouse"}
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() with declarations = %v, want nil", err)
	}
}
