package domain

import (
	"strings"
	"testing"
)

// TestLeoflowConfigRejectsReservedMetadataKeys: dexaflow.yaml is where authors
// write execution.labels and execution.annotations, so the reserved prefix is
// refused there, naming the file's own field, before anything is compiled.
func TestLeoflowConfigRejectsReservedMetadataKeys(t *testing.T) {
	for name, ex := range map[string]*Execution{
		"label":      {Labels: map[string]string{"leoflow.io/warm-worker": "true"}},
		"annotation": {Annotations: map[string]string{"leoflow.io/task-instance-id": "x"}},
	} {
		c := &LeoflowConfig{DagID: "sales", Tasks: map[string]*TaskConfig{"extract": {Execution: ex}}}
		err := c.Validate()
		if err == nil || !strings.Contains(err.Error(), "leoflow.io/") {
			t.Errorf("%s: Validate = %v, want a refusal naming the reserved key", name, err)
		}
	}
	ok := &LeoflowConfig{DagID: "sales", Tasks: map[string]*TaskConfig{"extract": {Execution: &Execution{
		Labels: map[string]string{"team.example.com/owner": "data", "leoflow.iox/other": "fine"},
	}}}}
	if err := ok.Validate(); err != nil {
		t.Errorf("ordinary labels rejected: %v", err)
	}
}
