package executor

import (
	"context"
	"errors"
	"testing"
)

// fakeWarmTargets is a canned WarmTargetSource.
type fakeWarmTargets struct {
	targets []WarmTarget
	err     error
}

func (f *fakeWarmTargets) ActiveWarmTargets(context.Context) ([]WarmTarget, error) {
	return f.targets, f.err
}

// fakeWarmPods records the reconciler's create/delete calls against a canned
// existing-pod set, so a test can assert exactly what the reconciler did. It also
// records the GC-anchor calls (ADR 0058 D11): which versions had EnsureWarmAnchor /
// DeleteWarmAnchor called, and the anchor name+UID each create was stamped with.
type fakeWarmPods struct {
	existing  []WarmPodInfo
	created   []WarmTarget
	deleted   []string
	listErr   error
	createErr map[string]error // per dag_version create error (nil = ok)
	panicOn   string           // dag_version whose create panics (isolation test)
	deleteErr map[string]error // per pod-name delete error (nil = ok)

	// Anchor recording (D11).
	createdAnchorName []string          // anchorName passed to CreateWarmPod, parallel to created
	createdAnchorUID  []string          // anchorUID passed to CreateWarmPod, parallel to created
	ensured           []string          // dag_versions EnsureWarmAnchor was called for, in order
	ensureErr         map[string]error  // per dag_version EnsureWarmAnchor error (nil = ok)
	anchorUID         map[string]string // dag_version -> UID EnsureWarmAnchor returns (default: "uid-<dv>")
	deletedAnchors    []string          // dag_versions DeleteWarmAnchor was called for, in order
	anchors           []string          // dag_versions ListWarmAnchors reports an anchor for
	anchorListErr     error             // ListWarmAnchors error (nil = ok)
}

func (f *fakeWarmPods) ListWarmAnchors(context.Context) ([]string, error) {
	if f.anchorListErr != nil {
		return nil, f.anchorListErr
	}
	return f.anchors, nil
}

func (f *fakeWarmPods) ListWarmPods(context.Context) ([]WarmPodInfo, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.existing, nil
}

func (f *fakeWarmPods) CreateWarmPod(_ context.Context, t WarmTarget, anchorName, anchorUID string) error {
	if f.panicOn != "" && t.DagVersionID == f.panicOn {
		panic("boom creating " + t.DagVersionID)
	}
	if f.createErr != nil {
		if err := f.createErr[t.DagVersionID]; err != nil {
			return err
		}
	}
	f.created = append(f.created, t)
	f.createdAnchorName = append(f.createdAnchorName, anchorName)
	f.createdAnchorUID = append(f.createdAnchorUID, anchorUID)
	return nil
}

func (f *fakeWarmPods) DeleteWarmPod(_ context.Context, name string) error {
	if f.deleteErr != nil {
		if err := f.deleteErr[name]; err != nil {
			return err
		}
	}
	f.deleted = append(f.deleted, name)
	return nil
}

func (f *fakeWarmPods) EnsureWarmAnchor(_ context.Context, dagVersionID string) (string, error) {
	f.ensured = append(f.ensured, dagVersionID)
	if f.ensureErr != nil {
		if err := f.ensureErr[dagVersionID]; err != nil {
			return "", err
		}
	}
	if f.anchorUID != nil {
		if uid, ok := f.anchorUID[dagVersionID]; ok {
			return uid, nil
		}
	}
	return "uid-" + dagVersionID, nil
}

func (f *fakeWarmPods) DeleteWarmAnchor(_ context.Context, dagVersionID string) error {
	f.deletedAnchors = append(f.deletedAnchors, dagVersionID)
	return nil
}

// fakeBusyWorkers is a canned BusyWarmWorkerSource: the set of warm-worker pod
// names currently serving a running attempt. err simulates the busy-list failing
// (do-no-harm abort).
type fakeBusyWorkers struct {
	busy map[string]bool
	err  error
}

func (f *fakeBusyWorkers) ListBusyWarmWorkerPods(context.Context) (map[string]bool, error) {
	return f.busy, f.err
}

// busySet builds a busy source from a list of busy pod names.
func busySet(names ...string) *fakeBusyWorkers {
	b := map[string]bool{}
	for _, n := range names {
		b[n] = true
	}
	return &fakeBusyWorkers{busy: b}
}

func warmPods(dagVersionID string, names ...string) []WarmPodInfo {
	out := make([]WarmPodInfo, 0, len(names))
	for _, n := range names {
		out = append(out, WarmPodInfo{Name: n, DagVersionID: dagVersionID})
	}
	return out
}

// warmTerminalPods builds warm pods that have reached a terminal phase (a
// crashed/drained/finished RestartPolicy:Never worker). The reconciler must not
// count these toward the target and must always reap them.
func warmTerminalPods(dagVersionID string, names ...string) []WarmPodInfo {
	out := make([]WarmPodInfo, 0, len(names))
	for _, n := range names {
		out = append(out, WarmPodInfo{Name: n, DagVersionID: dagVersionID, Terminal: true})
	}
	return out
}

