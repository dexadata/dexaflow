package executor

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// TestRecordOfAnotherEpochCountsAsNoRecord (ADR 0052 amendment): the pod's
// labels, which the control plane wrote, name the attempt. A record that names
// a different epoch did not come from this execution, so it counts as no
// record: the pod settles by phase, and a SUCCESS in it never settles success.
func TestRecordOfAnotherEpochCountsAsNoRecord(t *testing.T) {
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), taskoutcome.Succeeded().WithAttemptEpoch(2))
	v := classifyPod(pod)
	if v.fromRecord || v.settle != settleFailed {
		t.Fatalf("a mismatching record must fall back to phase, got %+v", v)
	}
}

// TestRecordOfTheSameEpochIsTrusted: a record naming the pod's own epoch, and
// a record with no epoch (an older agent, or epoch 0), drive the settle.
func TestRecordOfTheSameEpochIsTrusted(t *testing.T) {
	for name, rec := range map[string]taskoutcome.Record{
		"same epoch": taskoutcome.Succeeded().WithAttemptEpoch(3),
		"no epoch":   taskoutcome.Succeeded(),
	} {
		t.Run(name, func(t *testing.T) {
			v := classifyPod(withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), rec))
			if !v.fromRecord || v.settle != settleSucceeded {
				t.Fatalf("the record must settle success, got %+v", v)
			}
		})
	}
}

// TestUnlabeledPodTrustsOnlyAnEpochZeroRecord: a pod from before the epoch
// label is epoch 0, so a record naming another epoch is not its own.
func TestUnlabeledPodTrustsOnlyAnEpochZeroRecord(t *testing.T) {
	pod := withRecord(epochPod("p", "r1", "extract", 1, -1, corev1.PodFailed), taskoutcome.Succeeded().WithAttemptEpoch(1))
	if v := classifyPod(pod); v.fromRecord {
		t.Fatalf("an epoch-1 record on an unlabeled pod must not be trusted, got %+v", v)
	}
}

// TestInfraConfirmationIgnoresAMismatchingRecord: the confirmation pass reads
// the same rule, so another execution's SUCCESS cannot hold a mark back.
func TestInfraConfirmationIgnoresAMismatchingRecord(t *testing.T) {
	pod := withRecord(epochPod("p", "run-a", "extract", 1, 2, corev1.PodFailed), taskoutcome.Succeeded().WithAttemptEpoch(1))
	if got := attemptEvidence([]*corev1.Pod{pod}, Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2}); got != evidenceNone {
		t.Fatalf("a mismatching record is no evidence, got %v", got)
	}
}

// TestRecordWithEpochOnUnparseableLabelIsNotTrusted: a pod whose epoch label
// cannot be parsed matches no epoch, so a record that names one is not its own.
func TestRecordWithEpochOnUnparseableLabelIsNotTrusted(t *testing.T) {
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), taskoutcome.Succeeded().WithAttemptEpoch(3))
	pod.Labels[podLabelAttemptEpoch] = "not-a-number"
	if v := classifyPod(pod); v.fromRecord {
		t.Fatalf("a record with an epoch on an unparseable label must not be trusted, got %+v", v)
	}
}

// TestTeardownDoesNotTreatAMismatchingRecordAsTerminal: the teardown's terminal
// test reads the same rule, so another execution's record does not by itself
// mark a pod as finished.
func TestTeardownDoesNotTreatAMismatchingRecordAsTerminal(t *testing.T) {
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodRunning), taskoutcome.Succeeded().WithAttemptEpoch(2))
	if terminalForTeardown(pod) {
		t.Fatal("a record of another epoch must not make a running pod terminal for teardown")
	}
	same := withRecord(epochPod("q", "r1", "extract", 1, 3, corev1.PodRunning), taskoutcome.Succeeded().WithAttemptEpoch(3))
	if !terminalForTeardown(same) {
		t.Fatal("a record of the pod's own epoch makes it terminal for teardown")
	}
}
