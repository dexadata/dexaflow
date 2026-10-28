package executor

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"testing"
)

// warmTickVersions is the number of dag_versions the warm pool tick benchmarks
// keep warm, spread over warmTickTenants tenants.
const (
	warmTickVersions = 60
	warmTickTenants  = 3
)

// steadyRunVersions builds warmTickVersions versions with an active run, each
// holding its idle floor of 2 workers, so a tick has nothing to create or
// delete.
func steadyRunVersions() ([]WarmTarget, []WarmPodInfo) {
	var targets []WarmTarget
	var existing []WarmPodInfo
	for i := 0; i < warmTickVersions; i++ {
		dv, tn := fmt.Sprintf("dv%03d", i), fmt.Sprintf("T%d", i%warmTickTenants)
		targets = append(targets, WarmTarget{DagVersionID: dv, DagID: "d" + dv, TenantID: tn, EffectiveMinIdle: 2, MaxPoolSize: 8})
		existing = append(existing, warmPodsT(dv, tn, dv+"-a", dv+"-b")...)
	}
	return targets, existing
}

// steadyRationedPrewarm builds half the versions with an active run (floor 2)
// and half prewarm-only (floor 3, ADR 0082), every pool full. Under a cap of 35
// per tenant the run floors take 20 and the prewarm pools share 15, so 5 keep
// their floor and the others are rationed and hold idle workers above their
// share: the tick runs boundPrewarm, retiringPrewarmIdle and the run credit in
// rationTenant, and still creates and deletes nothing.
func steadyRationedPrewarm() ([]WarmTarget, []WarmPodInfo) {
	var targets []WarmTarget
	var existing []WarmPodInfo
	for i := 0; i < warmTickVersions; i++ {
		dv, tn := fmt.Sprintf("dv%03d", i), fmt.Sprintf("T%d", i%warmTickTenants)
		pre := i%2 == 1
		floor, names := 2, []string{dv + "-a", dv + "-b"}
		if pre {
			floor, names = 3, append(names, dv+"-c")
		}
		targets = append(targets, WarmTarget{DagVersionID: dv, DagID: "d" + dv, TenantID: tn, EffectiveMinIdle: floor, MaxPoolSize: 8, Prewarm: pre})
		existing = append(existing, warmPodsT(dv, tn, names...)...)
	}
	return targets, existing
}

// BenchmarkWarmPoolTick measures one steady warm pool reconcile tick (the
// leader pays it every warm pool interval) over 60 versions of 3 tenants
// under a per-tenant cap: the target, fleet and busy reads come from fakes, so
// Kubernetes and database time are excluded. It fails when the scenario is not
// steady, so the number never includes a create or a delete.
func BenchmarkWarmPoolTick(b *testing.B) {
	cases := []struct {
		name  string
		build func() ([]WarmTarget, []WarmPodInfo)
		cap   int
	}{
		{"versions=60/run", steadyRunVersions, 40},
		{"versions=60/rationed_prewarm", steadyRationedPrewarm, 35},
	}
	for _, c := range cases {
		targets, existing := c.build()
		b.Run(c.name, func(b *testing.B) {
			benchWarmTick(b, targets, existing, c.cap)
		})
	}
}

// benchWarmTick runs one warm-up tick, which logs the prewarm starts once,
// then times the steady ticks after it.
func benchWarmTick(b *testing.B, targets []WarmTarget, existing []WarmPodInfo, perTenantCap int) {
	b.Helper()
	pods := &fakeWarmPods{existing: existing}
	r := NewWarmPoolReconciler(&fakeWarmTargets{targets: targets}, pods, busySet(), perTenantCap, slog.New(slog.NewTextHandler(io.Discard, nil)), nil)
	ctx := context.Background()
	if err := r.Reconcile(ctx); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	for b.Loop() {
		if err := r.Reconcile(ctx); err != nil {
			b.Fatal(err)
		}
	}
	if len(pods.created) != 0 || len(pods.deleted) != 0 {
		b.Fatalf("tick is not steady: created %d, deleted %d", len(pods.created), len(pods.deleted))
	}
}
