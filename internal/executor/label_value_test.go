package executor

import (
	"context"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
)

// longID is a realistic id past the 63-character label value limit: a deeply
// nested task group, or a run id with a long logical date suffix.
var longID = "ingest_group.customer_accounts_subgroup.load_customer_accounts_into_warehouse_partitioned"

func assertValidLabels(t *testing.T, what string, labels map[string]string) {
	t.Helper()
	for k, v := range labels {
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			t.Errorf("%s label %s=%q is not a valid label value: %v", what, k, v, errs)
		}
	}
}

// Kubernetes rejects a label value longer than 63 characters at CREATE, so a
// long dag, task or run id used to fail the dispatch permanently. Every label
// BuildPod stamps must be a valid label value whatever the id length.
func TestBuildPodLongIDsYieldValidLabelValues(t *testing.T) {
	pod := BuildPod(Request{
		DagID: "dag_" + longID, TaskID: longID, RunID: "manual__" + longID,
		TenantID: "tenant_" + longID, TryNumber: 1, Image: "python:3.12",
	})
	assertValidLabels(t, "task pod", pod.Labels)
}

// Ids that already fit are stamped exactly as before, so pods created before
// this change keep matching the reapers' selectors across an upgrade.
func TestLabelValueKeepsShortIDsUnchanged(t *testing.T) {
	for _, id := range []string{"extract", "manual__2026-10-02T17:00:00+00:00", strings.Repeat("a", 63)} {
		if got, want := labelValue(id), sanitizeLabel(id); got != want {
			t.Errorf("labelValue(%q) = %q, want the unchanged %q", id, got, want)
		}
	}
}

// Two long ids that share the truncated prefix must still get distinct label
// values, or a reaper selecting one task's pod could match its sibling's.
func TestLabelValueKeepsLongIDsDistinct(t *testing.T) {
	a, b := labelValue(longID+"_a"), labelValue(longID+"_b")
	if a == b {
		t.Fatalf("distinct long ids collapsed to the same label value %q", a)
	}
	for _, v := range []string{a, b} {
		if errs := validation.IsValidLabelValue(v); len(errs) > 0 {
			t.Errorf("label value %q invalid: %v", v, errs)
		}
	}
}

// The reaper teardown selects on the same label values BuildPod stamps, so a
// task pod with long ids must still be found and deleted.
func TestDeleteTaskPodFindsPodWithLongIDs(t *testing.T) {
	runID, taskID := "manual__"+longID, longID
	pod := BuildPod(Request{DagID: "etl", TaskID: taskID, RunID: runID, TryNumber: 2, Image: "python:3.12"})
	pod.Namespace = "leoflow"
	cs := fake.NewSimpleClientset(pod)
	e := NewKubernetesExecutor(cs, "leoflow")

	if err := e.DeleteTaskPod(context.Background(), runID, taskID, 2); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	if got := podNames(t, cs); got[pod.Name] {
		t.Errorf("the task pod with long ids was not deleted: %v", got)
	}
}

// The per-run staging PVC carries the same run, dag and tenant labels.
func TestStagingClaimLongIDsYieldValidLabelValues(t *testing.T) {
	cs := fake.NewSimpleClientset()
	e := NewKubernetesExecutor(cs, "leoflow")
	req := Request{DagID: "dag_" + longID, RunID: "manual__" + longID, TenantID: "tenant_" + longID, StagingClaim: "leoflow-staging-x"}
	if err := e.ensureStagingClaim(context.Background(), req); err != nil {
		t.Fatalf("ensureStagingClaim: %v", err)
	}
	pvc, err := cs.CoreV1().PersistentVolumeClaims("leoflow").Get(context.Background(), "leoflow-staging-x", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("getting the staging claim: %v", err)
	}
	assertValidLabels(t, "staging claim", pvc.Labels)
}
