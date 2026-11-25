package executor

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"
)

// branchReporter is a fakeReporter that also settles branch successes.
type branchReporter struct {
	fakeReporter
	decisions map[string]domain.BranchDecision
}

func (b *branchReporter) SucceedBranchTaskIfActive(_ context.Context, id string, tryNumber, _ int, d domain.BranchDecision) (bool, error) {
	if b.decisions == nil {
		b.decisions = map[string]domain.BranchDecision{}
	}
	b.decisions[id] = d
	b.put(id, settledOutcome{kind: settleSucceeded, tryNumber: tryNumber})
	return true, nil
}

func branchPod(name, tiID string, rec taskoutcome.Record) *corev1.Pod {
	pod := withRecord(managedPod(name, tiID, corev1.PodFailed), rec)
	pod.Labels[podLabelBranch] = "true"
	return pod
}

func reconcileWith(t *testing.T, reporter OutcomeReporter, pods ...*corev1.Pod) {
	t.Helper()
	cs := fake.NewClientset()
	for _, p := range pods {
		if err := cs.Tracker().Add(p); err != nil {
			t.Fatal(err)
		}
	}
	if err := NewReconciler(cs, "leoflow", reporter).Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// ADR 0064 D6: a branch task's recovered success settles with its decision.
func TestReconcileSettlesABranchSuccessWithItsDecision(t *testing.T) {
	reporter := &branchReporter{}
	reconcileWith(t, reporter, branchPod("p", "ti", taskoutcome.Succeeded().WithBranch(taskoutcome.BranchDecision{FollowTaskIDs: []string{"a"}})))
	if want := (domain.BranchDecision{FollowTaskIDs: []string{"a"}}); !reflect.DeepEqual(reporter.decisions["ti"], want) {
		t.Fatalf("decision = %+v, want %+v", reporter.decisions["ti"], want)
	}
}

// A success record without a decision on a branch pod is settled failed,
// never as a success that would run every branch.
func TestReconcileFailsABranchSuccessWithoutDecision(t *testing.T) {
	reporter := &branchReporter{}
	reconcileWith(t, reporter, branchPod("p", "ti", taskoutcome.Succeeded()))
	got := reporter.settled["ti"]
	if got.kind != settleFailed || !strings.HasPrefix(got.reason, "branch_decision_missing") {
		t.Fatalf("settled %+v, want failed branch_decision_missing", got)
	}
	if len(reporter.decisions) != 0 {
		t.Errorf("decisions = %v, want none", reporter.decisions)
	}
}

// A decision in the record of a pod that is not a branch task is ignored.
func TestReconcileIgnoresADecisionOnAPlainTask(t *testing.T) {
	reporter := &branchReporter{}
	reconcileWith(t, reporter, withRecord(managedPod("p", "ti", corev1.PodFailed),
		taskoutcome.Succeeded().WithBranch(taskoutcome.BranchDecision{})))
	if reporter.settled["ti"].kind != settleSucceeded || len(reporter.decisions) != 0 {
		t.Fatalf("settled %+v, decisions %v, want a plain success", reporter.settled["ti"], reporter.decisions)
	}
}

// A reporter that cannot store a decision settles nothing for a branch
// success: the row is left to the reapers rather than succeeded without one.
func TestReconcileLeavesABranchSuccessWhenTheReporterCannotStoreIt(t *testing.T) {
	reporter := &fakeReporter{}
	reconcileWith(t, reporter, branchPod("p", "ti", taskoutcome.Succeeded().WithBranch(taskoutcome.BranchDecision{})))
	if got, ok := reporter.settled["ti"]; ok {
		t.Fatalf("settled %+v, want nothing", got)
	}
}

func TestBuildPodLabelsABranchTask(t *testing.T) {
	req := Request{TaskInstanceID: "ti", RunID: "r", TaskID: "pick", Branch: true, TimeoutSeconds: int(time.Minute.Seconds())}
	if got := BuildPod(req).Labels[podLabelBranch]; got != "true" {
		t.Errorf("branch label = %q, want true", got)
	}
	req.Branch = false
	if _, ok := BuildPod(req).Labels[podLabelBranch]; ok {
		t.Error("a task that is not a branch must carry no branch label")
	}
}
