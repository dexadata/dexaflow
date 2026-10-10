package storage

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
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
	got := warmTargets(versions, exec)
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
	got := warmTargets([]activeWarmVersion{{dagVersionID: "dv1", image: "i", dagMinIdle: 4}}, exec)
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
	got := warmTargets(versions, exec)
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
	got := warmTargets(versions, exec)
	ids := map[string]bool{}
	for _, tgt := range got {
		ids[tgt.DagVersionID] = true
	}
	if ids["src"] || !ids["rt-no-source"] || !ids["own-image"] || len(got) != 2 {
		t.Errorf("warm targets = %+v, want only rt-no-source and own-image", got)
	}

	exec.SourceMode.Enabled = false
	if got := warmTargets(versions, exec); len(got) != 3 {
		t.Errorf("with source mode off every version keeps its target, got %+v", got)
	}
}