// reconcileWarm runs a tick with NO busy workers (every live pod is idle) — the
// classic case the pre-N1d-b tests exercise.
func reconcileWarm(t *testing.T, targets *fakeWarmTargets, pods *fakeWarmPods) {
	t.Helper()
	reconcileWarmBusy(t, targets, pods, &fakeBusyWorkers{})
}

// reconcileWarmBusy runs a tick against a specific busy set.
func reconcileWarmBusy(t *testing.T, targets *fakeWarmTargets, pods *fakeWarmPods, busy *fakeBusyWorkers) {
	t.Helper()
	// cap 0 = no per-tenant cap, so these pre-M4 cases exercise the unchanged
	// per-version reconcile behavior.
	r := NewWarmPoolReconciler(targets, pods, busy, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
}

// TestWarmPoolReconcileCreatesMissing: target 2, zero existing => create 2.
func TestWarmPoolReconcileCreatesMissing(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 2 {
		t.Errorf("created %d pods, want 2", len(pods.created))
	}
	for _, c := range pods.created {
		if c.DagVersionID != "dv1" || c.Image != "img" {
			t.Errorf("created %+v, want dv1/img", c)
		}
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none", pods.deleted)
	}
}

// TestWarmPoolReconcileAtTargetNoop: target 2, 2 existing => create 0, delete 0.
func TestWarmPoolReconcileAtTargetNoop(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want no changes", pods.created, pods.deleted)
	}
}

// TestWarmPoolReconcileDeletesExcess: target 2, 3 existing => delete 1.
func TestWarmPoolReconcileDeletesExcess(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2", "p3")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
	if len(pods.deleted) != 1 {
		t.Fatalf("deleted %v, want exactly 1", pods.deleted)
	}
}

// TestWarmPoolReconcileDeletesInactiveVersion: a version no longer active (not in
// targets) with 1 existing pod => the pod is deleted (target 0).
func TestWarmPoolReconcileDeletesInactiveVersion(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1}}}
	pods := &fakeWarmPods{existing: append(warmPods("dv1", "p1"), warmPods("gone", "orphan")...)}
	reconcileWarm(t, targets, pods)
	if len(pods.deleted) != 1 || pods.deleted[0] != "orphan" {
		t.Errorf("deleted %v, want [orphan] (the inactive version's pod)", pods.deleted)
	}
	// dv1 was already at its target of 1, so no create.
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
}

// TestWarmPoolReconcileTargetZeroCreatesNothing: warm pools off / effective target
// 0 (the reconciler receives no targets, or a target of 0) creates nothing and,
// for a 0-target with existing pods, drains them.
func TestWarmPoolReconcileTargetZeroCreatesNothing(t *testing.T) {
	// No active targets at all: nothing to create, nothing existing to delete.
	reconcileWarm(t, &fakeWarmTargets{}, &fakeWarmPods{})

	// Explicit zero target with existing pods => drained to zero, never created.
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 0}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none for a zero target", pods.created)
	}
	if len(pods.deleted) != 2 {
		t.Errorf("deleted %v, want both pods drained for a zero target", pods.deleted)
	}
}

// TestWarmPoolReconcileReplacesTerminalPods is the H1 regression: a fleet of 2
// TERMINAL pods (crashed/drained workers) for a target of 2 must NOT read as
// satisfied. The reconciler must CREATE 2 replacements (terminal pods do not
// count toward the target) AND reap the 2 terminal pods (they can never serve
// again). Before the fix this created 0 and deleted 0 — the pool silently died.
func TestWarmPoolReconcileReplacesTerminalPods(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: warmTerminalPods("dv1", "dead1", "dead2")}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 2 {
		t.Errorf("created %d pods, want 2 (terminal pods must not count toward the target)", len(pods.created))
	}
	if len(pods.deleted) != 2 {
		t.Errorf("deleted %v, want the 2 terminal pods reaped", pods.deleted)
	}
	gotDeleted := map[string]bool{}
	for _, n := range pods.deleted {
		gotDeleted[n] = true
	}
	if !gotDeleted["dead1"] || !gotDeleted["dead2"] {
		t.Errorf("deleted %v, want both dead1 and dead2 reaped", pods.deleted)
	}
}

// TestWarmPoolReconcileTerminalPlusLive: target 2 with 1 live + 1 terminal =>
// create 1 (only the live pod counts), reap the terminal one, keep the live one.
func TestWarmPoolReconcileTerminalPlusLive(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2}}}
	pods := &fakeWarmPods{existing: append(warmPods("dv1", "live1"), warmTerminalPods("dv1", "dead1")...)}
	reconcileWarm(t, targets, pods)
	if len(pods.created) != 1 {
		t.Errorf("created %d pods, want 1 (1 live + 1 terminal, only live counts)", len(pods.created))
	}
	if len(pods.deleted) != 1 || pods.deleted[0] != "dead1" {
		t.Errorf("deleted %v, want [dead1] (terminal reaped, live kept)", pods.deleted)
	}
}

