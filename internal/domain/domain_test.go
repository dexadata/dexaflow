package domain

import (
	"strings"
	"testing"
)

func ptr[T any](v T) *T { return &v }

func validDAGSpec() *DAGSpec {
	return &DAGSpec{
		SchemaVersion: "1.0",
		DagID:         "etl_sales",
		DagVersion:    "v1.2.3",
		Image:         "myrepo/etl-sales:v1.2.3",
		Schedule:      ptr("0 5 * * *"),
		Tasks: []TaskSpec{
			{
				TaskID:      "extract",
				Type:        TaskTypePython,
				Entrypoint:  "tasks.extract:run",
				TriggerRule: TriggerRuleAllSuccess,
			},
			{
				TaskID:     "notify",
				Type:       TaskTypeBash,
				DependsOn:  []string{"extract"},
				Entrypoint: "echo done",
			},
		},
	}
}

func validLeoflowConfig() *LeoflowConfig {
	return &LeoflowConfig{
		SchemaVersion: "1.0",
		DagID:         "etl_sales",
		PythonVersion: "3.11",
		Dependencies:  []string{"pandas==2.1.0"},
	}
}

func TestDAGSpecValidateAcceptsValidSpec(t *testing.T) {
	if err := validDAGSpec().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

// A task marked with on_failure_callback (#424) validates: the compiler emits
// the boolean flag (the callable stays in dag.py), and the schema accepts it.
func TestDAGSpecValidateAcceptsOnFailureCallback(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks[0].OnFailureCallback = true
	if err := spec.Validate(); err != nil {
		t.Fatalf("Validate() with on_failure_callback = %v, want nil", err)
	}
}

func TestDAGSpecValidateRejectsInvalidSpecs(t *testing.T) {
	cases := map[string]func(*DAGSpec){
		"missing dag_id":       func(d *DAGSpec) { d.DagID = "" },
		"no tasks":             func(d *DAGSpec) { d.Tasks = nil },
		"bad schema_version":   func(d *DAGSpec) { d.SchemaVersion = "2.0" },
		"unknown task type":    func(d *DAGSpec) { d.Tasks[0].Type = "ruby" },
		"bad trigger rule":     func(d *DAGSpec) { d.Tasks[0].TriggerRule = "sometimes" },
		"python without entry": func(d *DAGSpec) { d.Tasks[0].Entrypoint = "" },
		"bash without command": func(d *DAGSpec) { d.Tasks[1].Entrypoint = "" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validDAGSpec()
			mutate(spec)
			if err := spec.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %q", name)
			}
		})
	}
}

// pod is now the only supported execution_mode (the inline http_api path was
// removed, ADR 0047/0048, #512). Any other value is rejected for every task type.
func TestExecutionModeRejectsNonPodModes(t *testing.T) {
	cases := map[string]func(*DAGSpec){
		"python inline": func(d *DAGSpec) { d.Tasks[0].ExecutionMode = ExecutionMode("inline") },
		"bash inline":   func(d *DAGSpec) { d.Tasks[1].ExecutionMode = ExecutionMode("inline") },
		"unknown mode":  func(d *DAGSpec) { d.Tasks[1].ExecutionMode = "turbo" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			spec := validDAGSpec()
			mutate(spec)
			if err := spec.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %q", name)
			}
		})
	}
}

func TestPythonMayDeclarePodMode(t *testing.T) {
	spec := validDAGSpec()
	spec.Tasks[0].ExecutionMode = ExecutionModePod
	if err := spec.Validate(); err != nil {
		t.Errorf("python with execution_mode pod should be valid: %v", err)
	}
}

func TestLeoflowConfigValidateAcceptsValidConfig(t *testing.T) {
	if err := validLeoflowConfig().Validate(); err != nil {
		t.Fatalf("Validate() = %v, want nil", err)
	}
}

func TestLeoflowConfigValidateAcceptsTaskOverrides(t *testing.T) {
	cfg := validLeoflowConfig()
	cfg.Tasks = map[string]*TaskConfig{
		"transform": {
			Retries:   ptr(5),
			Env:       map[string]string{"TZ": "UTC"},
			Resources: &Resources{Requests: &ResourceQuantity{CPU: "2", Memory: "4Gi"}},
		},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() with tasks override = %v, want nil", err)
	}
}

func TestLeoflowConfigValidateRejectsInvalidConfigs(t *testing.T) {
	cases := map[string]func(*LeoflowConfig){
		"missing dag_id":     func(c *LeoflowConfig) { c.DagID = "" },
		"bad python version": func(c *LeoflowConfig) { c.PythonVersion = "2.7" },
		"bad dag_id pattern": func(c *LeoflowConfig) { c.DagID = "has spaces" },
		"negative retries": func(c *LeoflowConfig) {
			c.Tasks = map[string]*TaskConfig{"t": {Retries: ptr(-1)}}
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			cfg := validLeoflowConfig()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Errorf("Validate() = nil, want error for %q", name)
			}
		})
	}
}

// A resource quantity Kubernetes cannot parse must be rejected where the author
// is still watching. It used to be dropped in silence at pod build
// (internal/executor/kubernetes.go quantities()), so a DAG asking for a 2 GB
// memory limit got a pod with NO limit — the opposite of what was requested, on
// a shared node, with nothing said. "2GB" is exactly the plausible typo: it is
// how memory is written everywhere except Kubernetes, which wants 2Gi or 2G.
func TestDAGSpecValidateRejectsUnparseableResourceQuantities(t *testing.T) {
	for _, tc := range []struct {
		name string
		res  Resources
		want string
	}{
		{"memory limit", Resources{Limits: &ResourceQuantity{Memory: "2GB"}}, "2GB"},
		{"cpu limit", Resources{Limits: &ResourceQuantity{CPU: "half"}}, "half"},
		{"memory request", Resources{Requests: &ResourceQuantity{Memory: "512 MB"}}, "512 MB"},
		{"cpu request", Resources{Requests: &ResourceQuantity{CPU: "1 core"}}, "1 core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			spec := validDAGSpec()
			spec.Tasks[0].Resources = &tc.res
			err := spec.Validate()
			if err == nil {
				t.Fatalf("Validate() = nil, want an error naming %q", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q does not quote the offending value %q", err, tc.want)
			}
			if !strings.Contains(err.Error(), spec.Tasks[0].TaskID) {
				t.Errorf("error %q does not name the task", err)
			}
		})
	}
}

// The forms Kubernetes accepts must keep working — this validation must not
// become a second, stricter grammar that rejects legitimate specs.
func TestDAGSpecValidateAcceptsKubernetesQuantities(t *testing.T) {
	for _, q := range []Resources{
		{Limits: &ResourceQuantity{CPU: "500m", Memory: "512Mi"}},
		{Limits: &ResourceQuantity{CPU: "2", Memory: "2Gi"}},
		{Requests: &ResourceQuantity{CPU: "0.5", Memory: "1G"}},
		{Limits: &ResourceQuantity{Memory: "1500k"}},
		{}, // nothing declared is valid: the platform default applies
	} {
		spec := validDAGSpec()
		r := q
		spec.Tasks[0].Resources = &r
		if err := spec.Validate(); err != nil {
			t.Errorf("Validate() with %+v = %v, want nil", q, err)
		}
	}
}
