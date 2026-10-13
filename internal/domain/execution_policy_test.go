package domain

import (
	"strings"
	"testing"
)

// everyPlacementField sets every field the allowlist governs, so a test can
// prove each one is checked.
func everyPlacementField() *Execution {
	return &Execution{
		NodeSelector:                  map[string]string{"pool": "gpu"},
		Tolerations:                   []map[string]any{{"key": "gpu", "operator": "Exists"}},
		Affinity:                      map[string]any{"nodeAffinity": map[string]any{}},
		TopologySpreadConstraints:     []map[string]any{{"maxSkew": 1}},
		PriorityClassName:             "high",
		RuntimeClassName:              ptr("gvisor"),
		ResourceClaims:                []map[string]any{{"name": "gpu"}},
		TerminationGracePeriodSeconds: ptr(int64(60)),
		Labels:                        map[string]string{"team": "a"},
		Annotations:                   map[string]string{"note": "x"},
	}
}

// Unset (the default) allows every field: no policy, no behavior change.
func TestExecutionPolicyDefaultAllowsEverything(t *testing.T) {
	for name, cfg := range map[string]ExecutionPolicyConfig{
		"zero value": {},
		"star":       {Fields: []string{"*"}},
	} {
		p, err := ParseExecutionPolicy(cfg)
		if err != nil {
			t.Fatalf("%s: ParseExecutionPolicy: %v", name, err)
		}
		if p != nil {
			t.Errorf("%s: policy = %+v, want nil (allow all)", name, p)
		}
		task := TaskSpec{TaskID: "t", Execution: everyPlacementField()}
		if err := p.CheckTask(task); err != nil {
			t.Errorf("%s: CheckTask = %v, want nil", name, err)
		}
	}
}

// An explicitly empty list allows no field, as "none" does and as an empty
// list does in Helm. Only an unset list (nil) means "every field": a config
// file that writes `fields: []` to lock placement down must not open it.
func TestExecutionPolicyExplicitEmptyFieldsAllowsNone(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{Fields: []string{}})
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	if p == nil {
		t.Fatal("policy is nil (allow all), want one that allows no field")
	}
	if err := p.CheckTask(TaskSpec{TaskID: "t", Execution: everyPlacementField()}); err == nil {
		t.Fatal("CheckTask = nil, want a refusal")
	}
	if err := p.CheckTask(TaskSpec{TaskID: "plain"}); err != nil {
		t.Errorf("CheckTask(no execution) = %v, want nil", err)
	}
}

// Each governed field is refused when the allowlist leaves it out, and the
// error names the task, the field and the setting to change.
func TestExecutionPolicyRefusesEachFieldOutsideTheAllowlist(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{Fields: []string{"none"}})
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	if p == nil {
		t.Fatal("policy is nil, want one that allows no field")
	}
	err = p.CheckTask(TaskSpec{TaskID: "extract", Execution: everyPlacementField()})
	if err == nil {
		t.Fatal("CheckTask = nil, want a refusal")
	}
	for _, f := range ExecutionPolicyFields {
		if !strings.Contains(err.Error(), "execution."+f) {
			t.Errorf("error %q does not name execution.%s", err, f)
		}
	}
	for _, want := range []string{`task "extract"`, "executor.defaults.execution_allowlist.fields"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// A field on the list passes; fields off it are still refused; fields the
// task leaves empty are never refused.
func TestExecutionPolicyAllowsListedFields(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{Fields: []string{"node_selector", " tolerations "}})
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	ok := TaskSpec{TaskID: "t", Execution: &Execution{
		NodeSelector:   map[string]string{"pool": "etl"},
		Tolerations:    []map[string]any{{"key": "etl"}},
		ServiceAccount: "sa", // not governed by fields
	}}
	if err := p.CheckTask(ok); err != nil {
		t.Errorf("CheckTask(listed fields) = %v, want nil", err)
	}
	if err := p.CheckTask(TaskSpec{TaskID: "t"}); err != nil {
		t.Errorf("CheckTask(no execution) = %v, want nil", err)
	}
	bad := TaskSpec{TaskID: "t", Execution: &Execution{PriorityClassName: "high"}}
	if err := p.CheckTask(bad); err == nil || !strings.Contains(err.Error(), "execution.priority_class_name") {
		t.Errorf("CheckTask(priority_class_name) = %v, want a refusal naming it", err)
	}
}

