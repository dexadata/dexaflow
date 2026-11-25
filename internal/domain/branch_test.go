package domain

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestBranchRoundTrips holds the dag.json shape of ADR 0064 D3.
func TestBranchRoundTrips(t *testing.T) {
	in := `{"task_id":"gate","type":"python","entrypoint":"dag:gate","branch":{"kind":"short_circuit","ignore_downstream_trigger_rules":false}}`
	var task TaskSpec
	if err := json.Unmarshal([]byte(in), &task); err != nil {
		t.Fatal(err)
	}
	if task.Branch == nil || task.Branch.Kind != BranchKindShortCircuit || task.Branch.IgnoresDownstreamTriggerRules() {
		t.Fatalf("decoded branch = %+v", task.Branch)
	}
	out, err := json.Marshal(task)
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != in {
		t.Errorf("round trip = %s, want %s", out, in)
	}
}

func TestShortCircuitIgnoresDownstreamTriggerRulesByDefault(t *testing.T) {
	if !(BranchSpec{Kind: BranchKindShortCircuit}).IgnoresDownstreamTriggerRules() {
		t.Error("an unset ignore_downstream_trigger_rules must default to true, as in Airflow")
	}
}

// TestHashWithoutBranchIsUnchanged pins the canonical hash of a spec without
// branch, so adding the field never moves an existing DAG version.
func TestHashWithoutBranchIsUnchanged(t *testing.T) {
	spec := &DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a"}},
	}
	got, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	const want = "9b987691ffe76e3780a155051ef8c38590ba02c66d76a16579895bff8b5a2079" // computed before TaskSpec.Branch existed
	if got != want {
		t.Errorf("hash = %s, want %s", got, want)
	}
}

// TestBranchIsStillRefusedAtRegistration: ADR 0064 step 2 ships the planner
// filter dark. The schema accepts branch only one release later (step 3), so
// a dag.json that carries it is refused until then.
func TestBranchIsStillRefusedAtRegistration(t *testing.T) {
	spec := &DAGSpec{
		SchemaVersion: "1.0", DagID: "etl", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{{TaskID: "a", Type: TaskTypePython, Entrypoint: "dag:a", Branch: &BranchSpec{Kind: BranchKindBranch}}},
	}
	err := spec.Validate()
	if err == nil || !strings.Contains(err.Error(), "branch") {
		t.Fatalf("Validate() = %v, want a refusal naming branch", err)
	}
}

func TestBranchDecisionFollows(t *testing.T) {
	d := BranchDecision{FollowTaskIDs: []string{"b"}}
	if !d.Follows("b") || d.Follows("c") {
		t.Errorf("Follows on %+v is wrong", d)
	}
}
