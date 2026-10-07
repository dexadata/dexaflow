package executor

import (
	"context"
	"strconv"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

// epochPod is taskPod plus the attempt-epoch label; epoch < 0 leaves the label
// off, the shape of a pod created before the epoch existed.
func epochPod(name, runID, taskID string, try, epoch int, phase corev1.PodPhase) *corev1.Pod {
	pod := taskPod(name, runID, taskID, try, phase)
	pod.UID = types.UID("uid-" + name)
	if epoch >= 0 {
		pod.Labels[podLabelAttemptEpoch] = strconv.Itoa(epoch)
	}
	return pod
}

// TestBuildPodStampsAttemptEpochLabel: every task pod carries the epoch the
// dispatcher claimed, next to the try, so teardown and the reconciler can tell
// two executions of one try apart (ADR 0051 amendment).
func TestBuildPodStampsAttemptEpochLabel(t *testing.T) {
	req := sampleReq()
	req.AttemptEpoch = 7
	pod := BuildPod(req)
	if got := pod.Labels["leoflow.io/attempt-epoch"]; got != "7" {
		t.Errorf("leoflow.io/attempt-epoch = %q, want 7", got)
	}
	if got := pod.Labels["leoflow.io/try-number"]; got != "1" {
		t.Errorf("try-number label must stay, got %q", got)
	}
}

// TestDeleteTaskPodDeletesOnlyTheMatchingEpoch is #901: two pods of one try
// (an infra re-place reuses it) are told apart by the epoch, so tearing down
// the reaped attempt never deletes its replacement. A pod with no epoch label
// is epoch 0.
func TestDeleteTaskPodDeletesOnlyTheMatchingEpoch(t *testing.T) {
	cs := fake.NewSimpleClientset(
		epochPod("legacy", "run-a", "extract", 1, -1, corev1.PodRunning),
		epochPod("e1", "run-a", "extract", 1, 1, corev1.PodRunning),
		epochPod("e2", "run-a", "extract", 1, 2, corev1.PodRunning),
	)
	e := NewKubernetesExecutor(cs, "leoflow")

	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 1}); err != nil {
		t.Fatalf("DeleteTaskPod(epoch 1): %v", err)
	}
	got := podNames(t, cs)
	if got["e1"] || !got["e2"] || !got["legacy"] {
		t.Fatalf("epoch 1 teardown must delete only e1, left %v", got)
	}
	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 0}); err != nil {
		t.Fatalf("DeleteTaskPod(epoch 0): %v", err)
	}
	got = podNames(t, cs)
	if got["legacy"] || !got["e2"] {
		t.Fatalf("epoch 0 teardown must delete only the unlabeled pod, left %v", got)
	}
}

// TestDeleteTaskPodDeletesByNameWithUIDPrecondition: the delete names the pod
// it listed and pins its UID, so a pod recreated under the same name between
// the list and the delete is never acted on (#901).
func TestDeleteTaskPodDeletesByNameWithUIDPrecondition(t *testing.T) {
	cs := fake.NewSimpleClientset(epochPod("victim", "run-a", "extract", 1, 3, corev1.PodRunning))
	var opts *metav1.DeleteOptions
	var name string
	cs.PrependReactor("delete", "pods", func(a k8stesting.Action) (bool, runtime.Object, error) {
		d := a.(k8stesting.DeleteActionImpl)
		name = d.GetName()
		o := d.GetDeleteOptions()
		opts = &o
		return false, nil, nil
	})
	e := NewKubernetesExecutor(cs, "leoflow")
	if err := e.DeleteTaskPod(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 3}); err != nil {
		t.Fatalf("DeleteTaskPod: %v", err)
	}
	if name != "victim" || opts == nil || opts.Preconditions == nil || opts.Preconditions.UID == nil || *opts.Preconditions.UID != "uid-victim" {
		t.Fatalf("delete must be by name with the listed UID as precondition, got name=%q opts=%+v", name, opts)
	}
}

// TestTaskPodPresenceFiltersOnAttemptEpoch: a live pod of a superseded epoch
// is not the presence of the current attempt, so it cannot defer that
// attempt's reap.
func TestTaskPodPresenceFiltersOnAttemptEpoch(t *testing.T) {
	cs := fake.NewSimpleClientset(epochPod("old", "run-a", "extract", 1, 1, corev1.PodPending))
	e := NewKubernetesExecutor(cs, "leoflow")
	got, err := e.TaskPodPresence(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2})
	if err != nil {
		t.Fatalf("TaskPodPresence: %v", err)
	}
	if got != PodPresenceAbsent {
		t.Errorf("a superseded epoch's pod must not count as the current attempt's presence, got %v", got)
	}
	got, err = e.TaskPodPresence(context.Background(), Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 1})
	if err != nil || got != PodPresenceLive {
		t.Errorf("the attempt's own pod is live, got %v err=%v", got, err)
	}
}

