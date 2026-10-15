package executor

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"
)

// prewarmCapTargets are two prewarm-only versions of one tenant whose
// declared floors (3 + 3) exceed the tenant cap. Their version ids sort in
// the opposite order of their DAG ids, so a test sees which key decides.
func prewarmCapTargets() *fakeWarmTargets {
	return &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dv1", DagID: "zeta", TenantID: "T", EffectiveMinIdle: 3, MaxPoolSize: 8, Prewarm: true},
		{DagVersionID: "dv9", DagID: "alpha", TenantID: "T", EffectiveMinIdle: 3, MaxPoolSize: 8, Prewarm: true},
	}}
}

// logged returns a reconciler over targets and pods whose log and decisions
// a test reads, with tenant cap perTenantCap.
func logged(targets WarmTargetSource, pods *fakeWarmPods, busy *fakeBusyWorkers, perTenantCap int) (*WarmPoolReconciler, *bytes.Buffer, *capturingRecorder) {
	var logs bytes.Buffer
	rec := &capturingRecorder{}
	return NewWarmPoolReconciler(targets, pods, busy, perTenantCap, slog.New(slog.NewTextHandler(&logs, nil)), rec), &logs, rec
}

// tick runs one reconcile and fails the test on an error.
func tick(t *testing.T, r *WarmPoolReconciler) {
	t.Helper()
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A prewarm-only version (no active run) does not raise the tenant budget:
// the declared floors share max_warm_pods_per_tenant, by DAG id, so a tenant
// cannot pin max_pool_size idle pods per unpaused DAG. The rationed version
// is logged and metered as warm_pool_prewarm_rationed, not as run floors
// over the cap.
func TestWarmPoolPrewarmFloorsRationInsideTheTenantCap(t *testing.T) {
	pods := &fakeWarmPods{}
	r, logs, rec := logged(prewarmCapTargets(), pods, busySet(), 4)
	tick(t, r)
	byV := createdByVersion(pods)
	if len(pods.created) != 4 || byV["dv9"] != 3 || byV["dv1"] != 1 {
		t.Errorf("created %v, want 4 in all: alpha (dv9) its 3, zeta (dv1) the 1 left", byV)
	}
	if rec.count("warm_pool_prewarm_rationed") != 1 {
		t.Errorf("warm_pool_prewarm_rationed = %d, want 1", rec.count("warm_pool_prewarm_rationed"))
	}
	if n := rec.count("warm_pool_tenant_cap_below_min_idle_sum"); n != 0 {
		t.Errorf("a prewarm-only ration metered run floors over the cap %d times", n)
	}
	if !strings.Contains(logs.String(), "dag_versions=[dv1]") {
		t.Errorf("the log does not name the rationed version dv1:\n%s", logs.String())
	}
}

// The rationed set is logged and metered when it changes, not on every tick.
func TestWarmPoolPrewarmRationIsNotedOncePerChange(t *testing.T) {
	targets := prewarmCapTargets()
	r, logs, rec := logged(targets, &fakeWarmPods{}, busySet(), 4)
	for i := 0; i < 3; i++ {
		tick(t, r)
	}
	if n := rec.count("warm_pool_prewarm_rationed"); n != 1 {
		t.Errorf("three ticks with the same rationed set metered %d times, want 1", n)
	}
	targets.targets = append(targets.targets, WarmTarget{DagVersionID: "dv5", DagID: "omega", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8, Prewarm: true})
	tick(t, r)
	if n := rec.count("warm_pool_prewarm_rationed"); n != 2 {
		t.Errorf("a changed rationed set metered %d times in all, want 2", n)
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 2 {
		t.Errorf("WARN lines = %d, want 2:\n%s", n, logs.String())
	}
}

// The logged list is bounded: the first 10 versions and how many more.
func TestWarmPoolPrewarmRationLogIsBounded(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{{DagVersionID: "dv00", DagID: "a00", TenantID: "T", EffectiveMinIdle: 1, MaxPoolSize: 8, Prewarm: true}}}
	for i := 1; i <= 12; i++ {
		targets.targets = append(targets.targets, WarmTarget{DagVersionID: fmt.Sprintf("dv%02d", i), DagID: fmt.Sprintf("a%02d", i), TenantID: "T", EffectiveMinIdle: 1, MaxPoolSize: 8, Prewarm: true})
	}
	r, logs, _ := logged(targets, &fakeWarmPods{}, busySet(), 1)
	tick(t, r)
	out := logs.String()
	if !strings.Contains(out, "dag_versions=\"[dv01 dv02 dv03 dv04 dv05 dv06 dv07 dv08 dv09 dv10]\"") || !strings.Contains(out, "more=2") {
		t.Errorf("the log does not list the first 10 rationed versions and 2 more:\n%s", out)
	}
	if strings.Contains(out, "dv11") {
		t.Errorf("the log lists more than 10 versions:\n%s", out)
	}
}

// The same targets win on every tick: the next reconcile, with the pods the
// first one created, creates and deletes nothing, whatever order the source
// returns the targets in.
func TestWarmPoolPrewarmRationIsStableAcrossTicks(t *testing.T) {
	pods := &fakeWarmPods{}
	reconcileWarmCap(t, prewarmCapTargets(), pods, busySet(), 4)
	next := &fakeWarmPods{}
	for i, c := range pods.created {
		next.existing = append(next.existing, WarmPodInfo{Name: c.DagVersionID + "-" + string(rune('a'+i)), DagVersionID: c.DagVersionID, TenantID: "T"})
	}
	reversed := prewarmCapTargets()
	reversed.targets[0], reversed.targets[1] = reversed.targets[1], reversed.targets[0]
	reconcileWarmCap(t, reversed, next, busySet(), 4)
	if len(next.created) != 0 || len(next.deleted) != 0 {
		t.Errorf("second tick created %v and deleted %v, want nothing", next.created, next.deleted)
	}
}

// When a DAG that sorts first takes a rationed version's share, the
// version's idle workers above its new share are not deleted that tick: it
// creates nothing, and the excess retires with the worker idle TTL (the
// agent exits, the pod is terminal), after which it is not replaced and the
// new DAG gets the room.
func TestWarmPoolPrewarmShareDropRetiresByIdleTTL(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dvZ", DagID: "zeta", TenantID: "T", EffectiveMinIdle: 3, MaxPoolSize: 8, Prewarm: true},
	}}
	pods := &fakeWarmPods{existing: warmPodsT("dvZ", "T", "z1", "z2", "z3")}
	r, _, _ := logged(targets, pods, busySet(), 3)
	tick(t, r)
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Fatalf("steady tick created %v deleted %v", pods.created, pods.deleted)
	}

	// alpha appears and takes the whole cap: zeta's share drops to 0.
	targets.targets = append(targets.targets, WarmTarget{DagVersionID: "dvA", DagID: "alpha", TenantID: "T", EffectiveMinIdle: 3, MaxPoolSize: 8, Prewarm: true})
	tick(t, r)
	if len(pods.deleted) != 0 {
		t.Errorf("the share drop deleted %v at once, want the idle workers kept until their idle TTL", pods.deleted)
	}
	if len(pods.created) != 0 {
		t.Errorf("created %v while zeta's idle workers fill the cap, want nothing", createdByVersion(pods))
	}

	// The idle TTL retires zeta's workers: they are reaped and not replaced,
	// and alpha creates its floor.
	for i := range pods.existing {
		pods.existing[i].Terminal = true
	}
	tick(t, r)
	if byV := createdByVersion(pods); byV["dvZ"] != 0 || byV["dvA"] != 3 {
		t.Errorf("after the idle TTL created %v, want alpha's 3 and none for zeta", byV)
	}
}