// TestWarmPoolReconcileDrainsTerminalOnInactiveVersion: an inactive version
// (target 0) with a live + a terminal pod must drain BOTH.
func TestWarmPoolReconcileDrainsTerminalOnInactiveVersion(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1}}}
	pods := &fakeWarmPods{existing: append(
		warmPods("dv1", "keep"),
		append(warmPods("gone", "orphan-live"), warmTerminalPods("gone", "orphan-dead")...)...,
	)}
	reconcileWarm(t, targets, pods)
	gotDeleted := map[string]bool{}
	for _, n := range pods.deleted {
		gotDeleted[n] = true
	}
	if !gotDeleted["orphan-live"] || !gotDeleted["orphan-dead"] {
		t.Errorf("deleted %v, want both orphan-live and orphan-dead drained", pods.deleted)
	}
	if gotDeleted["keep"] {
		t.Errorf("deleted %v, must not drain the active version's live pod", pods.deleted)
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none (dv1 already at target)", pods.created)
	}
}

// TestWarmPoolReconcilePerVersionIsolation: a version whose create panics must not
// block a healthy version's reconcile (panic-safe, per-version isolated).
func TestWarmPoolReconcilePerVersionIsolation(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "bad", EffectiveMinIdle: 1},
		{DagVersionID: "good", EffectiveMinIdle: 2},
	}}
	pods := &fakeWarmPods{panicOn: "bad"}
	// Must not panic out of Reconcile.
	reconcileWarm(t, targets, pods)
	// The healthy version was still fully reconciled.
	var good int
	for _, c := range pods.created {
		if c.DagVersionID == "good" {
			good++
		}
	}
	if good != 2 {
		t.Errorf("healthy version got %d creates, want 2 (bad version must not block it)", good)
	}
}

// TestWarmPoolReconcileCreateErrorIsolatedPerPod: a create error is logged and the
// version keeps going; it does not abort the whole reconcile.
func TestWarmPoolReconcileCreateErrorIsolatedPerPod(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "err", EffectiveMinIdle: 2},
		{DagVersionID: "ok", EffectiveMinIdle: 1},
	}}
	pods := &fakeWarmPods{createErr: map[string]error{"err": errors.New("apiserver 500")}}
	reconcileWarm(t, targets, pods)
	// The healthy version still created its pod despite the other's error.
	var ok int
	for _, c := range pods.created {
		if c.DagVersionID == "ok" {
			ok++
		}
	}
	if ok != 1 {
		t.Errorf("ok version got %d creates, want 1", ok)
	}
}

// TestWarmPoolReconcileListErrorReturned: a list failure is surfaced so the ticker
// logs it and retries next tick (nothing is created off a bad view of the world).
func TestWarmPoolReconcileListErrorReturned(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1}}}
	pods := &fakeWarmPods{listErr: errors.New("watch broke")}
	r := NewWarmPoolReconciler(targets, pods, &fakeBusyWorkers{}, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile = nil on list error, want the error surfaced")
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v off a failed list, want none", pods.created)
	}
}

// TestWarmPoolReconcileTargetsErrorReturned: a targets-source failure is surfaced.
func TestWarmPoolReconcileTargetsErrorReturned(t *testing.T) {
	targets := &fakeWarmTargets{err: errors.New("db down")}
	pods := &fakeWarmPods{}
	r := NewWarmPoolReconciler(targets, pods, &fakeBusyWorkers{}, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err == nil {
		t.Fatal("Reconcile = nil on targets error, want the error surfaced")
	}
}

// ---- ADR 0058 N1d-b: busy-aware reconcile (model A2). EffectiveMinIdle is the
// number of IDLE workers to keep ready; MaxPoolSize is the total ceiling. Busy
// workers (serving a running attempt) never count against the idle target and are
// never deleted. ----

// deletedSet turns the reconciler's deleted list into a set for membership asserts.
func deletedSet(pods *fakeWarmPods) map[string]bool {
	s := map[string]bool{}
	for _, n := range pods.deleted {
		s[n] = true
	}
	return s
}

// TestWarmPoolReconcileCreatesIdleBufferUnderLoad is the M2 create-idle-buffer
// case: EffectiveMinIdle=2, MaxPoolSize=8, and all 3 live workers are BUSY (0
// idle). Model A2 keeps 2 IDLE workers ready, so busy workers must NOT satisfy the
// idle target: the reconciler CREATES 2 (restoring the idle buffer) and deletes 0.
// The pre-N1d-b logic counted total workers against the target (3 > 2) and deleted
// a busy worker — the M1/M2 bug this fix closes.
func TestWarmPoolReconcileCreatesIdleBufferUnderLoad(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "b1", "b2", "b3")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3"))
	if len(pods.created) != 2 {
		t.Errorf("created %d, want 2 (busy workers do not count toward the idle buffer)", len(pods.created))
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (all 3 live workers are busy)", pods.deleted)
	}
}

