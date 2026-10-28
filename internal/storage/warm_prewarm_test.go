package storage

import (
	"testing"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// testUUID is a valid pgtype.UUID whose first byte is b, for tables of ids.
func testUUID(b byte) pgtype.UUID {
	return pgtype.UUID{Bytes: [16]byte{b}, Valid: true}
}

func candidateIDs(cands []warmCandidate) map[pgtype.UUID]warmCandidate {
	out := make(map[pgtype.UUID]warmCandidate, len(cands))
	for _, c := range cands {
		out[c.versionID] = c
	}
	return out
}

// TestWarmCandidatesFollowTheCurrentVersion locks the prewarm target
// computation (#1707): the current version of an unpaused DAG is a candidate
// before its first run, a run's version that is no longer its DAG's current
// one is not (its pool drains), and a run of a DAG with no current version
// keeps its version, as before.
func TestWarmCandidatesFollowTheCurrentVersion(t *testing.T) {
	dagA, dagB, dagC, dagD := testUUID(1), testUUID(2), testUUID(3), testUUID(4)
	tenant := testUUID(9)
	v1A, v2A, vB, vC, vD := testUUID(11), testUUID(12), testUUID(21), testUUID(31), testUUID(41)
	runs := []warmRunVersion{
		{dagPK: dagA, versionID: v1A, tenantID: tenant}, // superseded by v2A
		{dagPK: dagB, versionID: vB, tenantID: tenant},  // current, paused DAG with a live run
		{dagPK: dagB, versionID: vB, tenantID: tenant},  // a second run of the same version
		{dagPK: dagC, versionID: vC, tenantID: tenant},  // no current version on the DAG row
	}
	currents := []queries.ListWarmCurrentVersionsRow{
		{ID: dagA, TenantID: tenant, DagID: "a", CurrentVersionID: v2A, Prewarm: true},
		{ID: dagB, TenantID: tenant, DagID: "b", CurrentVersionID: vB, Prewarm: false},
		{ID: dagD, TenantID: tenant, DagID: "d", CurrentVersionID: vD, Prewarm: true},
	}
	got := candidateIDs(warmCandidates(runs, currents))

	if _, ok := got[v1A]; ok {
		t.Errorf("superseded version v1A is a candidate; its pool must drain: %+v", got)
	}
	if c, ok := got[v2A]; !ok || !c.prewarm {
		t.Errorf("current version v2A of an unpaused DAG = %+v (present %v), want a prewarm candidate", c, ok)
	}
	if c, ok := got[vD]; !ok || !c.prewarm {
		t.Errorf("current version vD with no run = %+v (present %v), want a prewarm candidate", c, ok)
	}
	if c, ok := got[vB]; !ok || c.prewarm {
		t.Errorf("current version vB with a live run = %+v (present %v), want a run candidate", c, ok)
	}
	if c, ok := got[vC]; !ok || c.current != nil {
		t.Errorf("run version vC of a DAG with no current version = %+v (present %v), want kept, not pinned", c, ok)
	}
	if c := got[v2A]; c.current == nil || c.current.dagID != "a" || c.current.tenant != tenant {
		t.Errorf("current candidate v2A carries pin key %+v, want tenant %v dag a", c.current, tenant)
	}
	if len(got) != 4 {
		t.Errorf("got %d candidates, want 4 distinct versions: %+v", len(got), got)
	}
}

// TestWarmCandidatesRunOnCurrentVersionIsNotPrewarm: a current version that
// also has an active run is one candidate, held by the run (prewarm false),
// so the reconciler's prewarm log does not fire for a pool runs already use.
func TestWarmCandidatesRunOnCurrentVersionIsNotPrewarm(t *testing.T) {
	dag, tenant, v := testUUID(1), testUUID(9), testUUID(11)
	got := warmCandidates(
		[]warmRunVersion{{dagPK: dag, versionID: v, tenantID: tenant}},
		[]queries.ListWarmCurrentVersionsRow{{ID: dag, TenantID: tenant, DagID: "a", CurrentVersionID: v, Prewarm: true}},
	)
	if len(got) != 1 || got[0].prewarm {
		t.Fatalf("candidates = %+v, want one run candidate (prewarm false)", got)
	}
}

// TestWarmTargetsPrewarmNeedsADeclaredMinIdle: a prewarm-only version
// (current, no active run) becomes a target only when the DAG itself declares
// min_idle_workers (ADR 0082 D3), and is marked Prewarm. The operator floor
// alone prewarms nothing, so it never costs a pool per unpaused DAG; a version
// an active run holds keeps the floor, as before.
func TestWarmTargetsPrewarmNeedsADeclaredMinIdle(t *testing.T) {
	floor := config.ExecutionSection{WarmPoolsEnabled: true, MinIdleWorkers: 2, MaxPoolSize: 8}
	got := warmTargets([]activeWarmVersion{{dagVersionID: "dv1", image: "i", prewarm: true}}, floor, nil)
	if len(got) != 0 {
		t.Fatalf("operator floor 2, DAG declares none: targets = %+v, want no prewarm target", got)
	}

	got = warmTargets([]activeWarmVersion{{dagVersionID: "dv1", image: "i", dagMinIdle: 3, prewarm: true}}, floor, nil)
	if len(got) != 1 || !got[0].Prewarm || got[0].EffectiveMinIdle != 3 {
		t.Fatalf("declared min_idle_workers 3: targets = %+v, want one Prewarm target of 3", got)
	}

	got = warmTargets([]activeWarmVersion{{dagVersionID: "dv2", image: "i"}}, floor, nil)
	if len(got) != 1 || got[0].Prewarm || got[0].EffectiveMinIdle != 2 {
		t.Fatalf("run version, DAG declares none: targets = %+v, want the operator floor 2", got)
	}

	off := config.ExecutionSection{WarmPoolsEnabled: false, MaxPoolSize: 8}
	got = warmTargets([]activeWarmVersion{{dagVersionID: "dv1", image: "i", dagMinIdle: 3, prewarm: true}}, off, nil)
	if len(got) != 0 {
		t.Fatalf("warm pools off: targets = %+v, want no prewarm target", got)
	}
}

// A target carries its DAG's id, which the reconciler orders prewarm targets
// by when the per-tenant cap cannot cover all of them (ADR 0082).
func TestWarmTargetsCarryTheDagID(t *testing.T) {
	exec := config.ExecutionSection{WarmPoolsEnabled: true, MaxPoolSize: 8}
	got := warmTargets([]activeWarmVersion{{dagVersionID: "dv1", dagID: "orders", image: "i", dagMinIdle: 2, prewarm: true}}, exec, nil)
	if len(got) != 1 || got[0].DagID != "orders" {
		t.Fatalf("targets = %+v, want one target with DagID orders", got)
	}
}
