package executor

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// overrideReporter is a fakeReporter that also implements
// InfraOverrideReporter: active reports what SucceedTaskIfActive returns,
// overridable what SucceedTaskOverInfraMark does.
type overrideReporter struct {
	fakeReporter
	active      bool
	overridable bool
	activeCalls int
	overrides   []Attempt
}

func (o *overrideReporter) SucceedTaskIfActive(ctx context.Context, id string, tryNumber, epoch int) (bool, error) {
	o.activeCalls++
	if o.active {
		return true, o.SucceedTask(ctx, id, tryNumber, epoch)
	}
	return false, nil
}

func (o *overrideReporter) SucceedTaskOverInfraMark(_ context.Context, _ string, tryNumber, epoch int) (InfraOverride, bool, error) {
	o.overrides = append(o.overrides, Attempt{TryNumber: tryNumber, AttemptEpoch: epoch})
	if !o.overridable {
		return InfraOverride{}, false, nil
	}
	return InfraOverride{Mark: "agent_lost", TenantID: "tn", DagID: "etl", DagRunID: "r1", TaskID: "extract"}, true, nil
}

type overrideCounter struct{ marks []string }

func (c *overrideCounter) RecordInfraOverride(mark string) { c.marks = append(c.marks, mark) }

type markerSink struct {
	refs   []logs.Ref
	events []logs.Event
}

func (m *markerSink) AppendEvent(ref logs.Ref, ev logs.Event) error {
	m.refs, m.events = append(m.refs, ref), append(m.events, ev)
	return nil
}

func overrideSweep(t *testing.T, rep *overrideReporter, pod *corev1.Pod) (*overrideCounter, *markerSink) {
	t.Helper()
	r := NewReconciler(fake.NewClientset(pod), "leoflow", rep)
	counter, sink := &overrideCounter{}, &markerSink{}
	r.SetInfraOverrideObservers(counter, sink)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return counter, sink
}

// TestReconcilerOverridesAnInfraMarkWithASuccessRecord: the active settle
// found nothing active, so the reconciler falls back to the override, for the
// pod's own attempt, and the recovered success is logged and metered once.
func TestReconcilerOverridesAnInfraMarkWithASuccessRecord(t *testing.T) {
	rep := &overrideReporter{overridable: true}
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), taskoutcome.Succeeded())
	pod.Annotations = map[string]string{"leoflow.io/task-instance-id": "ti-1"}
	counter, sink := overrideSweep(t, rep, pod)

	if len(rep.overrides) != 1 || rep.overrides[0] != (Attempt{TryNumber: 1, AttemptEpoch: 3}) {
		t.Fatalf("want one override for the pod's (try, epoch), got %+v", rep.overrides)
	}
	if len(counter.marks) != 1 || counter.marks[0] != "agent_lost" {
		t.Errorf("the override must be metered once by mark, got %v", counter.marks)
	}
	if len(sink.events) != 1 {
		t.Fatalf("the override must append one log line, got %d", len(sink.events))
	}
	ev, ref := sink.events[0], sink.refs[0]
	if ev.Stream != "system" || !strings.Contains(ev.Message, "outcome recovered from the durable record over agent_lost") {
		t.Errorf("log line = %+v", ev)
	}
	if ref != (logs.Ref{TenantID: "tn", DagID: "etl", RunID: "r1", TaskID: "extract", TryNumber: 1, AttemptEpoch: 3}) {
		t.Errorf("the line must go to the attempt's own log, got %+v", ref)
	}
}

// TestReconcilerOverrideOnlyAfterAZeroRowSettle: an active row settles as
// before, and the override is never tried.
func TestReconcilerOverrideOnlyAfterAZeroRowSettle(t *testing.T) {
	rep := &overrideReporter{active: true, overridable: true}
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), taskoutcome.Succeeded())
	pod.Annotations = map[string]string{"leoflow.io/task-instance-id": "ti-1"}
	counter, sink := overrideSweep(t, rep, pod)
	if rep.activeCalls != 1 || len(rep.overrides) != 0 || len(counter.marks) != 0 || len(sink.events) != 0 {
		t.Fatalf("an active settle must not fall back: active=%d overrides=%v marks=%v lines=%d",
			rep.activeCalls, rep.overrides, counter.marks, len(sink.events))
	}
}

// TestReconcilerNeverOverridesWithAFailedRecord: the reap's own teardown makes
// the agent write FAILED, so a FAILED record never overrides an infra mark.
func TestReconcilerNeverOverridesWithAFailedRecord(t *testing.T) {
	rep := &overrideReporter{overridable: true}
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), taskoutcome.FailedWith(1))
	pod.Annotations = map[string]string{"leoflow.io/task-instance-id": "ti-1"}
	overrideSweep(t, rep, pod)
	if len(rep.overrides) != 0 {
		t.Fatalf("a FAILED record must never override, got %+v", rep.overrides)
	}
}

// TestReconcilerNeverOverridesWithoutARecord: a pod that succeeded by phase
// alone carries no durable record, so it never overrides an infra mark.
func TestReconcilerNeverOverridesWithoutARecord(t *testing.T) {
	rep := &overrideReporter{overridable: true}
	pod := epochPod("p", "r1", "extract", 1, 3, corev1.PodSucceeded)
	pod.Annotations = map[string]string{"leoflow.io/task-instance-id": "ti-1"}
	overrideSweep(t, rep, pod)
	if len(rep.overrides) != 0 {
		t.Fatalf("a phase-only success must never override, got %+v", rep.overrides)
	}
}

// TestReconcilerOverrideNoOpIsSilent: when the guard refuses (confirmed, past
// the valve, finalized run), nothing is logged or metered.
func TestReconcilerOverrideNoOpIsSilent(t *testing.T) {
	rep := &overrideReporter{}
	pod := withRecord(epochPod("p", "r1", "extract", 1, 3, corev1.PodFailed), taskoutcome.Succeeded())
	pod.Annotations = map[string]string{"leoflow.io/task-instance-id": "ti-1"}
	counter, sink := overrideSweep(t, rep, pod)
	if len(rep.overrides) != 1 || len(counter.marks) != 0 || len(sink.events) != 0 {
		t.Fatalf("a refused override is silent: overrides=%v marks=%v lines=%d", rep.overrides, counter.marks, len(sink.events))
	}
}