// A version with an active run keeps today's behavior: its floor is honored
// over the cap by raising the budget, and the prewarm-only version gets what
// the cap leaves after it, here nothing, which the log names.
func TestWarmPoolRunFloorsStillRaiseTheBudgetOverPrewarm(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dvRun", DagID: "run", TenantID: "T", EffectiveMinIdle: 5, MaxPoolSize: 8},
		{DagVersionID: "dvPre", DagID: "aaa", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8, Prewarm: true},
	}}
	pods := &fakeWarmPods{}
	r, logs, rec := logged(targets, pods, busySet(), 4)
	tick(t, r)
	byV := createdByVersion(pods)
	if byV["dvRun"] != 5 || byV["dvPre"] != 0 {
		t.Errorf("created %v, want the run version's 5 and no prewarm worker", byV)
	}
	if !strings.Contains(logs.String(), "dag_versions=[dvPre]") {
		t.Errorf("the log does not name the rationed dvPre:\n%s", logs.String())
	}
	if rec.count("warm_pool_tenant_cap_below_min_idle_sum") < 1 {
		t.Error("the run floor over the cap was not metered")
	}
}

// Prewarm floors within the cap are all granted and nothing is metered.
func TestWarmPoolPrewarmWithinTheCapIsNotMetered(t *testing.T) {
	pods := &fakeWarmPods{}
	rec := reconcileWarmCap(t, prewarmCapTargets(), pods, busySet(), 6)
	if len(pods.created) != 6 {
		t.Errorf("created %d, want both floors of 3", len(pods.created))
	}
	if n := rec.count("warm_pool_prewarm_rationed") + rec.count("warm_pool_tenant_cap_below_min_idle_sum"); n != 0 {
		t.Errorf("metered %d rations within the cap, want 0", n)
	}
}

