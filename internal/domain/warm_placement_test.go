package domain

import (
	"testing"
)

func warmStrPtr(s string) *string { return &s }
func warmInt64Ptr(n int64) *int64 { return &n }

// TestWarmPlacementReasonExecutionFields: a warm worker is created before any
// task is known, so it carries none of a task's own placement or pod
// metadata. A task that declares any of them takes a dedicated pod, and the
// reason names the field the author set.
func TestWarmPlacementReasonExecutionFields(t *testing.T) {
	cases := []struct {
		name string
		exec *Execution
		want string
	}{
		{"no execution", nil, ""},
		{"empty execution", &Execution{}, ""},
		{"image pull policy only", &Execution{ImagePullPolicy: "Always"}, ""},
		{"default service account", &Execution{ServiceAccount: "dexaflow-task"}, ""},
		{"node selector", &Execution{NodeSelector: map[string]string{"pool": "gpu"}}, "node_selector set"},
		{"tolerations", &Execution{Tolerations: []map[string]any{{"key": "gpu"}}}, "tolerations set"},
		{"affinity", &Execution{Affinity: map[string]any{"nodeAffinity": map[string]any{}}}, "affinity set"},
		{"topology spread", &Execution{TopologySpreadConstraints: []map[string]any{{"maxSkew": 1}}}, "topology_spread_constraints set"},
		{"priority class", &Execution{PriorityClassName: "etl-low"}, "priority_class_name set"},
		{"runtime class", &Execution{RuntimeClassName: warmStrPtr("gvisor")}, "runtime_class_name set"},
		{"empty runtime class", &Execution{RuntimeClassName: warmStrPtr("")}, "runtime_class_name set"},
		{"termination grace", &Execution{TerminationGracePeriodSeconds: warmInt64Ptr(120)}, "termination_grace_period_seconds set"},
		{"resource claims", &Execution{ResourceClaims: []map[string]any{{"name": "gpu"}}}, "resource_claims set"},
		{"labels", &Execution{Labels: map[string]string{"team": "a"}}, "labels set"},
		{"annotations", &Execution{Annotations: map[string]string{"sidecar.istio.io/inject": "false"}}, "annotations set"},
		{"pinned other service account", &Execution{ServiceAccount: "custom-sa"}, "service_account set"},
	}
	p := WarmPolicy{Enabled: true, ServiceAccount: "dexaflow-task"}
	for _, c := range cases {
		got := p.DedicatedReason(WarmVersion{Image: "etl:v1"}, TaskSpec{TaskID: "t", Execution: c.exec})
		if got != c.want {
			t.Errorf("%s: DedicatedReason = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestWarmPlacementReasonServiceAccount: with no default task ServiceAccount
// the warm workers run as the namespace default, so a task that pins any
// ServiceAccount takes a dedicated pod, and one that pins none stays eligible.
func TestWarmPlacementReasonServiceAccount(t *testing.T) {
	p := WarmPolicy{Enabled: true}
	cases := []struct {
		name string
		exec *Execution
		want string
	}{
		{"pinned SA, no warm default", &Execution{ServiceAccount: "custom-sa"}, "service_account set"},
		{"no SA, no warm default", &Execution{}, ""},
	}
	for _, c := range cases {
		if got := p.DedicatedReason(WarmVersion{Image: "etl:v1"}, TaskSpec{TaskID: "t", Execution: c.exec}); got != c.want {
			t.Errorf("%s: DedicatedReason = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestWarmPlacementReasonVersion: what excludes a whole version comes first,
// whatever the task declares: warm pools off, staging (ADR 0058 D5) and
// source mode (ADR 0067 §3).
func TestWarmPlacementReasonVersion(t *testing.T) {
	task := TaskSpec{TaskID: "t", Execution: &Execution{NodeSelector: map[string]string{"a": "b"}}}
	cases := []struct {
		name    string
		policy  WarmPolicy
		version WarmVersion
		want    string
	}{
		{"warm pools off", WarmPolicy{}, WarmVersion{Image: "etl:v1"}, "warm pools off"},
		{"staging", WarmPolicy{Enabled: true}, WarmVersion{Image: "etl:v1", Staging: &StagingConfig{Enabled: true}}, "staging enabled"},
		{"staging block off", WarmPolicy{Enabled: true}, WarmVersion{Image: "etl:v1", Staging: &StagingConfig{}}, "node_selector set"},
		{"source mode", WarmPolicy{Enabled: true, SourceModeImage: "rt@sha256:1"}, WarmVersion{Image: "rt@sha256:1", HasSource: true}, "source mode"},
		{"source mode with warm pools", WarmPolicy{Enabled: true, SourceModeImage: "rt@sha256:1", SourceModeWarm: true}, WarmVersion{Image: "rt@sha256:1", HasSource: true}, "node_selector set"},
		{"runtime image without source", WarmPolicy{Enabled: true, SourceModeImage: "rt@sha256:1"}, WarmVersion{Image: "rt@sha256:1"}, "node_selector set"},
		{"other image with source", WarmPolicy{Enabled: true, SourceModeImage: "rt@sha256:1"}, WarmVersion{Image: "etl:v1", HasSource: true}, "node_selector set"},
	}
	for _, c := range cases {
		if got := c.policy.DedicatedReason(c.version, task); got != c.want {
			t.Errorf("%s: DedicatedReason = %q, want %q", c.name, got, c.want)
		}
		wantVersion := c.want
		if wantVersion == "node_selector set" {
			wantVersion = ""
		}
		if got := c.policy.VersionReason(c.version); got != wantVersion {
			t.Errorf("%s: VersionReason = %q, want %q", c.name, got, wantVersion)
		}
	}
}

// TestWarmPlacementReasonSize: with a resource unit a warm pod is one unit, so
// only a size-1 task without resources fits on it (ADR 0066 §3); without a
// unit, a task whose limit on a dedicated pod would be above the default
// limits the warm pod gets takes a dedicated pod (#537).
func TestWarmPlacementReasonSize(t *testing.T) {
	unit, err := ParseResourceUnit(ResourceUnitConfig{CPU: "250m", Memory: "512Mi"})
	if err != nil {
		t.Fatal(err)
	}
	limits, err := ParseDefaultLimits("1", "1Gi")
	if err != nil {
		t.Fatal(err)
	}
	big := &Resources{Limits: &ResourceQuantity{CPU: "2"}}
	small := &Resources{Limits: &ResourceQuantity{CPU: "500m"}}
	cases := []struct {
		name   string
		policy WarmPolicy
		task   TaskSpec
		want   string
	}{
		{"unit, size 1", WarmPolicy{Enabled: true, Unit: unit}, TaskSpec{}, ""},
		{"unit, size 4", WarmPolicy{Enabled: true, Unit: unit}, TaskSpec{PoolSlots: 4}, "size 4 is above the warm pod's 1 unit"},
		{"unit, own resources", WarmPolicy{Enabled: true, Unit: unit}, TaskSpec{Resources: small}, "resources set (a warm pod is 1 unit)"},
		{"limits, small task", WarmPolicy{Enabled: true, Limits: limits}, TaskSpec{Resources: small}, ""},
		{"limits, big task", WarmPolicy{Enabled: true, Limits: limits}, TaskSpec{Resources: big}, "resources above the warm pod's default limits"},
		{"unit wins over limits", WarmPolicy{Enabled: true, Unit: unit, Limits: limits}, TaskSpec{}, ""},
		{"no unit, no limits", WarmPolicy{Enabled: true}, TaskSpec{Resources: big, PoolSlots: 4}, ""},
	}
	for _, c := range cases {
		if got := c.policy.DedicatedReason(WarmVersion{Image: "etl:v1"}, c.task); got != c.want {
			t.Errorf("%s: DedicatedReason = %q, want %q", c.name, got, c.want)
		}
	}
}

// TestWarmPolicyHasWarmTask: a version keeps a warm pool only when one of its
// tasks may be placed on a warm worker (#2065), decided per task exactly as
// DedicatedReason decides it: the size against the unit or the default
// limits, the ServiceAccount and the placement fields. A version with no task
// has none.
func TestWarmPolicyHasWarmTask(t *testing.T) {
	unit, err := ParseResourceUnit(ResourceUnitConfig{CPU: "250m", Memory: "512Mi"})
	if err != nil {
		t.Fatal(err)
	}
	limits, err := ParseDefaultLimits("1", "1Gi")
	if err != nil {
		t.Fatal(err)
	}
	sized := TaskSpec{TaskID: "sized", Resources: &Resources{Limits: &ResourceQuantity{CPU: "500m"}}}
	big := TaskSpec{TaskID: "big", Resources: &Resources{Limits: &ResourceQuantity{CPU: "2"}}}
	wide := TaskSpec{TaskID: "wide", PoolSlots: 2}
	plain := TaskSpec{TaskID: "plain"}
	pinned := TaskSpec{TaskID: "pinned", Execution: &Execution{ServiceAccount: "reporting"}}
	placed := TaskSpec{TaskID: "placed", Execution: &Execution{Labels: map[string]string{"team": "x"}}}
	cases := []struct {
		name   string
		policy WarmPolicy
		tasks  []TaskSpec
		want   bool
	}{
		{"unit, every task declares resources", WarmPolicy{Unit: unit}, []TaskSpec{sized, big}, false},
		{"unit, every task is size 2", WarmPolicy{Unit: unit}, []TaskSpec{wide, wide}, false},
		{"unit, one size-1 task without resources", WarmPolicy{Unit: unit}, []TaskSpec{sized, wide, plain}, true},
		{"limits, every task above them", WarmPolicy{Limits: limits}, []TaskSpec{big}, false},
		{"limits, one task within them", WarmPolicy{Limits: limits}, []TaskSpec{big, sized}, true},
		{"no unit, no limits", WarmPolicy{}, []TaskSpec{big, wide}, true},
		{"every task pins another ServiceAccount or sets labels", WarmPolicy{ServiceAccount: "dexaflow-task"}, []TaskSpec{pinned, placed}, false},
		{"no task", WarmPolicy{}, nil, false},
	}
	for _, c := range cases {
		if got := c.policy.HasWarmTask(c.tasks); got != c.want {
			t.Errorf("%s: HasWarmTask = %v, want %v", c.name, got, c.want)
		}
		someWarm := false
		for _, task := range c.tasks {
			someWarm = someWarm || WarmPolicy{Enabled: true, ServiceAccount: c.policy.ServiceAccount, Unit: c.policy.Unit, Limits: c.policy.Limits}.
				DedicatedReason(WarmVersion{Image: "etl:v1"}, task) == ""
		}
		if someWarm != c.want {
			t.Errorf("%s: HasWarmTask disagrees with DedicatedReason (some task warm = %v)", c.name, someWarm)
		}
	}
}
