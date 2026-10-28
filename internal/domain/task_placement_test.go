package domain

import (
	"strings"
	"testing"
)

// TestValidateRejectsMalformedTaskPlacement verifies that structured task placement
// fields (tolerations, affinity, topologySpreadConstraints, resourceClaims) are
// validated loudly at DAG registration rather than silently dropped at pod build (#628).
func TestValidateRejectsMalformedTaskPlacement(t *testing.T) {
	cases := map[string]struct {
		task TaskSpec
		want string
	}{
		"toleration unknown key": {
			task: TaskSpec{
				TaskID:     "t1",
				Type:       TaskTypePython,
				Entrypoint: "dag:t1",
				Execution: &Execution{
					Tolerations: []map[string]any{{"keyy": "dedicated", "operator": "Equal"}},
				},
			},
			want: "execution.tolerations",
		},
		"toleration invalid type": {
			task: TaskSpec{
				TaskID:     "t2",
				Type:       TaskTypePython,
				Entrypoint: "dag:t2",
				Execution: &Execution{
					Tolerations: []map[string]any{{"key": "dedicated", "tolerationSeconds": "not-a-number"}},
				},
			},
			want: "execution.tolerations",
		},
		"affinity unknown key": {
			task: TaskSpec{
				TaskID:     "t3",
				Type:       TaskTypePython,
				Entrypoint: "dag:t3",
				Execution: &Execution{
					Affinity: map[string]any{"nodeAffinityy": map[string]any{}},
				},
			},
			want: "execution.affinity",
		},
		"topology spread constraints unknown key": {
			task: TaskSpec{
				TaskID:     "t4",
				Type:       TaskTypePython,
				Entrypoint: "dag:t4",
				Execution: &Execution{
					TopologySpreadConstraints: []map[string]any{{"maxSkeww": 1}},
				},
			},
			want: "execution.topology_spread_constraints",
		},
		"resource claims unknown key": {
			task: TaskSpec{
				TaskID:     "t5",
				Type:       TaskTypePython,
				Entrypoint: "dag:t5",
				Execution: &Execution{
					ResourceClaims: []map[string]any{{"namee": "gpu"}},
				},
			},
			want: "execution.resource_claims",
		},
		"resources claims unknown key": {
			task: TaskSpec{
				TaskID:     "t6",
				Type:       TaskTypePython,
				Entrypoint: "dag:t6",
				Resources: &Resources{
					Claims: []map[string]any{{"namee": "gpu"}},
				},
			},
			want: "resources.claims",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			spec := &DAGSpec{
				SchemaVersion: "1.0",
				DagID:         "test_dag",
				DagVersion:    "v1",
				Image:         "img:v1",
				Tasks:         []TaskSpec{tc.task},
			}
			err := spec.Validate()
			if err == nil {
				t.Fatalf("expected validation error for %s, got nil", name)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q should mention %q", err.Error(), tc.want)
			}
			if !strings.Contains(err.Error(), tc.task.TaskID) {
				t.Errorf("error %q should name task %q", err.Error(), tc.task.TaskID)
			}
		})
	}
}

// TestValidateAcceptsValidTaskPlacement ensures that valid placement specifications
// validate cleanly.
func TestValidateAcceptsValidTaskPlacement(t *testing.T) {
	spec := &DAGSpec{
		SchemaVersion: "1.0",
		DagID:         "valid_placement",
		DagVersion:    "v1",
		Image:         "img:v1",
		Tasks: []TaskSpec{
			{
				TaskID:     "t1",
				Type:       TaskTypePython,
				Entrypoint: "dag:t1",
				Execution: &Execution{
					Tolerations: []map[string]any{
						{"key": "dedicated", "operator": "Equal", "value": "gpu", "effect": "NoSchedule"},
					},
					Affinity: map[string]any{
						"nodeAffinity": map[string]any{
							"requiredDuringSchedulingIgnoredDuringExecution": map[string]any{
								"nodeSelectorTerms": []any{
									map[string]any{
										"matchExpressions": []any{
											map[string]any{
												"key":      "disktype",
												"operator": "In",
												"values":   []any{"ssd"},
											},
										},
									},
								},
							},
						},
					},
					TopologySpreadConstraints: []map[string]any{
						{
							"maxSkew":           1,
							"topologyKey":       "topology.kubernetes.io/zone",
							"whenUnsatisfiable": "DoNotSchedule",
							"labelSelector": map[string]any{
								"matchLabels": map[string]any{"app": "worker"},
							},
						},
					},
					ResourceClaims: []map[string]any{
						{"name": "gpu-claim"},
					},
				},
				Resources: &Resources{
					Claims: []map[string]any{
						{"name": "gpu-claim"},
					},
				},
			},
		},
	}

	if err := spec.Validate(); err != nil {
		t.Fatalf("valid placement must validate cleanly, got: %v", err)
	}
}
