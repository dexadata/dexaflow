package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestWarmTargets locks the pure active-version -> warm-target projection (ADR
// 0058 N1b2b): distinct active dag_versions each resolve to their effective warm
// target via the operator's clamp/fallback/flag gate, and duplicates (many active
// runs of one version) collapse to a single target.
func TestWarmTargets(t *testing.T) {
	exec := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}
	versions := []activeWarmVersion{
		{dagVersionID: "dv1", image: "img1", dagMinIdle: 2, tenantID: "tA"},  // author 2 -> 2
		{dagVersionID: "dv1", image: "img1", dagMinIdle: 2, tenantID: "tA"},  // dup of dv1, collapses
		{dagVersionID: "dv2", image: "img2", dagMinIdle: 0, tenantID: "tB"},  // unset -> operator floor 1
		{dagVersionID: "dv3", image: "img3", dagMinIdle: 20, tenantID: "tB"}, // over cap -> clamped 8
	}
	got := warmTargets(versions, exec, nil)
	if len(got) != 3 {
		t.Fatalf("warmTargets returned %d entries, want 3 distinct versions: %+v", len(got), got)
	}
	want := map[string]int{"dv1": 2, "dv2": 1, "dv3": 8}
	for _, tgt := range got {
		if w, ok := want[tgt.DagVersionID]; !ok || tgt.EffectiveMinIdle != w {
			t.Errorf("target %s = %d, want %d", tgt.DagVersionID, tgt.EffectiveMinIdle, w)
		}
	}
	// Image is threaded through so the reconciler can build the warm pod.
	for _, tgt := range got {
		if tgt.Image == "" {
			t.Errorf("target %s carries no image", tgt.DagVersionID)
		}
	}
	// MaxPoolSize (the operator's total ceiling) is threaded onto every target so
	// the busy-aware reconciler can cap pool growth under load (ADR 0058 N1d-b).
	for _, tgt := range got {
		if tgt.MaxPoolSize != 8 {
			t.Errorf("target %s MaxPoolSize = %d, want 8 (operator's execution.max_pool_size)", tgt.DagVersionID, tgt.MaxPoolSize)
		}
	}
	// TenantID is threaded onto every target so the reconciler can enforce the
	// per-tenant aggregate warm-pod cap (M4).
	wantTenant := map[string]string{"dv1": "tA", "dv2": "tB", "dv3": "tB"}
	for _, tgt := range got {
		if w := wantTenant[tgt.DagVersionID]; tgt.TenantID != w {
			t.Errorf("target %s TenantID = %q, want %q", tgt.DagVersionID, tgt.TenantID, w)
		}
	}
}

// TestWarmTargetsWarmPoolsOff locks that with warm pools off every effective
// target is 0, so the reconciler (were it even running) would keep every pool
// empty.
func TestWarmTargetsWarmPoolsOff(t *testing.T) {
	exec := config.ExecutionSection{WarmPoolsEnabled: false, MinIdleWorkers: 5, MaxPoolSize: 8}
	got := warmTargets([]activeWarmVersion{{dagVersionID: "dv1", image: "i", dagMinIdle: 4}}, exec, nil)
	if len(got) != 1 || got[0].EffectiveMinIdle != 0 {
		t.Errorf("warmTargets with pools off = %+v, want single dv1 with target 0", got)
	}
}

// TestWarmTargetsExcludesStagingVersion pins ADR 0058 D5: a version whose DAG
// opted into the per-run /staging volume gets no warm target, so the reconciler
// never creates a warm pod (which lacks the mount) for it.
func TestWarmTargetsExcludesStagingVersion(t *testing.T) {
	exec := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}
	versions := []activeWarmVersion{
		{dagVersionID: "dv1", image: "i", dagMinIdle: 2, tenantID: "tA"},
		{dagVersionID: "dv2", image: "i", dagMinIdle: 2, tenantID: "tA", staging: true},
	}
	got := warmTargets(versions, exec, nil)
	if len(got) != 1 || got[0].DagVersionID != "dv1" {
		t.Errorf("staging version must be excluded from warm targets, got %+v", got)
	}
}

