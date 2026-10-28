package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// is_paused_upon_creation (Airflow's DAG kwarg, #1705) is optional and keeps
// both values: an explicit false must survive decoding, validation and
// re-encoding, because it is what makes a DAG start active on a server that
// pauses new DAGs.
func TestDAGSpecIsPausedUponCreationRoundTrip(t *testing.T) {
	for _, raw := range []string{"true", "false"} {
		in := `{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1",` +
			`"is_paused_upon_creation":` + raw + `,"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`
		var spec DAGSpec
		if err := json.Unmarshal([]byte(in), &spec); err != nil {
			t.Fatal(err)
		}
		if spec.IsPausedUponCreation == nil || *spec.IsPausedUponCreation != (raw == "true") {
			t.Fatalf("%s: decoded IsPausedUponCreation = %v", raw, spec.IsPausedUponCreation)
		}
		if err := spec.Validate(); err != nil {
			t.Fatalf("%s: a spec with is_paused_upon_creation must validate: %v", raw, err)
		}
		out, err := json.Marshal(&spec)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(out), `"is_paused_upon_creation":`+raw) {
			t.Fatalf("%s: re-encoded spec lost the field: %s", raw, out)
		}
	}
}

// A spec that does not set it encodes without the key, so the spec hash of
// every DAG registered before the field existed is unchanged.
func TestDAGSpecIsPausedUponCreationOmittedWhenUnset(t *testing.T) {
	spec := DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
	}
	out, err := json.Marshal(&spec)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "is_paused_upon_creation") {
		t.Fatalf("unset field must be omitted: %s", out)
	}
}

// The schema rejects a non-boolean value.
func TestDAGSpecSchemaRejectsNonBooleanIsPausedUponCreation(t *testing.T) {
	s, err := schemas()
	if err != nil {
		t.Fatal(err)
	}
	var doc any
	if err := json.Unmarshal([]byte(`{"schema_version":"1.0","dag_id":"etl","dag_version":"v1","image":"img:v1",`+
		`"is_paused_upon_creation":"yes","tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`), &doc); err != nil {
		t.Fatal(err)
	}
	if err := validateAgainst(s.dag, doc); err == nil {
		t.Fatal(`is_paused_upon_creation: "yes" must be refused by the schema`)
	}
}