// TestWarmPoolReconcileRespectsMaxPoolSize is the M2 respect-ceiling case:
// EffectiveMinIdle=2, MaxPoolSize=8, 8 live workers all BUSY (0 idle). The idle
// buffer is short but the pool is at its total ceiling, so the reconciler creates
// 0 (never exceeding MaxPoolSize) and deletes 0.
func TestWarmPoolReconcileRespectsMaxPoolSize(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3", "b4", "b5", "b6", "b7", "b8"))
	if len(pods.created) != 0 {
		t.Errorf("created %d, want 0 (pool is at MaxPoolSize=8)", len(pods.created))
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none", pods.deleted)
	}
}

// TestWarmPoolReconcileIdleSteadyState: EffectiveMinIdle=2 with exactly 2 idle + 3
// busy workers is at steady state — the idle buffer is satisfied and no idle is in
// excess, so nothing is created or deleted.
func TestWarmPoolReconcileIdleSteadyState(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "i1", "i2", "b1", "b2", "b3")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3"))
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want no changes (2 idle + 3 busy is steady state)", pods.created, pods.deleted)
	}
}

// TestWarmPoolReconcileDeletesExcessIdleOnly: EffectiveMinIdle=2 with 5 idle + 1
// busy. There are 3 idle workers over target, so the reconciler deletes exactly 3
// IDLE workers, keeping the busy worker and 2 idle.
func TestWarmPoolReconcileDeletesExcessIdleOnly(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "i1", "i2", "i3", "i4", "i5", "b1")}
	reconcileWarmBusy(t, targets, pods, busySet("b1"))
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
	if len(pods.deleted) != 3 {
		t.Fatalf("deleted %v, want exactly 3 idle workers", pods.deleted)
	}
	if deletedSet(pods)["b1"] {
		t.Errorf("deleted %v, must NOT delete the busy worker b1", pods.deleted)
	}
}

// TestWarmPoolReconcileNeverDeletesBusy is the M1 case: a scale-down where every
// candidate is BUSY. EffectiveMinIdle=0 (drain-like) with 3 busy workers and 0
// idle — there is nothing idle to delete, so the reconciler deletes 0. A busy
// worker's in-flight attempt is never killed; it is reaped on a later tick once it
// goes idle.
func TestWarmPoolReconcileNeverDeletesBusy(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 0, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "b1", "b2", "b3")}
	reconcileWarmBusy(t, targets, pods, busySet("b1", "b2", "b3"))
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (every candidate is busy; M1: never delete a busy worker)", pods.deleted)
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v, want none", pods.created)
	}
}

// TestWarmPoolReconcileDrainLeavesBusy is the drain case: an inactive version
// (target 0, MaxPoolSize 0) holding 2 idle + 1 busy + 1 terminal. The reconciler
// drains the 2 idle and reaps the 1 terminal, but LEAVES the busy worker to finish
// its attempt — deleting it would kill live work (M1). It is deleted next tick
// once idle.
func TestWarmPoolReconcileDrainLeavesBusy(t *testing.T) {
	// dv1 is active (keeps its own pod); "gone" is inactive (not in targets).
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: append(
		warmPods("dv1", "keep"),
		append(warmPods("gone", "idle1", "idle2", "busy1"), warmTerminalPods("gone", "dead1")...)...,
	)}
	reconcileWarmBusy(t, targets, pods, busySet("busy1"))
	del := deletedSet(pods)
	if !del["idle1"] || !del["idle2"] {
		t.Errorf("deleted %v, want both idle workers drained", pods.deleted)
	}
	if !del["dead1"] {
		t.Errorf("deleted %v, want the terminal pod reaped", pods.deleted)
	}
	if del["busy1"] {
		t.Errorf("deleted %v, must LEAVE the busy worker to finish its attempt (M1)", pods.deleted)
	}
	if del["keep"] {
		t.Errorf("deleted %v, must not touch the active version's pod", pods.deleted)
	}
	if len(pods.deleted) != 3 {
		t.Errorf("deleted %v, want exactly 3 (2 idle + 1 terminal)", pods.deleted)
	}
}

// TestWarmPoolReconcileBusyListErrorAborts is the do-no-harm case: the busy-worker
// source erroring means the reconciler cannot tell busy from idle, so the tick
// takes NO action — zero creates, zero deletes — rather than risk deleting a busy
// worker. Reconcile returns nil (logged + metered, not surfaced as a ticker error)
// so it simply retries next tick on a good view.
func TestWarmPoolReconcileBusyListErrorAborts(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2", "p3")}
	busy := &fakeBusyWorkers{err: errors.New("db down")}
	r := NewWarmPoolReconciler(targets, pods, busy, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile = %v, want nil (busy-list error is do-no-harm, not surfaced)", err)
	}
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want NO action when the busy set is unavailable", pods.created, pods.deleted)
	}
}

// TestWarmPoolReconcileNoBusySourceAborts: a nil busy source (wiring regression)
// is do-no-harm too — no classification means no action.
func TestWarmPoolReconcileNoBusySourceAborts(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "p1", "p2", "p3")}
	r := NewWarmPoolReconciler(targets, pods, nil, 0, nil, nil)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile = %v, want nil (nil busy source is do-no-harm)", err)
	}
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Errorf("created %v deleted %v, want NO action without a busy source", pods.created, pods.deleted)
	}
}