// priority_classes restricts the values of priority_class_name.
func TestExecutionPolicyRestrictsPriorityClasses(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{PriorityClasses: []string{"etl-low", "etl-normal"}})
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	if p == nil {
		t.Fatal("policy is nil, want one that restricts priority classes")
	}
	if cerr := p.CheckTask(TaskSpec{TaskID: "t", Execution: &Execution{PriorityClassName: "etl-low"}}); cerr != nil {
		t.Errorf("listed priority class refused: %v", cerr)
	}
	if cerr := p.CheckTask(TaskSpec{TaskID: "t", Execution: &Execution{Affinity: map[string]any{"a": 1}}}); cerr != nil {
		t.Errorf("other fields stay allowed with fields unset: %v", cerr)
	}
	err = p.CheckTask(TaskSpec{TaskID: "t", Execution: &Execution{PriorityClassName: "system-cluster-critical"}})
	if err == nil {
		t.Fatal("unlisted priority class accepted")
	}
	for _, want := range []string{"system-cluster-critical", "executor.defaults.execution_allowlist.priority_classes", "etl-low"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestExecutionPolicyRejectsBadConfig(t *testing.T) {
	for name, cfg := range map[string]ExecutionPolicyConfig{
		"unknown field":  {Fields: []string{"node_selector", "hostNetwork"}},
		"star and field": {Fields: []string{"*", "affinity"}},
		"none and field": {Fields: []string{"none", "affinity"}},
		"blank entry":    {Fields: []string{"affinity", " "}},
		"blank class":    {PriorityClasses: []string{""}},
	} {
		if _, err := ParseExecutionPolicy(cfg); err == nil {
			t.Errorf("%s: ParseExecutionPolicy accepted %+v", name, cfg)
		}
	}
}

// CheckSpec reports every task that breaks the policy, not only the first.
func TestExecutionPolicyCheckSpecReportsEveryTask(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{Fields: []string{"none"}})
	if err != nil {
		t.Fatal(err)
	}
	spec := &DAGSpec{Tasks: []TaskSpec{
		{TaskID: "a", Execution: &Execution{Affinity: map[string]any{"x": 1}}},
		{TaskID: "b"},
		{TaskID: "c", Execution: &Execution{NodeSelector: map[string]string{"k": "v"}}},
	}}
	err = p.CheckSpec(spec)
	if err == nil {
		t.Fatal("CheckSpec = nil, want refusals")
	}
	if !strings.Contains(err.Error(), `task "a"`) || !strings.Contains(err.Error(), `task "c"`) || strings.Contains(err.Error(), `task "b"`) {
		t.Errorf("error %q should name tasks a and c only", err)
	}
	var nilPolicy *ExecutionPolicy
	if err := nilPolicy.CheckSpec(spec); err != nil {
		t.Errorf("nil policy CheckSpec = %v, want nil", err)
	}
}

// service_accounts restricts the ServiceAccount a task may name (issue #537
// C2). A task that names none runs as the operator's default and is not
// checked.
func TestExecutionPolicyRestrictsServiceAccounts(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{ServiceAccounts: []string{"etl-reader"}})
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	if p == nil {
		t.Fatal("policy is nil, want one that restricts service accounts")
	}
	if cerr := p.CheckTask(TaskSpec{TaskID: "t", Execution: &Execution{ServiceAccount: "etl-reader"}}); cerr != nil {
		t.Errorf("listed service account refused: %v", cerr)
	}
	if cerr := p.CheckTask(TaskSpec{TaskID: "t"}); cerr != nil {
		t.Errorf("task without a service account refused: %v", cerr)
	}
	err = p.CheckTask(TaskSpec{TaskID: "t", Execution: &Execution{ServiceAccount: "cluster-admin-sa"}})
	if err == nil {
		t.Fatal("unlisted service account accepted")
	}
	for _, want := range []string{"cluster-admin-sa", "executor.defaults.execution_allowlist.service_accounts"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

// images restricts the DAG image (issue #537 C2): an exact reference, or a
// prefix ending in "*".
func TestExecutionPolicyRestrictsImages(t *testing.T) {
	p, err := ParseExecutionPolicy(ExecutionPolicyConfig{Images: []string{"registry.example.com/team-a/*", "busybox:1.36"}})
	if err != nil {
		t.Fatalf("ParseExecutionPolicy: %v", err)
	}
	if p == nil {
		t.Fatal("policy is nil, want one that restricts images")
	}
	for _, img := range []string{"registry.example.com/team-a/etl:v1", "busybox:1.36", ""} {
		if err := p.CheckImage(img); err != nil {
			t.Errorf("CheckImage(%q) = %v, want nil", img, err)
		}
	}
	for _, img := range []string{"registry.example.com/team-b/etl:v1", "busybox:latest", "registry.example.com/team-a"} {
		err := p.CheckImage(img)
		if err == nil {
			t.Errorf("CheckImage(%q) accepted", img)
			continue
		}
		if !strings.Contains(err.Error(), "executor.defaults.execution_allowlist.images") {
			t.Errorf("error %q does not name the setting", err)
		}
	}
	spec := &DAGSpec{Image: "evil.example.com/miner:latest", Tasks: []TaskSpec{{TaskID: "a"}}}
	if err := p.CheckSpec(spec); err == nil || !strings.Contains(err.Error(), "evil.example.com/miner:latest") {
		t.Errorf("CheckSpec = %v, want a refusal naming the image", err)
	}
	var nilPolicy *ExecutionPolicy
	if err := nilPolicy.CheckImage("anything"); err != nil {
		t.Errorf("nil policy CheckImage = %v", err)
	}
}

func TestExecutionPolicyRejectsBadIdentityConfig(t *testing.T) {
	for name, cfg := range map[string]ExecutionPolicyConfig{
		"blank sa":       {ServiceAccounts: []string{" "}},
		"blank image":    {Images: []string{""}},
		"star in middle": {Images: []string{"registry/*/etl"}},
	} {
		if _, err := ParseExecutionPolicy(cfg); err == nil {
			t.Errorf("%s: ParseExecutionPolicy accepted %+v", name, cfg)
		}
	}
}
