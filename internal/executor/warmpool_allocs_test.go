package executor

import (
	"context"
	"fmt"
	"testing"
)

// steadyWarmReconciler builds a reconciler over versions run versions of 3
// tenants under a per-tenant cap, each holding podsPerVersion idle workers
// and a floor of the same size, and runs one tick so the next ones are
// steady: nothing to create or delete.
func steadyWarmReconciler(ctx context.Context, t *testing.T, versions, podsPerVersion int) (*WarmPoolReconciler, *fakeWarmPods) {
	t.Helper()
	var targets []WarmTarget
	var existing []WarmPodInfo
	for i := 0; i < versions; i++ {
		dv, tn := fmt.Sprintf("dv%03d", i), fmt.Sprintf("T%d", i%3)
		targets = append(targets, WarmTarget{DagVersionID: dv, DagID: "d" + dv, TenantID: tn, EffectiveMinIdle: podsPerVersion, MaxPoolSize: podsPerVersion})
		for p := 0; p < podsPerVersion; p++ {
			existing = append(existing, WarmPodInfo{Name: fmt.Sprintf("%s-%d", dv, p), DagVersionID: dv, TenantID: tn})
		}
	}
	pods := &fakeWarmPods{existing: existing}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: targets}, pods, busySet(), versions*podsPerVersion, nil, nil)
	if err := r.Reconcile(ctx); err != nil {
		t.Fatal(err)
	}
	return r, pods
}

// TestWarmPoolSteadyTickAllocsDoNotGrowWithPods: the leader runs the warm
// pool tick every interval, and in steady state it only reads. Grouping the
// fleet by version and classifying each version's workers must cost the same
// number of allocations whether a version holds 2 workers or 8 (#1906): the
// work per pod is a count, not a slice append.
func TestWarmPoolSteadyTickAllocsDoNotGrowWithPods(t *testing.T) {
	ctx := context.Background()
	allocs := func(podsPerVersion int) float64 {
		r, pods := steadyWarmReconciler(ctx, t, 60, podsPerVersion)
		n := testing.AllocsPerRun(20, func() {
			if err := r.Reconcile(ctx); err != nil {
				t.Fatal(err)
			}
		})
		if len(pods.created) != 0 || len(pods.deleted) != 0 {
			t.Fatalf("tick with %d pods per version is not steady: created %d, deleted %d", podsPerVersion, len(pods.created), len(pods.deleted))
		}
		return n
	}
	small, large := allocs(2), allocs(8)
	if large != small {
		t.Errorf("steady tick allocations: %v with 2 workers per version, %v with 8; want the same", small, large)
	}
}

// TestWarmPoolSteadyPrewarmPassAllocatesNothing: the prewarm pass ADR 0082
// added to every tick (boundPrewarm and notePoolChanges) allocates nothing
// once the pools are steady and the cap covers every prewarm floor (#1906).
// It used to copy the targets and rebuild its per-tick sets on every tick.
func TestWarmPoolSteadyPrewarmPassAllocatesNothing(t *testing.T) {
	ctx := context.Background()
	var targets []WarmTarget
	byVersion := map[string][]WarmPodInfo{}
	for i := 0; i < 20; i++ {
		dv, tn := fmt.Sprintf("dv%03d", i), fmt.Sprintf("T%d", i%2)
		targets = append(targets, WarmTarget{DagVersionID: dv, DagID: "d" + dv, TenantID: tn, EffectiveMinIdle: 2, MaxPoolSize: 4, Prewarm: i%2 == 1})
		byVersion[dv] = warmPodsT(dv, tn, dv+"-a", dv+"-b")
	}
	// A version that drains: it has pods and is no longer a target.
	byVersion["old"] = warmPodsT("old", "T0", "old-a")
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: targets}, &fakeWarmPods{}, busySet(), 100, nil, nil)
	pass := func() {
		kept := r.boundPrewarm(ctx, targets)
		r.notePoolChanges(ctx, kept, byVersion)
	}
	pass()
	if allocs := testing.AllocsPerRun(20, pass); allocs != 0 {
		t.Errorf("steady prewarm pass allocates %v times per tick, want 0", allocs)
	}
	if !r.lastTargets["dv001"] || !r.draining["old"] || r.draining["dv001"] {
		t.Errorf("prewarm pass lost its state: lastTargets %v, draining %v", r.lastTargets, r.draining)
	}
}

// TestWarmPoolSteadyVersionAllocatesNothing: classifying a steady version's
// workers (one busy, the idle ones at their floor, one terminal reaped)
// counts them and lists none, so it allocates nothing (#1906). The idle
// workers are listed only when some must be trimmed.
func TestWarmPoolSteadyVersionAllocatesNothing(t *testing.T) {
	ctx := context.Background()
	have := append(warmPods("dv", "w-busy", "w-1", "w-2"), warmTerminalPods("dv", "w-dead")...)
	busy := map[string]bool{"w-busy": true}
	pods := &fakeWarmPods{deleted: make([]string, 0, 64)}
	r := NewWarmPoolReconciler(&fakeWarmTargets{}, pods, busySet(), 0, nil, nil)
	target := WarmTarget{DagVersionID: "dv", EffectiveMinIdle: 2, MaxPoolSize: 4}
	var remaining, create int
	allocs := testing.AllocsPerRun(20, func() {
		remaining, create = r.reconcileVersion(ctx, target, have, busy, unlimitedAllowance)
	})
	if allocs != 0 {
		t.Errorf("steady version allocates %v times per tick, want 0", allocs)
	}
	if remaining != 3 || create != 0 {
		t.Errorf("remaining, create = %d, %d; want 3 (busy + 2 idle), 0", remaining, create)
	}
}

// Every group of groupByVersion is capped at its own length, so an append to
// one group reallocates instead of writing over the next group in the shared
// backing array (#1906).
func TestGroupByVersionGroupsDoNotAlias(t *testing.T) {
	fleet := []WarmPodInfo{
		{Name: "a1", DagVersionID: "a"}, {Name: "b1", DagVersionID: "b"},
		{Name: "a2", DagVersionID: "a"}, {Name: "c1", DagVersionID: "c"},
		{Name: "b2", DagVersionID: "b"},
	}
	byVersion := groupByVersion(fleet, 3)
	want := map[string][]string{"a": {"a1", "a2"}, "b": {"b1", "b2"}, "c": {"c1"}}
	for v, g := range byVersion {
		if cap(g) != len(g) {
			t.Errorf("group %s has cap %d, len %d: an append would reach the next group", v, cap(g), len(g))
		}
	}
	for _, v := range []string{"a", "b"} {
		_ = append(byVersion[v], WarmPodInfo{Name: "intruder", DagVersionID: v})
	}
	for v, names := range want {
		got := byVersion[v]
		if len(got) != len(names) {
			t.Fatalf("group %s = %v, want %v", v, got, names)
		}
		for i, n := range names {
			if got[i].Name != n {
				t.Errorf("group %s[%d] = %s after appends to other groups, want %s", v, i, got[i].Name, n)
			}
		}
	}
}