// ---- M4: per-tenant aggregate warm-pod cap (execution.max_warm_pods_per_tenant).
// A tenant's TOTAL warm pods across all its dag_versions are bounded on a shared
// cluster so one tenant cannot pin unlimited idle pods and starve neighbors. The
// cap is RESERVE-then-RATION and reliability-safe: it never starves a promised
// idle floor, and it is enforced by refusing to CREATE, never by deleting a busy
// worker. ----

// warmPodsT builds live warm pods for a dag_version tagged with a tenant, as the
// tenant label lands on WarmPodInfo.TenantID.
func warmPodsT(dagVersionID, tenant string, names ...string) []WarmPodInfo {
	out := make([]WarmPodInfo, 0, len(names))
	for _, n := range names {
		out = append(out, WarmPodInfo{Name: n, DagVersionID: dagVersionID, TenantID: tenant})
	}
	return out
}

// reconcileWarmCap runs a tick with a per-tenant cap and a capturing recorder so a
// test can assert both the create/delete decisions and the metered decisions.
func reconcileWarmCap(t *testing.T, targets *fakeWarmTargets, pods *fakeWarmPods, busy *fakeBusyWorkers, perTenantCap int) *capturingRecorder {
	t.Helper()
	rec := &capturingRecorder{}
	r := NewWarmPoolReconciler(targets, pods, busy, perTenantCap, nil, rec)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return rec
}

// createdByVersion counts the reconciler's creates per dag_version.
func createdByVersion(pods *fakeWarmPods) map[string]int {
	m := map[string]int{}
	for _, c := range pods.created {
		m[c.DagVersionID]++
	}
	return m
}

// TestWarmPoolTenantCapFloorHonoredOverCap is the anti-starvation invariant: a
// tenant whose promised idle floors SUM to more than the cap (3 versions ×
// EffectiveMinIdle=5 = 15 vs cap 8) must still get EVERY floor — min_idle is
// sacred, the cap never denies the operator's promised buffer. The reconciler
// raises the effective budget to the floor sum, creates all 15, and meters
// warm_pool_tenant_cap_below_min_idle_sum so the misconfiguration is visible.
func TestWarmPoolTenantCapFloorHonoredOverCap(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv1", TenantID: "T", EffectiveMinIdle: 5, MaxPoolSize: 8},
		{DagVersionID: "dv2", TenantID: "T", EffectiveMinIdle: 5, MaxPoolSize: 8},
		{DagVersionID: "dv3", TenantID: "T", EffectiveMinIdle: 5, MaxPoolSize: 8},
	}}
	pods := &fakeWarmPods{} // zero live
	rec := reconcileWarmCap(t, targets, pods, &fakeBusyWorkers{}, 8)

	if len(pods.created) != 15 {
		t.Errorf("created %d, want 15 (every version's floor of 5 honored despite cap 8)", len(pods.created))
	}
	byV := createdByVersion(pods)
	for _, dv := range []string{"dv1", "dv2", "dv3"} {
		if byV[dv] != 5 {
			t.Errorf("version %s created %d, want 5 (its promised idle floor, never capped away)", dv, byV[dv])
		}
	}
	if n := rec.count("warm_pool_tenant_cap_below_min_idle_sum"); n < 1 {
		t.Errorf("warm_pool_tenant_cap_below_min_idle_sum metered %d times, want >= 1 (loud misconfiguration signal)", n)
	}
}

// TestWarmPoolTenantCapHeadroomRationedFairly: two versions each with floor 2
// (floors reserved), tenant cap 8, current live 4 (all busy, split 2/2). Both
// versions are short of their idle floor; the 4 headroom (8−4) is rationed so BOTH
// reach their floor — neither version is starved by the other.
func TestWarmPoolTenantCapHeadroomRationedFairly(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv1", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8},
		{DagVersionID: "dv2", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8},
	}}
	pods := &fakeWarmPods{existing: append(
		warmPodsT("dv1", "T", "b1", "b2"),
		warmPodsT("dv2", "T", "b3", "b4")...,
	)}
	reconcileWarmCap(t, targets, pods, busySet("b1", "b2", "b3", "b4"), 8)

	byV := createdByVersion(pods)
	if byV["dv1"] != 2 || byV["dv2"] != 2 {
		t.Errorf("created dv1=%d dv2=%d, want 2 each (headroom rationed so both reach their idle floor)", byV["dv1"], byV["dv2"])
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (all live workers are busy)", pods.deleted)
	}
}

// TestWarmPoolTenantCapAtCapNoCreateBusyUntouched: a tenant AT its cap because busy
// pods fill the budget. A second version is short of its idle floor, but the tenant
// has no headroom, so it creates 0 — and no busy worker is deleted to make room.
// The cap binds at the TENANT level, not the per-version MaxPoolSize (dv2's own
// pool has ample room).
func TestWarmPoolTenantCapAtCapNoCreateBusyUntouched(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv1", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8},
		{DagVersionID: "dv2", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8},
	}}
	// dv1 holds 4 busy pods (fills the cap of 4); dv2 has nothing and is short.
	pods := &fakeWarmPods{existing: warmPodsT("dv1", "T", "b1", "b2", "b3", "b4")}
	reconcileWarmCap(t, targets, pods, busySet("b1", "b2", "b3", "b4"), 4)

	if len(pods.created) != 0 {
		t.Errorf("created %v, want none (tenant is at its aggregate cap)", pods.created)
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (never delete a busy worker to satisfy the cap)", pods.deleted)
	}
}