// TestCachedPodActiveFiltersOnAttemptEpoch: the informer fast path asks the
// same exact-attempt question as the live read.
func TestCachedPodActiveFiltersOnAttemptEpoch(t *testing.T) {
	cs := fake.NewClientset(epochPod("old", "run-a", "extract", 1, 1, corev1.PodPending))
	pi := NewPodInformer(cs, "leoflow")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pi.Start(ctx)
	if !pi.WaitForCacheSync(ctx) {
		t.Fatal("cache did not sync")
	}
	if pi.CachedPodActive(Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2}) {
		t.Errorf("a superseded epoch's pod must not make the current attempt active")
	}
	if !pi.CachedPodActive(Attempt{RunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 1}) {
		t.Errorf("the attempt's own pod must read active")
	}
}

// epochRecordingHeartbeatStore records the attempt each mark names.
type epochRecordingHeartbeatStore struct {
	candidates []AgentLostCandidate
	marks      []Attempt
}

func (f *epochRecordingHeartbeatStore) ListAgentLostCandidates(context.Context) ([]AgentLostCandidate, error) {
	return f.candidates, nil
}

func (f *epochRecordingHeartbeatStore) MarkTaskAgentLost(_ context.Context, _ string, try, epoch int) (bool, error) {
	f.marks = append(f.marks, Attempt{TryNumber: try, AttemptEpoch: epoch})
	return true, nil
}

func (f *epochRecordingHeartbeatStore) MarkTaskCredentialCeiling(_ context.Context, _ string, try, epoch int) (bool, error) {
	f.marks = append(f.marks, Attempt{TryNumber: try, AttemptEpoch: epoch})
	return true, nil
}

// TestAgentLostReaperMarksAndTearsDownTheCandidateEpoch: the reaper marks
// exactly the attempt it listed (so a candidate that went stale between list
// and mark cannot fail its replacement) and deletes only that attempt's pod.
func TestAgentLostReaperMarksAndTearsDownTheCandidateEpoch(t *testing.T) {
	cs := fake.NewSimpleClientset(
		epochPod("superseded", "run-a", "extract", 1, 1, corev1.PodRunning),
		epochPod("reaped", "run-a", "extract", 1, 2, corev1.PodRunning),
	)
	store := &epochRecordingHeartbeatStore{candidates: []AgentLostCandidate{{
		TaskInstanceID: "ti-1", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2,
		LastHeartbeat: time.Now().Add(-time.Hour),
	}}}
	r := newAgentLostReaper(store, reapTestLogger(), time.Minute, nil)
	r.pods = NewKubernetesExecutor(cs, "leoflow")
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marks) != 1 || store.marks[0] != (Attempt{TryNumber: 1, AttemptEpoch: 2}) {
		t.Fatalf("the mark must name the candidate's (try, epoch), got %+v", store.marks)
	}
	got := podNames(t, cs)
	if got["reaped"] || !got["superseded"] {
		t.Fatalf("only the reaped epoch's pod may be torn down, left %v", got)
	}
}

// epochMarkStore serves one candidate of each reaper and records the attempt
// every mark names.
type epochMarkStore struct {
	running []PodLostCandidate
	queued  []StaleQueuedCandidate
	warm    []WarmBoundTI
	marks   []Attempt
	// stale makes every mark match no row, as when the listed attempt was
	// re-claimed or moved on between the list and the write.
	stale bool
}

func (f *epochMarkStore) ListRunningTasks(context.Context, time.Duration) ([]PodLostCandidate, error) {
	return f.running, nil
}

func (f *epochMarkStore) ListStaleQueuedCandidates(context.Context) ([]StaleQueuedCandidate, error) {
	return f.queued, nil
}

func (f *epochMarkStore) ListWarmBoundRunningTIs(context.Context) ([]WarmBoundTI, error) {
	return f.warm, nil
}

func (f *epochMarkStore) MarkTaskPodLost(_ context.Context, _ string, try, epoch int) (bool, error) {
	f.marks = append(f.marks, Attempt{TryNumber: try, AttemptEpoch: epoch})
	return true, nil
}

func (f *epochMarkStore) MarkTaskDispatchLost(_ context.Context, _ string, try, epoch int) (bool, error) {
	f.marks = append(f.marks, Attempt{TryNumber: try, AttemptEpoch: epoch})
	return !f.stale, nil
}

