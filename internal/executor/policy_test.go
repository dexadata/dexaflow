package executor

import (
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func mustPolicy(t *testing.T, doc string) *Policy {
	t.Helper()
	p, err := ParsePolicy([]byte(doc))
	if err != nil {
		t.Fatalf("ParsePolicy: %v", err)
	}
	return p
}

func strPtr(s string) *string { return &s }

func TestParsePolicyEmptyIsNil(t *testing.T) {
	for _, doc := range []string{"", "   \n", "{}"} {
		p, err := ParsePolicy([]byte(doc))
		if err != nil || !p.IsZero() {
			t.Errorf("ParsePolicy(%q) = %+v, %v; want a zero policy", doc, p, err)
		}
	}
}

func TestParsePolicyRejectsInvalidDocuments(t *testing.T) {
	for name, doc := range map[string]string{
		"unknown key":           "runtime_class: gvisor",
		"bad quantity":          "resources:\n  max:\n    memory: lots",
		"empty image prefix":    "images:\n  allowed: [\"\"]",
		"empty label prefix":    "metadata:\n  allowed_label_prefixes: [\"\"]",
		"bad toleration":        "placement:\n  tolerations:\n    - {key: pool, operator: Sideways}",
		"force sa not allowed":  "service_account:\n  force: a\n  allowed: [b]",
		"empty allowed sa name": "service_account:\n  allowed: [\"\"]",
	} {
		if _, err := ParsePolicy([]byte(doc)); err == nil {
			t.Errorf("%s: ParsePolicy accepted %q", name, doc)
		}
	}
}

func TestZeroPolicyLeavesTheRequestUntouched(t *testing.T) {
	req := Request{Image: "any/image:1", Execution: domain.Execution{
		ServiceAccount: "x", NodeSelector: map[string]string{"a": "b"},
		Annotations: map[string]string{"k": "v"},
	}}
	var p *Policy
	if err := p.Apply(&req); err != nil {
		t.Fatalf("nil policy: %v", err)
	}
	if err := (&Policy{}).Apply(&req); err != nil {
		t.Fatalf("zero policy: %v", err)
	}
	if req.Execution.ServiceAccount != "x" || req.Execution.NodeSelector["a"] != "b" || req.Execution.RuntimeClassName != nil {
		t.Errorf("request changed: %+v", req.Execution)
	}
}

func TestPolicyForcesRuntimeClassAndServiceAccount(t *testing.T) {
	p := mustPolicy(t, "runtime_class_name: gvisor\nservice_account:\n  force: tenant-runner\n")
	req := Request{Execution: domain.Execution{RuntimeClassName: strPtr("runc"), ServiceAccount: "admin-sa"}}
	if err := p.Apply(&req); err != nil {
		t.Fatal(err)
	}
	if req.Execution.RuntimeClassName == nil || *req.Execution.RuntimeClassName != "gvisor" {
		t.Errorf("runtime class = %v, want gvisor", req.Execution.RuntimeClassName)
	}
	if req.Execution.ServiceAccount != "tenant-runner" {
		t.Errorf("service account = %q, want tenant-runner", req.Execution.ServiceAccount)
	}
}

func TestPolicyRestrictsServiceAccounts(t *testing.T) {
	p := mustPolicy(t, "service_account:\n  allowed: [etl-reader, etl-writer]\n")
	ok := Request{Execution: domain.Execution{ServiceAccount: "etl-reader"}}
	if err := p.Apply(&ok); err != nil {
		t.Errorf("allowed SA refused: %v", err)
	}
	for _, sa := range []string{"admin", ""} {
		bad := Request{Execution: domain.Execution{ServiceAccount: sa}}
		if err := p.Apply(&bad); !errors.Is(err, ErrPolicyRefused) {
			t.Errorf("SA %q: err = %v, want ErrPolicyRefused", sa, err)
		}
	}
}

func TestPolicyForcesPlacementWithPolicyKeysWinning(t *testing.T) {
	p := mustPolicy(t, `placement:
  node_selector: {pool: tasks, "kubernetes.io/os": linux}
  tolerations:
    - {key: pool, operator: Equal, value: tasks, effect: NoSchedule}
`)
	req := Request{Execution: domain.Execution{
		NodeSelector: map[string]string{"pool": "prod", "disk": "ssd"},
		Tolerations:  []map[string]any{{"key": "gpu", "operator": "Exists"}},
	}}
	if err := p.Apply(&req); err != nil {
		t.Fatal(err)
	}
	ns := req.Execution.NodeSelector
	if ns["pool"] != "tasks" || ns["kubernetes.io/os"] != "linux" || ns["disk"] != "ssd" {
		t.Errorf("node selector = %v", ns)
	}
	if len(req.Execution.Tolerations) != 2 || req.Execution.Tolerations[1]["key"] != "pool" {
		t.Errorf("tolerations = %v, want the DAG's plus the policy's", req.Execution.Tolerations)
	}
}

func TestPolicyForcedPlacementDoesNotAliasThePolicy(t *testing.T) {
	p := mustPolicy(t, "placement:\n  node_selector: {pool: tasks}\n")
	a, b := Request{}, Request{}
	if err := p.Apply(&a); err != nil {
		t.Fatal(err)
	}
	a.Execution.NodeSelector["pool"] = "mutated"
	if err := p.Apply(&b); err != nil {
		t.Fatal(err)
	}
	if b.Execution.NodeSelector["pool"] != "tasks" {
		t.Errorf("a request mutated the shared policy map: %v", b.Execution.NodeSelector)
	}
}

func TestPolicyRefusesDAGPlacementWhenLocked(t *testing.T) {
	p := mustPolicy(t, "placement:\n  allow_dag_placement: false\n")
	cases := map[string]domain.Execution{
		"node_selector": {NodeSelector: map[string]string{"a": "b"}},
		"tolerations":   {Tolerations: []map[string]any{{"key": "a"}}},
		"affinity":      {Affinity: map[string]any{"nodeAffinity": map[string]any{}}},
		"topology":      {TopologySpreadConstraints: []map[string]any{{"maxSkew": 1}}},
	}
	for name, ex := range cases {
		req := Request{Execution: ex}
		err := p.Apply(&req)
		if !errors.Is(err, ErrPolicyRefused) {
			t.Errorf("%s: err = %v, want ErrPolicyRefused", name, err)
		}
	}
	if err := p.Apply(&Request{}); err != nil {
		t.Errorf("no DAG placement refused: %v", err)
	}
}

func TestPolicyRestrictsPriorityClasses(t *testing.T) {
	p := mustPolicy(t, "placement:\n  allowed_priority_classes: [batch-low]\n")
	for pc, wantErr := range map[string]bool{"": false, "batch-low": false, "system-cluster-critical": true} {
		err := p.Apply(&Request{Execution: domain.Execution{PriorityClassName: pc}})
		if got := errors.Is(err, ErrPolicyRefused); got != wantErr {
			t.Errorf("priority class %q: err = %v, want refused=%v", pc, err, wantErr)
		}
	}
}

func TestPolicyRestrictsMetadataPrefixes(t *testing.T) {
	p := mustPolicy(t, "metadata:\n  allowed_label_prefixes: [team.example.com/]\n  allowed_annotation_prefixes: []\n")
	if err := p.Apply(&Request{Execution: domain.Execution{Labels: map[string]string{"team.example.com/owner": "x"}}}); err != nil {
		t.Errorf("allowed label refused: %v", err)
	}
	if err := p.Apply(&Request{Execution: domain.Execution{Labels: map[string]string{"app": "x"}}}); !errors.Is(err, ErrPolicyRefused) {
		t.Errorf("label outside prefixes: err = %v", err)
	}
	if err := p.Apply(&Request{Execution: domain.Execution{Annotations: map[string]string{"sidecar.istio.io/inject": "false"}}}); !errors.Is(err, ErrPolicyRefused) {
		t.Errorf("annotation with an empty allow list: err = %v", err)
	}
	// Unset prefixes are no rule at all.
	open := mustPolicy(t, "runtime_class_name: gvisor\n")
	if err := open.Apply(&Request{Execution: domain.Execution{Annotations: map[string]string{"any": "x"}}}); err != nil {
		t.Errorf("annotation refused with no metadata rule: %v", err)
	}
}

func TestPolicyResourceCeiling(t *testing.T) {
	p := mustPolicy(t, "resources:\n  max: {cpu: \"2\", memory: 4Gi}\n")
	q := func(cpu, mem string) *domain.ResourceQuantity { return &domain.ResourceQuantity{CPU: cpu, Memory: mem} }

	within := Request{Resources: domain.Resources{Requests: q("500m", "1Gi"), Limits: q("2", "4Gi")}}
	if err := p.Apply(&within); err != nil {
		t.Errorf("within the ceiling: %v", err)
	}
	for name, res := range map[string]domain.Resources{
		"limit over":      {Limits: q("3", "1Gi")},
		"request over":    {Requests: q("100m", "8Gi")},
		"unparseable":     {Limits: q("two", "")},
		"request no unit": {Requests: q("", "5000000000")},
	} {
		req := Request{Resources: res}
		if err := p.Apply(&req); !errors.Is(err, ErrPolicyRefused) {
			t.Errorf("%s: err = %v, want ErrPolicyRefused", name, err)
		}
	}

	// A missing limit is filled with the ceiling, so the pod is never unbounded.
	empty := Request{}
	if err := p.Apply(&empty); err != nil {
		t.Fatal(err)
	}
	if l := empty.Resources.Limits; l == nil || l.CPU != "2" || l.Memory != "4Gi" {
		t.Errorf("limits = %+v, want the ceiling", l)
	}
	partial := Request{Resources: domain.Resources{Limits: q("1", "")}}
	if err := p.Apply(&partial); err != nil {
		t.Fatal(err)
	}
	if l := partial.Resources.Limits; l.CPU != "1" || l.Memory != "4Gi" {
		t.Errorf("limits = %+v, want cpu kept and memory filled", l)
	}
}

func TestPolicyCeilingDoesNotAliasTheRequestsLimits(t *testing.T) {
	p := mustPolicy(t, "resources:\n  max: {memory: 4Gi}\n")
	shared := &domain.ResourceQuantity{CPU: "1"}
	req := Request{Resources: domain.Resources{Limits: shared}}
	if err := p.Apply(&req); err != nil {
		t.Fatal(err)
	}
	if shared.Memory != "" {
		t.Errorf("Apply wrote through a shared ResourceQuantity: %+v", shared)
	}
}

func TestPolicyImageAllowList(t *testing.T) {
	p := mustPolicy(t, "images:\n  allowed: [registry.example.com/dags/, docker.io/library/python]\n")
	for img, wantErr := range map[string]bool{
		"registry.example.com/dags/etl:1":                false,
		"registry.example.com/dags/team/etl@sha256:abcd": false,
		"docker.io/library/python:3.12":                  false,
		"docker.io/library/python":                       false,
		"registry.example.com/dagsevil/etl:1":            true,
		"docker.io/library/python-evil:1":                true,
		"evil.io/registry.example.com/dags/x":            true,
		"":                                               true,
	} {
		err := p.Apply(&Request{Image: img})
		if got := errors.Is(err, ErrPolicyRefused); got != wantErr {
			t.Errorf("image %q: err = %v, want refused=%v", img, err, wantErr)
		}
	}
}

func TestPolicyRefusalNamesTheField(t *testing.T) {
	p := mustPolicy(t, "images:\n  allowed: [registry.example.com/]\n")
	err := p.Apply(&Request{Image: "evil.io/x:1"})
	if err == nil || !strings.Contains(err.Error(), "image") || !strings.Contains(err.Error(), "evil.io/x:1") {
		t.Errorf("refusal = %v, want it to name the field and value", err)
	}
}

func TestPolicyRequiresDedicatedPod(t *testing.T) {
	cases := map[string]bool{
		"":                                      false,
		"images:\n  allowed: [r/]\n":            false,
		"runtime_class_name: gvisor\n":          true,
		"placement:\n  node_selector: {a: b}\n": true,
		"placement:\n  tolerations: [{key: a, operator: Exists}]\n": true,
		"service_account:\n  force: x\n":                            true,
	}
	for doc, want := range cases {
		if got := mustPolicy(t, doc).RequiresDedicatedPod(); got != want {
			t.Errorf("%q: RequiresDedicatedPod = %v, want %v", doc, got, want)
		}
	}
}