// TestWarmPoolTenantCapNeverDeletesBusyOnLoweredCap: the cap is lowered at runtime
// below the tenant's current busy count. The reconciler must never delete a busy
// worker to claw back to the cap — it deletes only IDLE excess (via the unchanged
// per-version path) and leaves every busy worker running.
func TestWarmPoolTenantCapNeverDeletesBusyOnLoweredCap(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv1", TenantID: "T", EffectiveMinIdle: 1, MaxPoolSize: 8},
	}}
	// 5 busy + 2 idle; cap 2 is far below the 5 busy. The 2 idle are over the floor
	// of 1, so the excess-idle path trims exactly 1 idle; the 5 busy stay.
	pods := &fakeWarmPods{existing: append(
		warmPodsT("dv1", "T", "b1", "b2", "b3", "b4", "b5"),
		warmPodsT("dv1", "T", "i1", "i2")...,
	)}
	reconcileWarmCap(t, targets, pods, busySet("b1", "b2", "b3", "b4", "b5"), 2)

	if len(pods.created) != 0 {
		t.Errorf("created %v, want none (tenant is well over the lowered cap)", pods.created)
	}
	del := deletedSet(pods)
	for _, b := range []string{"b1", "b2", "b3", "b4", "b5"} {
		if del[b] {
			t.Errorf("deleted busy worker %s, must NEVER delete a busy worker to satisfy a lowered cap", b)
		}
	}
	if len(pods.deleted) != 1 {
		t.Errorf("deleted %v, want exactly 1 (only the idle excess over the floor)", pods.deleted)
	}
}

// TestWarmPoolTenantCapMultiTenantIsolation: tenant A being at its cap must not
// affect tenant B's creates. B has full headroom and reaches its floor normally.
func TestWarmPoolTenantCapMultiTenantIsolation(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dva", TenantID: "A", EffectiveMinIdle: 2, MaxPoolSize: 8},
		{DagVersionID: "dvb", TenantID: "B", EffectiveMinIdle: 2, MaxPoolSize: 8},
	}}
	// A is saturated with 4 busy (cap 4); B has nothing.
	pods := &fakeWarmPods{existing: warmPodsT("dva", "A", "a1", "a2", "a3", "a4")}
	reconcileWarmCap(t, targets, pods, busySet("a1", "a2", "a3", "a4"), 4)

	byV := createdByVersion(pods)
	if byV["dva"] != 0 {
		t.Errorf("tenant A created %d, want 0 (A is at its cap)", byV["dva"])
	}
	if byV["dvb"] != 2 {
		t.Errorf("tenant B created %d, want 2 (B has its own budget; A being at cap must not affect it)", byV["dvb"])
	}
}

// TestWarmPoolTenantCapUnattributablePodNotDeleted: a pre-label pod (no tenant
// label, "" TenantID) is never deleted to honor the cap. Here the tenant is over
// budget (busy pods), yet the unlabeled idle pod — within its version's floor —
// survives, and warm_pool_untenanted_pod is metered.
func TestWarmPoolTenantCapUnattributablePodNotDeleted(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv1", TenantID: "T", EffectiveMinIdle: 1, MaxPoolSize: 8},
	}}
	pods := &fakeWarmPods{existing: append(
		warmPodsT("dv1", "T", "b1", "b2", "b3"),      // busy, labeled
		WarmPodInfo{Name: "u1", DagVersionID: "dv1"}, // idle, NO tenant label
	)}
	rec := reconcileWarmCap(t, targets, pods, busySet("b1", "b2", "b3"), 2)

	if deletedSet(pods)["u1"] {
		t.Errorf("deleted %v, must NOT delete an unattributable (pre-label) pod for the cap", pods.deleted)
	}
	if len(pods.deleted) != 0 {
		t.Errorf("deleted %v, want none (u1 is within the floor; the cap deletes nothing)", pods.deleted)
	}
	if n := rec.count("warm_pool_untenanted_pod"); n < 1 {
		t.Errorf("warm_pool_untenanted_pod metered %d times, want >= 1", n)
	}
}

// ---- ADR 0058 D11: per-dag-version GC-anchor ConfigMap. The reconciler ensures
// the anchor before creating any pod (so every warm pod is cascade-GC-owned by it)
// and — the load-bearing reliability guard — deletes the anchor ONLY for an
// inactive version that has fully drained to ZERO pods, so the ownerReference
// cascade can never kill a live (busy OR idle) attempt. ----

