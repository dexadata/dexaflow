package dispatch

import (
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func mustForcePolicy(t *testing.T, doc string) *ForcePolicy {
	t.Helper()
	p, err := ParseForcePolicy([]byte(doc))
	if err != nil {
		t.Fatalf("ParseForcePolicy: %v", err)
	}
	return p
}

func TestParseForcePolicyEmptyIsNil(t *testing.T) {
	for _, doc := range []string{"", "  \n", "null", "{}", "force: {}", "force: null"} {
		p, err := ParseForcePolicy([]byte(doc))
		if err != nil {
			t.Fatalf("%q: %v", doc, err)
		}
		if p != nil {
			t.Fatalf("%q: want nil policy, got %+v", doc, p)
		}
	}
}

func TestParseForcePolicyRefusesBadDocuments(t *testing.T) {
	cases := map[string]string{
		"unknown top key":         "forse:\n  runtime_class_name: gvisor\n",
		"unknown force key":       "force:\n  runtime_class: gvisor\n",
		"bad runtime class":       "force:\n  runtime_class_name: Not_Valid\n",
		"bad service account":     "force:\n  service_account: UPPER\n",
		"reserved selector key":   "force:\n  node_selector:\n    leoflow.io/pool: a\n",
		"reserved dexaflow key":   "force:\n  node_selector:\n    dexaflow.io/pool: a\n",
		"bad selector key":        "force:\n  node_selector:\n    'bad key': a\n",
		"bad selector value":      "force:\n  node_selector:\n    pool: 'has space'\n",
		"toleration bad operator": "force:\n  tolerations:\n    - key: a\n      operator: Maybe\n",
		"toleration bad effect":   "force:\n  tolerations:\n    - key: a\n      effect: Sometimes\n",
		"toleration exists value": "force:\n  tolerations:\n    - key: a\n      operator: Exists\n      value: b\n",
		"toleration seconds":      "force:\n  tolerations:\n    - key: a\n      effect: NoSchedule\n      toleration_seconds: 5\n",
		"toleration no key":       "force:\n  tolerations:\n    - operator: Equal\n      value: b\n",
		"toleration unknown key":  "force:\n  tolerations:\n    - key: a\n      tolerationSeconds: 5\n",
		"restrict rule from 0063": "force:\n  runtime_class_name: gvisor\nimages:\n  allowed: [x/]\n",
	}
	for name, doc := range cases {
		if _, err := ParseForcePolicy([]byte(doc)); err == nil {
			t.Errorf("%s: want an error for %q", name, doc)
		} else if !strings.Contains(err.Error(), "executor.policy") {
			t.Errorf("%s: error %q does not name executor.policy", name, err)
		}
	}
}

func TestForcePolicyApplyForcesEveryField(t *testing.T) {
	p := mustForcePolicy(t, `
force:
  runtime_class_name: gvisor
  service_account: sandboxed
  node_selector:
    pool: sandbox
    kubernetes.io/os: linux
  tolerations:
    - key: sandbox
      operator: Equal
      value: "true"
      effect: NoSchedule
    - key: node.kubernetes.io/not-ready
      operator: Exists
      effect: NoExecute
      toleration_seconds: 30
`)
	own := "runc"
	task := domain.TaskSpec{TaskID: "t", Execution: &domain.Execution{
		ServiceAccount:   "mine",
		RuntimeClassName: &own,
		NodeSelector:     map[string]string{"pool": "fast", "zone": "a"},
		Tolerations:      []map[string]any{{"key": "gpu", "operator": "Exists"}},
	}}
	got, changed := p.Apply(task)
	if !changed {
		t.Fatal("Apply reported no change")
	}
	e := got.Execution
	if e.RuntimeClassName == nil || *e.RuntimeClassName != "gvisor" {
		t.Errorf("runtime class = %v, want gvisor", e.RuntimeClassName)
	}
	if e.ServiceAccount != "sandboxed" {
		t.Errorf("service account = %q, want sandboxed", e.ServiceAccount)
	}
	want := map[string]string{"pool": "sandbox", "zone": "a", "kubernetes.io/os": "linux"}
	if len(e.NodeSelector) != len(want) {
		t.Errorf("node selector = %v, want %v", e.NodeSelector, want)
	}
	for k, v := range want {
		if e.NodeSelector[k] != v {
			t.Errorf("node selector[%q] = %q, want %q", k, e.NodeSelector[k], v)
		}
	}
	if len(e.Tolerations) != 3 {
		t.Fatalf("tolerations = %v, want the task's one plus the policy's two", e.Tolerations)
	}
	if e.Tolerations[0]["key"] != "gpu" {
		t.Errorf("the task's own toleration must come first, got %v", e.Tolerations[0])
	}
	last := e.Tolerations[2]
	if last["key"] != "node.kubernetes.io/not-ready" || last["operator"] != "Exists" || last["effect"] != "NoExecute" || last["tolerationSeconds"] != int64(30) {
		t.Errorf("forced toleration = %v", last)
	}
	if _, ok := last["value"]; ok {
		t.Errorf("an empty value must be left out, got %v", last)
	}
}

func TestForcePolicyApplyNeverWritesThroughTheTask(t *testing.T) {
	p := mustForcePolicy(t, "force:\n  runtime_class_name: gvisor\n  node_selector:\n    pool: sandbox\n  tolerations:\n    - key: sandbox\n      operator: Exists\n")
	own := "runc"
	exec := &domain.Execution{
		RuntimeClassName: &own,
		NodeSelector:     map[string]string{"pool": "fast"},
		Tolerations:      []map[string]any{{"key": "gpu", "operator": "Exists"}},
	}
	task := domain.TaskSpec{TaskID: "t", Execution: exec}
	got, _ := p.Apply(task)
	if exec.NodeSelector["pool"] != "fast" || len(exec.Tolerations) != 1 || *exec.RuntimeClassName != "runc" {
		t.Fatalf("Apply changed the cached spec: %+v", exec)
	}
	got.Execution.NodeSelector["extra"] = "x"
	got.Execution.Tolerations[0]["key"] = "changed"
	if _, ok := exec.NodeSelector["extra"]; ok || exec.Tolerations[0]["key"] != "gpu" {
		t.Fatal("the result shares a map with the task")
	}
	again, _ := p.Apply(task)
	if again.Execution.NodeSelector["pool"] != "sandbox" || len(again.Execution.Tolerations) != 2 {
		t.Fatalf("a second Apply saw a mutated policy: %+v", again.Execution)
	}
}

func TestForcePolicyApplyOnTaskWithoutExecution(t *testing.T) {
	p := mustForcePolicy(t, "force:\n  service_account: sandboxed\n")
	got, changed := p.Apply(domain.TaskSpec{TaskID: "t"})
	if !changed || got.Execution == nil || got.Execution.ServiceAccount != "sandboxed" {
		t.Fatalf("got %+v changed=%v", got.Execution, changed)
	}
	if got.Execution.RuntimeClassName != nil || got.Execution.NodeSelector != nil || got.Execution.Tolerations != nil {
		t.Fatalf("unset rules must leave their field alone: %+v", got.Execution)
	}
}

func TestForcePolicyNilChangesNothing(t *testing.T) {
	var p *ForcePolicy
	task := domain.TaskSpec{TaskID: "t"}
	got, changed := p.Apply(task)
	if changed || got.Execution != nil {
		t.Fatalf("nil policy changed the task: %+v", got)
	}
	if p.ServiceAccount() != "" {
		t.Fatal("nil policy forces a service account")
	}
}

func TestForcePolicySummaryAndServiceAccount(t *testing.T) {
	var none *ForcePolicy
	if none.Summary() != nil || none.ServiceAccount() != "" {
		t.Fatalf("nil policy: summary %v, service account %q", none.Summary(), none.ServiceAccount())
	}
	p := mustForcePolicy(t, `
force:
  runtime_class_name: gvisor
  service_account: dexaflow-task
  node_selector: {pool: tasks}
  tolerations: [{key: dedicated, operator: Equal, value: tasks, effect: NoSchedule}]
`)
	if got := strings.Join(p.Summary(), ","); got != "runtime_class_name,service_account,node_selector,tolerations" {
		t.Fatalf("summary = %q", got)
	}
	if p.ServiceAccount() != "dexaflow-task" {
		t.Fatalf("service account = %q", p.ServiceAccount())
	}
	if got := mustForcePolicy(t, "force: {node_selector: {pool: tasks}}").Summary(); strings.Join(got, ",") != "node_selector" {
		t.Fatalf("node selector only: summary = %v", got)
	}
}

func TestParseForcePolicyListsEveryProblemInOrder(t *testing.T) {
	doc := "force: {runtime_class_name: Bad_Name, service_account: Also_Bad}"
	_, first := ParseForcePolicy([]byte(doc))
	if first == nil {
		t.Fatal("want an error")
	}
	msg := first.Error()
	rc, sa := strings.Index(msg, "runtime_class_name"), strings.Index(msg, "service_account")
	if rc < 0 || sa < 0 || rc > sa {
		t.Fatalf("want both problems, sorted: %s", msg)
	}
	for range 20 {
		if _, err := ParseForcePolicy([]byte(doc)); err.Error() != msg {
			t.Fatalf("error text changed between parses:\n%s\n%s", msg, err)
		}
	}
}