// TestPodLostReaperIsPinnedToTheCandidateEpoch: a live pod of a superseded
// epoch neither defers the reap of the current attempt nor is deleted by it,
// and the mark names the listed (try, epoch).
func TestPodLostReaperIsPinnedToTheCandidateEpoch(t *testing.T) {
	cs := fake.NewSimpleClientset(epochPod("superseded", "run-a", "extract", 1, 1, corev1.PodRunning))
	store := &epochMarkStore{running: []PodLostCandidate{{
		TaskInstanceID: "ti-1", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2,
		RunningSince: time.Now().Add(-time.Hour),
	}}}
	r := newPodLostReaper(store, reapTestLogger(), time.Minute, nil)
	r.pods = NewKubernetesExecutor(cs, "leoflow")
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marks) != 1 || store.marks[0] != (Attempt{TryNumber: 1, AttemptEpoch: 2}) {
		t.Fatalf("the mark must name the candidate's (try, epoch), got %+v", store.marks)
	}
	if !podNames(t, cs)["superseded"] {
		t.Fatalf("the superseded epoch's pod is not the reaped attempt's to delete")
	}
}

// TestDispatchLostReaperMarksTheCandidateEpoch: the dispatch-lost mark names
// the listed (try, epoch), so a row re-dispatched since the list is not failed.
func TestDispatchLostReaperMarksTheCandidateEpoch(t *testing.T) {
	store := &epochMarkStore{queued: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-1", DagRunID: "run-a", TaskID: "extract", TryNumber: 3, AttemptEpoch: 4,
		QueuedAt: time.Now().Add(-time.Hour),
	}}}
	r := newDispatchLostReaper(store, reapTestLogger(), time.Minute, nil)
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marks) != 1 || store.marks[0] != (Attempt{TryNumber: 3, AttemptEpoch: 4}) {
		t.Fatalf("the mark must name the candidate's (try, epoch), got %+v", store.marks)
	}
}

// TestDispatchLostReaperLeavesThePodWhenTheMarkIsANoop: a dispatch-lost mark
// that matched no row (the listed attempt was re-claimed, or its agent reported
// RUNNING, between the list and the write) is not a reap. The reaper must not
// tear down that attempt's pod, which may now be the row's live execution.
func TestDispatchLostReaperLeavesThePodWhenTheMarkIsANoop(t *testing.T) {
	cs := fake.NewSimpleClientset()
	store := &epochMarkStore{stale: true, queued: []StaleQueuedCandidate{{
		TaskInstanceID: "ti-1", DagRunID: "run-a", TaskID: "extract", TryNumber: 1, AttemptEpoch: 2,
		QueuedAt: time.Now().Add(-time.Hour),
	}}}
	rec := &capturingRecorder{}
	r := newDispatchLostReaper(store, reapTestLogger(), time.Minute, rec)
	r.pods = NewKubernetesExecutor(cs, "leoflow")
	// The pod materializes after the presence read: the first list (the read)
	// sees nothing, a later one (the teardown) would find it.
	lists := 0
	cs.PrependReactor("list", "pods", func(k8stesting.Action) (bool, runtime.Object, error) {
		lists++
		if lists == 1 {
			return true, &corev1.PodList{}, nil
		}
		return false, nil, nil
	})
	if _, err := cs.CoreV1().Pods("leoflow").Create(context.Background(),
		epochPod("now-running", "run-a", "extract", 1, 2, corev1.PodRunning), metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marks) != 1 {
		t.Fatalf("want one mark attempt, got %+v", store.marks)
	}
	if !podNames(t, cs)["now-running"] {
		t.Fatalf("a no-op mark must not tear down the attempt's pod")
	}
	if rec.count("dispatch_lost") != 0 || rec.count("dispatch_lost_noop") != 1 {
		t.Fatalf("a no-op mark is recorded as dispatch_lost_noop, not a reap: %v", rec.decisions)
	}
}

// TestWarmWorkerLostReaperMarksTheBoundEpoch: the warm failover mark names the
// bound attempt's (try, epoch).
func TestWarmWorkerLostReaperMarksTheBoundEpoch(t *testing.T) {
	store := &epochMarkStore{warm: []WarmBoundTI{{
		TaskInstanceID: "ti-1", DagRunID: "run-a", TaskID: "extract", TryNumber: 2, AttemptEpoch: 5, WarmWorkerID: "gone",
	}}}
	r := newWarmReaper(store, &fakeWarmLister{})
	if err := r.run(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}
	if len(store.marks) != 1 || store.marks[0] != (Attempt{TryNumber: 2, AttemptEpoch: 5}) {
		t.Fatalf("the mark must name the bound attempt's (try, epoch), got %+v", store.marks)
	}
}
