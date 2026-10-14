package executor

import (
	"bytes"
	"context"
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

// A prewarm-only version (no active run) does not raise the tenant budget:
// the declared floors share max_warm_pods_per_tenant, by DAG id, so a tenant
// cannot pin max_pool_size idle pods per unpaused DAG. The starved prewarm is
// logged and metered as a floor the cap cannot cover.
func TestWarmPoolPrewarmFloorsRationInsideTheTenantCap(t *testing.T) {
	var logs bytes.Buffer
	rec := &capturingRecorder{}
	pods := &fakeWarmPods{}
	r := NewWarmPoolReconciler(prewarmCapTargets(), pods, busySet(), 4, slog.New(slog.NewTextHandler(&logs, nil)), rec)
	if err := r.Reconcile(context.Background()); err != nil {
		t.Fatal(err)
	}
	byV := createdByVersion(pods)
	if len(pods.created) != 4 || byV["dv9"] != 3 || byV["dv1"] != 1 {
		t.Errorf("created %v, want 4 in all: alpha (dv9) its 3, zeta (dv1) the 1 left", byV)
	}
	if rec.count("warm_pool_tenant_cap_below_min_idle_sum") < 1 {
		t.Error("the starved prewarm floor was not metered")
	}
	if !strings.Contains(logs.String(), "dag_versions=[dv1]") {
		t.Errorf("the log does not name the starved version dv1:\n%s", logs.String())
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

// An existing prewarm pool above its share is trimmed to it when another
// DAG that sorts first takes the cap: the bound holds on idle pods, not only
// on creates.
func TestWarmPoolPrewarmPoolAboveItsShareIsTrimmed(t *testing.T) {
	pods := &fakeWarmPods{existing: warmPodsT("dv1", "T", "z1", "z2", "z3")}
	reconcileWarmCap(t, prewarmCapTargets(), pods, busySet(), 4)
	if len(pods.deleted) != 2 {
		t.Errorf("deleted %v, want 2 of zeta's 3 idle pods (its share is 1)", pods.deleted)
	}
}

// A version with an active run keeps today's behavior: its floor is honored
// over the cap by raising the budget, and the prewarm-only version gets what
// the cap leaves after it, here nothing.
func TestWarmPoolRunFloorsStillRaiseTheBudgetOverPrewarm(t *testing.T) {
	targets := &fakeWarmTargets{targets: []WarmTarget{
		{DagVersionID: "dvRun", DagID: "run", TenantID: "T", EffectiveMinIdle: 5, MaxPoolSize: 8},
		{DagVersionID: "dvPre", DagID: "aaa", TenantID: "T", EffectiveMinIdle: 2, MaxPoolSize: 8, Prewarm: true},
	}}
	pods := &fakeWarmPods{}
	rec := reconcileWarmCap(t, targets, pods, busySet(), 4)
	byV := createdByVersion(pods)
	if byV["dvRun"] != 5 || byV["dvPre"] != 0 {
		t.Errorf("created %v, want the run version's 5 and no prewarm worker", byV)
	}
	if rec.count("warm_pool_tenant_cap_below_min_idle_sum") < 1 {
		t.Error("the floors over the cap were not metered")
	}
}

// Prewarm floors within the cap are all granted and nothing is metered.
func TestWarmPoolPrewarmWithinTheCapIsNotMetered(t *testing.T) {
	pods := &fakeWarmPods{}
	rec := reconcileWarmCap(t, prewarmCapTargets(), pods, busySet(), 6)
	if len(pods.created) != 6 {
		t.Errorf("created %d, want both floors of 3", len(pods.created))
	}
	if n := rec.count("warm_pool_tenant_cap_below_min_idle_sum"); n != 0 {
		t.Errorf("metered %d starvations within the cap, want 0", n)
	}
}