// anchorDeleted reports whether DeleteWarmAnchor was called for a dag_version.
func anchorDeleted(pods *fakeWarmPods, dagVersionID string) bool {
	for _, dv := range pods.deletedAnchors {
		if dv == dagVersionID {
			return true
		}
	}
	return false
}

// TestWarmPoolAnchorNotDeletedWhileInactiveVersionHasPod is the FOOTGUN guard: an
// INACTIVE version ("gone", not in targets) that still holds a BUSY warm pod must
// keep its anchor — deleting it would cascade-delete the pod and KILL the live
// attempt. The busy pod is left to finish (M1) and the anchor is NOT deleted this
// tick. DeleteWarmAnchor must never be called while any pod still references it.
func TestWarmPoolAnchorNotDeletedWhileInactiveVersionHasPod(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: append(
		warmPods("dv1", "keep"),
		warmPods("gone", "busy1")...,
	)}
	reconcileWarmBusy(t, targets, pods, busySet("busy1"))

	if deletedSet(pods)["busy1"] {
		t.Errorf("deleted %v, must LEAVE the inactive version's busy worker to finish (M1)", pods.deleted)
	}
	if anchorDeleted(pods, "gone") {
		t.Errorf("DeleteWarmAnchor called for 'gone' while its busy pod still references the anchor — the cascade would kill a live attempt (FOOTGUN)")
	}
	if len(pods.deletedAnchors) != 0 {
		t.Errorf("deletedAnchors = %v, want none while any pod still exists", pods.deletedAnchors)
	}
}

// TestWarmPoolAnchorDeletedForInactiveVersionThatNeverHadAPod (#1500): every
// create of a version was refused (admission), so its anchor exists but no pod
// was ever listed for it. Once the version is inactive, the reconciler must still
// delete that anchor: zero pods reference it, so the cascade is a no-op. An
// active version with no pod yet keeps its anchor (it is still being created).
func TestWarmPoolAnchorDeletedForInactiveVersionThatNeverHadAPod(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 0, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{anchors: []string{"neverstarted", "dv1"}}

	reconcileWarmBusy(t, targets, pods, busySet())

	if !anchorDeleted(pods, "neverstarted") {
		t.Errorf("deletedAnchors = %v, want 'neverstarted' deleted (inactive, no pod ever listed)", pods.deletedAnchors)
	}
	if anchorDeleted(pods, "dv1") {
		t.Errorf("deletedAnchors = %v, must NEVER delete an active version's anchor", pods.deletedAnchors)
	}
}

// TestWarmPoolAnchorOfInactiveVersionWithAPodIsNotDeletedTwice (#1500): an
// inactive version that still has a pod is handled by the drain (its anchor is
// deleted once the drain leaves zero pods, or kept while one remains); the
// orphan sweep must not delete it on its own.
func TestWarmPoolAnchorOfInactiveVersionWithAPodIsNotDeletedTwice(t *testing.T) {
	targets := &fakeWarmTargets{}
	pods := &fakeWarmPods{
		existing: warmPods("busyver", "b1"),
		anchors:  []string{"busyver"},
	}

	reconcileWarmBusy(t, targets, pods, busySet("b1"))

	if anchorDeleted(pods, "busyver") {
		t.Errorf("deletedAnchors = %v, must NOT delete busyver's anchor (its busy pod still references it)", pods.deletedAnchors)
	}
}

// TestWarmPoolAnchorListErrorDeletesNothing (#1500): when the anchors cannot be
// listed, the orphan sweep takes no action this tick (do-no-harm) and the drain
// of versions that have pods is unaffected.
func TestWarmPoolAnchorListErrorDeletesNothing(t *testing.T) {
	targets := &fakeWarmTargets{}
	pods := &fakeWarmPods{
		existing:      warmPods("gone", "idle1"),
		anchors:       []string{"neverstarted"},
		anchorListErr: errors.New("apiserver down"),
	}

	reconcileWarmBusy(t, targets, pods, busySet())

	if anchorDeleted(pods, "neverstarted") {
		t.Errorf("deletedAnchors = %v, must not delete an orphan anchor when the anchor list failed", pods.deletedAnchors)
	}
	if !anchorDeleted(pods, "gone") {
		t.Errorf("deletedAnchors = %v, want the drained 'gone' anchor still deleted", pods.deletedAnchors)
	}
}

// TestWarmPoolAnchorDeletedWhenInactiveVersionFullyDrained: an INACTIVE version
// whose pods are ALL idle is drained to zero this tick — so after the drain zero
// pods reference the anchor and it IS deleted (bookkeeping so the anchor does not
// leak). At zero pods the cascade is a no-op. The active version's anchor is never
// touched.
func TestWarmPoolAnchorDeletedWhenInactiveVersionFullyDrained(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: append(
		warmPods("dv1", "keep"),
		warmPods("gone", "idle1", "idle2")...,
	)}
	reconcileWarmBusy(t, targets, pods, busySet()) // none busy

	del := deletedSet(pods)
	if !del["idle1"] || !del["idle2"] {
		t.Errorf("deleted %v, want both idle workers of the inactive version drained", pods.deleted)
	}
	if !anchorDeleted(pods, "gone") {
		t.Errorf("deletedAnchors = %v, want 'gone' deleted (its version fully drained to zero pods)", pods.deletedAnchors)
	}
	if anchorDeleted(pods, "dv1") {
		t.Errorf("deletedAnchors = %v, must NEVER delete an active version's anchor", pods.deletedAnchors)
	}
}

