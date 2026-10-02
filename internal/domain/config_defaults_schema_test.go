package domain

import (
	"strings"
	"testing"
)

// A dexaflow.yaml `defaults` block declaring resources and node_selector (the
// DAG-wide placement/QoS fallback, EKS validation aresta #6) validates against
// the canonical schema.
func TestDefaultsBlockWithNodeSelectorValidates(t *testing.T) {
	c := &LeoflowConfig{
		DagID: "sales",
		Defaults: &ConfigDefaults{
			Resources:    &DefaultResources{CPU: "1", Memory: "1Gi"},
			NodeSelector: map[string]string{"disktype": "ssd"},
		},
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("valid defaults block rejected: %v", err)
	}
}

// An unknown key under `defaults` fails validation loudly
// (additionalProperties:false) instead of being silently accepted-and-dropped —
// the exact footgun that hid node_selector before it became a wired field. This
// guards the whole "accepted but never reaches the pod" class: a future unwired
// default key fails at compile, not at runtime.
func TestDefaultsBlockUnknownKeyRejected(t *testing.T) {
	s, err := schemas()
	if err != nil {
		t.Fatalf("compile schemas: %v", err)
	}
	inst := map[string]any{
		"dag_id": "sales",
		"defaults": map[string]any{
			"resources": map[string]any{"cpu": "1", "memory": "1Gi"},
			// Typo / unwired key: must be rejected, not silently discarded.
			"nodeselector": map[string]any{"disktype": "ssd"},
		},
	}
	if verr := validateAgainst(s.leoflow, inst); verr == nil {
		t.Fatal("expected unknown key under defaults to be rejected (additionalProperties:false)")
	}
}

// A PARTIAL defaults.resources block — only cpu, or only memory — is rejected at
// compile, naming the field that is missing.
//
// The block's sole documented purpose is the requests == limits expansion, so
// half of it is meaningless: AsResources still returns a NON-NIL Resources with
// one dimension empty, the empty dimension is dropped from the pod spec, and
// because the object is non-nil the dispatcher's fallback to the per-cluster
// platform default never runs (it switches wholesale on task.Resources != nil).
// The task then ends up worse configured than with no defaults block at all —
// one dimension pinned, the other with no request or limit from anywhere, and
// the cluster's own default silently suppressed (#802). Rejecting the partial is
// the fail-loud half: the author sees it on their own machine, at compile time,
// with the missing field named, instead of a fleet operator discovering months
// later that the platform floor stopped applying.
func TestPartialDefaultsResourcesRejected(t *testing.T) {
	for _, tc := range []struct {
		name    string
		res     *DefaultResources
		missing string
	}{
		{"cpu only", &DefaultResources{CPU: "500m"}, "memory"},
		{"memory only", &DefaultResources{Memory: "512Mi"}, "cpu"},
		{"present but empty", &DefaultResources{}, "cpu"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := &LeoflowConfig{DagID: "sales", Defaults: &ConfigDefaults{Resources: tc.res}}
			err := c.Validate()
			if err == nil {
				t.Fatalf("a partial defaults.resources (%+v) must be rejected: it suppresses the platform default instead of completing it", *tc.res)
			}
			if !strings.Contains(err.Error(), tc.missing) {
				t.Errorf("error must name the missing field %q so the author knows what to add, got: %v", tc.missing, err)
			}
		})
	}
}

// The complete pair, and a defaults block that declares no resources at all,
// both stay valid — the requirement is "both or neither", not "always both".
func TestDefaultsResourcesBothOrNeitherValidates(t *testing.T) {
	both := &LeoflowConfig{
		DagID:    "sales",
		Defaults: &ConfigDefaults{Resources: &DefaultResources{CPU: "1", Memory: "1Gi"}},
	}
	if err := both.Validate(); err != nil {
		t.Errorf("the complete pair must stay valid: %v", err)
	}
	neither := &LeoflowConfig{
		DagID:    "sales",
		Defaults: &ConfigDefaults{NodeSelector: map[string]string{"disktype": "ssd"}},
	}
	if err := neither.Validate(); err != nil {
		t.Errorf("a defaults block with no resources at all must stay valid: %v", err)
	}
}