// A target rationed to nothing has not started prewarming: when it later
// gets a share, the prewarm start is logged and metered then.
func TestWarmPoolPrewarmStartedWhenARationedTargetGetsAShare(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dvA", DagID: "alpha", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8, Prewarm: true},
		{DagVersionID: "dvZ", DagID: "zeta", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8, Prewarm: true},
	}}
	r, logs, rec := logged(targets, &fakeWarmPods{}, busySet(), 2)
	tick(t, r)
	if n := rec.count("warm_pool_prewarm_started"); n != 1 || strings.Contains(logs.String(), "prewarming the warm pool of a DAG's current version before its first run dag_version=dvZ") {
		t.Fatalf("first tick: prewarm_started = %d, want 1 for alpha only:\n%s", n, logs.String())
	}
	targets.targets = targets.targets[1:] // alpha is gone: zeta gets the cap
	tick(t, r)
	if n := rec.count("warm_pool_prewarm_started"); n != 2 {
		t.Errorf("prewarm_started = %d after zeta got a share, want 2", n)
	}
}

// A tick aborted before it acts (the busy set cannot be read) notes no
// ration, so it neither logs nor meters one.
func TestWarmPoolAbortedTickNotesNoRation(t *testing.T) {
	r, logs, rec := logged(prewarmCapTargets(), &fakeWarmPods{}, &fakeBusyWorkers{err: errors.New("db down")}, 4)
	tick(t, r)
	if rec.count("warm_pool_prewarm_rationed")+rec.count("warm_pool_tenant_cap_below_min_idle_sum") != 0 || strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("an aborted tick noted a ration:\n%s", logs.String())
	}
}

// When a run of another version needs its floor while a prewarmed pool holds
// the whole cap, the run floor is granted at once: the prewarmed version's
// idle workers above its new share are not charged against it, so the cap is
// exceeded by those idle workers until they retire with the worker idle TTL
// (ADR 0082 D7). The prewarmed version deletes and creates nothing, and once
// its workers retire it gets back only what the cap leaves.
func TestWarmPoolRunFloorIsGrantedOverARationedPrewarmPool(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dvZ", DagID: "zeta", TenantID: "T", EffectiveMinIdle: 3, MaxPoolSize: 8, Prewarm: true},
	}}
	pods := &fakeWarmPods{existing: warmPodsT("dvZ", "T", "z1", "z2", "z3")}
	r, _, _ := logged(targets, pods, busySet(), 3)
	tick(t, r)
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		t.Fatalf("steady tick created %v deleted %v", pods.created, pods.deleted)
	}

	// A run of DAG "run" (another version, floor 2) becomes active.
	targets.targets = append(targets.targets, WarmTarget{DagVersionID: "dvR", DagID: "run", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8})
	tick(t, r)
	if byV := createdByVersion(pods); byV["dvR"] != 2 || byV["dvZ"] != 0 || len(pods.deleted) != 0 {
		t.Fatalf("created %v deleted %v, want the run floor of 2 at once and zeta's idle workers kept", byV, pods.deleted)
	}

	// zeta's workers retire with the idle TTL: zeta gets back its share of 1.
	pods.created = nil
	for i := range pods.existing {
		if pods.existing[i].DagVersionID == "dvZ" {
			pods.existing[i].Terminal = true
		}
	}
	pods.existing = append(pods.existing, warmPodsT("dvR", "T", "r1", "r2")...)
	tick(t, r)
	if byV := createdByVersion(pods); byV["dvZ"] != 1 || byV["dvR"] != 0 {
		t.Errorf("after the idle TTL created %v, want zeta's share of 1 and nothing for the run", byV)
	}
}