// TestWarmPoolAnchorNotDeletedOnActiveScaleDown: an ACTIVE version scaling down
// (idle excess trimmed) must keep its anchor — anchor deletion is never used to
// scale down an active version, only as drain bookkeeping for an inactive one.
func TestWarmPoolAnchorNotDeletedOnActiveScaleDown(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", EffectiveMinIdle: 1, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{existing: warmPods("dv1", "i1", "i2", "i3")}
	reconcileWarmBusy(t, targets, pods, busySet())

	if len(pods.deleted) != 2 {
		t.Errorf("deleted %v, want exactly 2 excess idle trimmed", pods.deleted)
	}
	if len(pods.deletedAnchors) != 0 {
		t.Errorf("deletedAnchors = %v, want none (an active version's anchor is never deleted on scale-down)", pods.deletedAnchors)
	}
}

// TestWarmPoolAnchorNeverDeletedWhilePodsExist sweeps the invariant across a mixed
// fleet: two inactive versions, one with a busy pod (must keep its anchor) and one
// with a failed idle delete (pod still there — must keep its anchor). Neither
// anchor may be deleted while a pod remains.
func TestWarmPoolAnchorNeverDeletedWhilePodsExist(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "active", EffectiveMinIdle: 1, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{
		existing: append(append(
			warmPods("active", "a1"),
			warmPods("busyver", "b1")...),
			warmPods("stuckver", "s1")...,
		),
		// s1's delete fails, so stuckver still has a pod after the drain.
		deleteErr: map[string]error{"s1": errors.New("apiserver 500")},
	}
	reconcileWarmBusy(t, targets, pods, busySet("b1"))

	if anchorDeleted(pods, "busyver") {
		t.Errorf("deletedAnchors = %v, must NOT delete busyver's anchor (busy pod still references it)", pods.deletedAnchors)
	}
	if anchorDeleted(pods, "stuckver") {
		t.Errorf("deletedAnchors = %v, must NOT delete stuckver's anchor (its pod's delete failed, so it still exists)", pods.deletedAnchors)
	}
}

// TestWarmPoolEnsuresAnchorBeforeCreate: creating warm workers for a version first
// ensures its GC anchor and stamps every created pod with the anchor's owner UID
// (so the cascade owner is set on birth).
func TestWarmPoolEnsuresAnchorBeforeCreate(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv1", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8}}}
	pods := &fakeWarmPods{anchorUID: map[string]string{"dv1": "uid-dv1-abc"}}
	reconcileWarmBusy(t, targets, pods, busySet())

	var ensuredDV1 bool
	for _, dv := range pods.ensured {
		if dv == "dv1" {
			ensuredDV1 = true
		}
	}
	if !ensuredDV1 {
		t.Fatalf("EnsureWarmAnchor was not called for dv1 before creating its pods (ensured=%v)", pods.ensured)
	}
	if len(pods.created) != 2 {
		t.Fatalf("created %d pods, want 2", len(pods.created))
	}
	for i := range pods.created {
		if pods.createdAnchorUID[i] != "uid-dv1-abc" {
			t.Errorf("created pod %d anchorUID = %q, want uid-dv1-abc (the ensured anchor's UID)", i, pods.createdAnchorUID[i])
		}
		if pods.createdAnchorName[i] != "leoflow-pool-dv1" {
			t.Errorf("created pod %d anchorName = %q, want leoflow-pool-dv1", i, pods.createdAnchorName[i])
		}
	}
}

// TestWarmPoolEnsureAnchorErrorSkipsCreatesForVersionOnly: if EnsureWarmAnchor fails
// for one version, NO pod is created for it this tick (do-no-harm: better a missed
// create than a warm pod with no GC owner), it is metered, and OTHER versions still
// reconcile normally.
func TestWarmPoolEnsureAnchorErrorSkipsCreatesForVersionOnly(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "bad", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8},
		{DagVersionID: "good", Image: "img", EffectiveMinIdle: 2, MaxPoolSize: 8},
	}}
	pods := &fakeWarmPods{ensureErr: map[string]error{"bad": errors.New("apiserver 500")}}
	rec := reconcileWarmCap(t, targets, pods, &fakeBusyWorkers{}, 0)

	byV := createdByVersion(pods)
	if byV["bad"] != 0 {
		t.Errorf("bad version created %d pods, want 0 (anchor ensure failed — never create an unowned warm pod)", byV["bad"])
	}
	if byV["good"] != 2 {
		t.Errorf("good version created %d pods, want 2 (a sibling's anchor error must not block it)", byV["good"])
	}
	if n := rec.count("warm_pool_anchor_ensure_error"); n < 1 {
		t.Errorf("warm_pool_anchor_ensure_error metered %d times, want >= 1", n)
	}
}