// TestWarmTargetsExcludesSourceModeVersion pins ADR 0067 §3: a source-mode
// version (runtime image plus a source, with the mode on) gets no warm target,
// since a warm pod is built before its task and carries no dag.py.
func TestWarmTargetsExcludesSourceModeVersion(t *testing.T) {
	const rt = "ghcr.io/dexadata/runtime@sha256:abc"
	exec := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8,
		SourceMode: config.SourceModeSection{Enabled: true, Image: rt}}
	versions := []activeWarmVersion{
		{dagVersionID: "src", image: rt, hasSource: true},
		{dagVersionID: "rt-no-source", image: rt},
		{dagVersionID: "own-image", image: "etl:v1", hasSource: true},
	}
	got := warmTargets(versions, exec, nil)
	ids := map[string]bool{}
	for _, tgt := range got {
		ids[tgt.DagVersionID] = true
	}
	if ids["src"] || !ids["rt-no-source"] || !ids["own-image"] || len(got) != 2 {
		t.Errorf("warm targets = %+v, want only rt-no-source and own-image", got)
	}

	exec.SourceMode.Enabled = false
	if got := warmTargets(versions, exec, nil); len(got) != 3 {
		t.Errorf("with source mode off every version keeps its target, got %+v", got)
	}

	// ADR 0095: with the mode's warm pools on, the dag.py goes with each
	// assignment, so the source-mode version keeps its target too.
	exec.SourceMode.Enabled, exec.SourceMode.WarmPools = true, true
	if got := warmTargets(versions, exec, nil); len(got) != 3 {
		t.Errorf("with source_mode.warm_pools on every version keeps its target, got %+v", got)
	}
}

// A version whose image is outside the operator's image allowlist (issue #537
// C2) gets no warm target, so the reconciler never starts a warm pod on it:
// dispatch would refuse its tasks, but a warm container would already have run
// the image under the task ServiceAccount. Allowed images keep their pools.
func TestWarmTargetsSkipImagesOutsideTheAllowlist(t *testing.T) {
	exec := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}
	policy, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{Images: []string{"registry.example.com/team-a/*"}})
	if err != nil {
		t.Fatal(err)
	}
	versions := []activeWarmVersion{
		{dagVersionID: "ok", image: "registry.example.com/team-a/etl:v1", dagMinIdle: 2},
		{dagVersionID: "rogue", image: "ghcr.io/someone/tool:latest", dagMinIdle: 2},
	}
	got := warmTargets(versions, exec, policy)
	if len(got) != 1 || got[0].DagVersionID != "ok" {
		t.Fatalf("warmTargets = %+v, want only the allowed version", got)
	}
	// Without a policy (the default) both keep their warm pools.
	if got := warmTargets(versions, exec, nil); len(got) != 2 {
		t.Fatalf("warmTargets(nil policy) = %+v, want both versions", got)
	}
}

// TestWarmTargetsSkipsVersionWithNoWarmTask (#2065): a version none of whose
// tasks may be placed on a warm worker (with executor.unit set, every task
// declares resources or is larger than size 1) gets no warm target, run or
// prewarm, so no idle warm pod sits next to the dedicated pods its tasks take.
func TestWarmTargetsSkipsVersionWithNoWarmTask(t *testing.T) {
	exec := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 1, MaxPoolSize: 8}
	versions := []activeWarmVersion{
		{dagVersionID: "warm", image: "i", dagMinIdle: 2},
		{dagVersionID: "sized", image: "i", dagMinIdle: 2, noWarmTask: true},
		{dagVersionID: "sized-prewarm", image: "i", dagMinIdle: 2, prewarm: true, noWarmTask: true},
	}
	got := warmTargets(versions, exec, nil)
	if len(got) != 1 || got[0].DagVersionID != "warm" {
		t.Fatalf("warmTargets = %+v, want only the version with a warm-eligible task", got)
	}
}

// TestNoWarmTaskHonorsTheAllowlist (#2065): dispatch refuses a task the
// execution allowlist refuses before it offers it to a warm worker, so such a
// task does not keep its version's warm pool either. Here the only task that
// fits a warm pod pins the warm workers' own ServiceAccount, which an
// allowlist set after the DAG was registered no longer allows.
func TestNoWarmTaskHonorsTheAllowlist(t *testing.T) {
	placement := domain.WarmPolicy{Enabled: true, ServiceAccount: "dexaflow-task"}
	allow, err := domain.ParseExecutionPolicy(domain.ExecutionPolicyConfig{ServiceAccounts: []string{"reporting"}})
	if err != nil {
		t.Fatal(err)
	}
	tasks := []domain.TaskSpec{
		{TaskID: "pinned", Execution: &domain.Execution{ServiceAccount: "dexaflow-task"}},
		{TaskID: "placed", Execution: &domain.Execution{Labels: map[string]string{"team": "x"}}},
	}
	if !noWarmTask(placement, allow, tasks) {
		t.Error("a version whose only warm-fitting task the allowlist refuses keeps a warm target")
	}
	if noWarmTask(placement, nil, tasks) {
		t.Error("without an allowlist the pinned task fits a warm pod, but the version lost its target")
	}
	if !noWarmTask(placement, nil, nil) {
		t.Error("a version with no task keeps a warm target")
	}
}
