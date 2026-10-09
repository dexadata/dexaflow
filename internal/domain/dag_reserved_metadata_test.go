package domain

import (
	"strings"
	"testing"
)

// TestValidateRejectsReservedExecutionMetadata pins the early feedback for the
// executor-owned leoflow.io/ prefix: a task declaring a pod label or annotation
// under it is refused at compile/registration, naming the task and the key, so
// the author learns at authoring time rather than finding the key silently
// dropped from the pod at dispatch.
func TestValidateRejectsReservedExecutionMetadata(t *testing.T) {
	cases := map[string]Execution{
		"label":      {Labels: map[string]string{"leoflow.io/warm-worker": "true"}},
		"annotation": {Annotations: map[string]string{"leoflow.io/agent-identity": "{}"}},
	}
	for name, exec := range cases {
		t.Run(name, func(t *testing.T) {
			spec := &DAGSpec{
				SchemaVersion: "1.0", DagID: "d", DagVersion: "v1", Image: "img:v1",
				Tasks: []TaskSpec{{TaskID: "extract", Type: TaskTypePython, Entrypoint: "dag:extract", Execution: &exec}},
			}
			err := spec.Validate()
			if err == nil {
				t.Fatal("a task declaring metadata under leoflow.io/ must be rejected")
			}
			if !strings.Contains(err.Error(), "leoflow.io/") || !strings.Contains(err.Error(), "extract") {
				t.Errorf("rejection should name the task and the reserved key, got: %v", err)
			}
		})
	}
}

// TestValidateAcceptsOrdinaryExecutionMetadata guards against over-reach: keys
// outside the reserved prefix, including other domains that merely contain
// "leoflow", stay valid.
func TestValidateAcceptsOrdinaryExecutionMetadata(t *testing.T) {
	spec := &DAGSpec{
		SchemaVersion: "1.0", DagID: "d", DagVersion: "v1", Image: "img:v1",
		Tasks: []TaskSpec{{TaskID: "extract", Type: TaskTypePython, Entrypoint: "dag:extract", Execution: &Execution{
			Labels:      map[string]string{"team": "data-eng", "example.com/leoflow.io": "x"},
			Annotations: map[string]string{"cost-center": "1234"},
		}}},
	}
	if err := spec.Validate(); err != nil {
		t.Fatalf("ordinary execution metadata must validate, got: %v", err)
	}
}
