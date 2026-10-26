package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// activeWarmVersion is one active dag_version's warm-relevant spec fields: the
// pure input to warmTargets, extracted so the projection is unit-testable without
// a database.
type activeWarmVersion struct {
	dagVersionID string
	// dagID is the DAG's id from the spec; the reconciler orders a tenant's
	// prewarm targets by it under the per-tenant cap (ADR 0082).
	dagID      string
	image      string
	dagMinIdle int
	// tenantID owns this dag_version; threaded onto the WarmTarget so the reconciler
	// can enforce the per-tenant aggregate warm-pod cap (M4). Many active runs of a
	// version share one tenant, so it dedupes with the version.
	tenantID string
	// staging marks a version whose DAG opted into the per-run shared /staging
	// volume (ADR 0022). Such versions are excluded from warm pools (ADR 0058 D5):
	// a warm worker carries no per-run /staging mount, so its attempts must run on
	// dedicated pods. Kept on the pure input so the exclusion is unit-testable.
	staging bool
	// hasSource marks a version that carries its dag.py. On the runtime image
	// with source mode on it runs in source mode and is excluded from warm pools
	// (ADR 0067 §3): a warm pod cannot carry the task's source.
	hasSource bool
	// prewarm marks a version held only as its DAG's current version, with no
	// active run (ADR 0082). It gets a target only when the DAG declares
	// min_idle_workers and the resolved idle target is above 0.
	prewarm bool
	// noWarmTask marks a version none of whose tasks dispatch would place on
	// a warm worker (domain.WarmPolicy.HasWarmTask): with executor.unit set,
	// every task declares resources or is larger than size 1, say. Its idle
	// warm workers would serve none of its tasks, so it gets no target
	// (#2065).
	noWarmTask bool
}

// warmTargets dedupes the active versions (many runs share one immutable
// dag_version) and resolves each to its effective warm target through the
// operator's clamp/fallback/flag gate (config.ExecutionSection.EffectiveMinIdle).
// A version whose image the operator's execution allowlist refuses gets no
// target (issue #537 C2), nor does a version with no warm-eligible task
// (#2065), and a prewarm version (current, no active run) gets
// one only when its DAG declares min_idle_workers (ADR 0082 D3). The result is the
// executor.WarmTargetSource contract: one entry per distinct dag_version kept.
func warmTargets(versions []activeWarmVersion, exec config.ExecutionSection, policy *domain.ExecutionPolicy) []executor.WarmTarget {
	seen := make(map[string]bool, len(versions))
	out := make([]executor.WarmTarget, 0, len(versions))
	for _, v := range versions {
		if seen[v.dagVersionID] {
			continue
		}
		seen[v.dagVersionID] = true
		if v.staging {
			// ADR 0058 D5: staging versions never get a warm pool.
			continue
		}
		if !exec.SourceMode.WarmPoolsOn() && domain.SourceModeApplies(exec.SourceMode.RuntimeImage(), v.image, v.hasSource) {
			// ADR 0067 §3: source-mode versions run on cold pods unless
			// execution.source_mode.warm_pools delivers their dag.py to warm
			// workers (ADR 0095).
			continue
		}
		if policy.CheckImage(v.image) != nil {
			// Issue #537 C2: dispatch refuses this version's tasks, and a warm
			// pod would run the disallowed image before any task reached it.
			continue
		}
		if v.noWarmTask {
			// #2065: dispatch places none of its tasks on a warm worker.
			continue
		}
		minIdle := exec.EffectiveMinIdle(v.dagMinIdle)
		if v.prewarm && (v.dagMinIdle <= 0 || minIdle == 0) {
			// ADR 0082 D3: only a DAG that declares min_idle_workers is
			// prewarmed; the operator floor alone keeps no pool for a DAG
			// without a run, and a target of 0 would only keep an empty
			// pool's anchor alive.
			continue
		}
		out = append(out, executor.WarmTarget{
			DagVersionID:     v.dagVersionID,
			DagID:            v.dagID,
			Image:            v.image,
			EffectiveMinIdle: minIdle,
			Prewarm:          v.prewarm,
			// MaxPoolSize is the operator's total ceiling of warm workers per
			// dag_version (model A2, ADR 0058 N1d-b). The reconciler grows the pool
			// past min_idle under load up to this cap and never beyond it.
			MaxPoolSize: exec.MaxPoolSize,
			// TenantID owns this dag_version; the reconciler sums a tenant's idle
			// floors and rations its aggregate budget across versions (M4).
			TenantID: v.tenantID,
		})
	}
	return out
}

// ActiveWarmTargets returns one warm target per dag_version the warm pool keeps
// (ADR 0058 N1b2b, ADR 0082), the read side of a warm-pool reconcile tick: the
// current version of every active, unpaused DAG that declares min_idle_workers,
// prewarmed before its first run (#1707), and the version of every active run
// that is still its DAG's current version. A run whose version a register, a rollback or a promote superseded
// keeps running on it, but the version is no longer a target, so its pool
// drains: idle workers are deleted and busy ones finish their attempt first,
// and the run's later attempts take dedicated pods. A run of a DAG with no
// current version keeps its version's target, as before. It reuses the same
// cached-spec path as ActiveRuns: the spec is immutable per dag_version_id, so
// N active runs sharing a version decode it once, and the effective target is
// derived from the DAG author's min_idle_workers under the operator's
// clamp/fallback. It implements executor.WarmTargetSource without the executor
// importing storage. A version none of whose tasks dispatch would place on a
// warm worker gets no target (#2065). A run held by its paused DAG (#1705) keeps a target only
// while one of its tasks is live, since nothing else of it is dispatched until
// the unpause, and a paused DAG is not prewarmed.
func (s *SchedulerStore) ActiveWarmTargets(ctx context.Context) ([]executor.WarmTarget, error) {
	rows, err := s.q.ListActiveDagRuns(ctx)
	if err != nil {
		return nil, fmt.Errorf("listing active runs: %w", err)
	}
	runs, err := s.warmRuns(ctx, rows)
	if err != nil {
		return nil, err
	}
	refs := make([]warmRunVersion, 0, len(runs))
	dagIDs := make([]pgtype.UUID, 0, len(runs))
	seenDag := make(map[pgtype.UUID]bool, len(runs))
	for _, run := range runs {
		refs = append(refs, warmRunVersion{dagPK: run.DagID, versionID: run.DagVersionID, tenantID: run.TenantID})
		if !seenDag[run.DagID] {
			seenDag[run.DagID] = true
			dagIDs = append(dagIDs, run.DagID)
		}
	}
	currents, err := s.q.ListWarmCurrentVersions(ctx, queries.ListWarmCurrentVersionsParams{
		Prewarm: s.warmExec.WarmPoolsEnabled, DagIds: dagIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("listing current dag versions: %w", err)
	}
	cands := warmCandidates(refs, currents)
	ids := make([]pgtype.UUID, 0, len(cands))
	for _, c := range cands {
		ids = append(ids, c.versionID)
	}
	// One query for every cold version, not one per prewarmed DAG (#1707).
	if err := s.specs.prefetch(ctx, s.q, ids); err != nil {
		return nil, err
	}
	versions := make([]activeWarmVersion, 0, len(cands))
	for _, c := range cands {
		spec, err := s.candidateSpec(ctx, c)
		if err != nil {
			return nil, err
		}
		versions = append(versions, activeWarmVersion{
			dagVersionID: uuidToString(c.versionID),
			dagID:        spec.DagID,
			image:        spec.Image,
			dagMinIdle:   spec.MinIdleWorkers,
			tenantID:     uuidToString(c.tenantID),
			// ADR 0058 D5: a staging version is excluded from warm pools by
			// warmTargets. Defense-in-depth alongside the dispatch-path guard.
			staging:   spec.Staging != nil && spec.Staging.Enabled,
			hasSource: spec.Source != "",
			prewarm:   c.prewarm,
			// #2065: the per-task half of the decision dispatch places with.
			noWarmTask: noWarmTask(s.warmPlacement, s.warmPolicy, spec.Tasks),
		})
	}
	return warmTargets(versions, s.warmExec, s.warmPolicy), nil
}

// noWarmTask reports whether dispatch would place none of tasks on a warm
// worker (#2065): it refuses a task the execution allowlist refuses before
// any placement, and offers the others to a warm worker only when
// placement's per-task decision (domain.WarmPolicy.HasWarmTask) accepts them.
func noWarmTask(placement domain.WarmPolicy, allow *domain.ExecutionPolicy, tasks []domain.TaskSpec) bool {
	for i := range tasks {
		if allow.CheckTask(tasks[i]) == nil && placement.HasWarmTask(tasks[i:i+1]) {
			return false
		}
	}
	return true
}

// candidateSpec reads a warm candidate's spec through the spec cache: a DAG's
// current version is pinned there, as every reader of a current version pins
// it, and any other version is read as part of the tick's working set.
func (s *SchedulerStore) candidateSpec(ctx context.Context, c warmCandidate) (domain.DAGSpec, error) {
	if c.current != nil {
		_, spec, err := s.specs.getCurrent(ctx, s.q, *c.current, c.versionID)
		return spec, err
	}
	_, spec, err := s.specs.getForTick(ctx, s.q, c.versionID)
	return spec, err
}

// warmRunVersion is the dag_version of one active run that needs warm workers,
// with its DAG and tenant.
type warmRunVersion struct {
	dagPK, versionID, tenantID pgtype.UUID
}

// warmCandidate is one dag_version a warm-pool reconcile tick may keep a pool
// for. current names the DAG whose current version it is (nil when no DAG row
// says so), and prewarm marks a version held only as the current version of an
// unpaused DAG, with no active run that needs it.
type warmCandidate struct {
	versionID, tenantID pgtype.UUID
	current             *currentSpecKey
	prewarm             bool
}

// warmCandidates is the pure target computation of ActiveWarmTargets (ADR
// 0082): the versions of the active runs that are still their DAG's current
// version (or whose DAG has no current version), then the current versions
// marked prewarm that no run already holds. A run version its DAG no longer
// points at is left out, so the reconciler drains its pool. Each version
// appears once, runs first, in input order.
func warmCandidates(runs []warmRunVersion, currents []queries.ListWarmCurrentVersionsRow) []warmCandidate {
	currentOf := make(map[pgtype.UUID]queries.ListWarmCurrentVersionsRow, len(currents))
	for _, c := range currents {
		currentOf[c.ID] = c
	}
	seen := make(map[pgtype.UUID]bool, len(runs)+len(currents))
	out := make([]warmCandidate, 0, len(currents))
	for _, r := range runs {
		cur, hasCurrent := currentOf[r.dagPK]
		if hasCurrent && cur.CurrentVersionID != r.versionID {
			continue // superseded: the run finishes on it, its pool drains
		}
		if seen[r.versionID] {
			continue
		}
		seen[r.versionID] = true
		c := warmCandidate{versionID: r.versionID, tenantID: r.tenantID}
		if hasCurrent {
			c.current = &currentSpecKey{tenant: cur.TenantID, dagID: cur.DagID}
		}
		out = append(out, c)
	}
	for _, cur := range currents {
		if !cur.Prewarm || seen[cur.CurrentVersionID] {
			continue
		}
		seen[cur.CurrentVersionID] = true
		out = append(out, warmCandidate{
			versionID: cur.CurrentVersionID, tenantID: cur.TenantID, prewarm: true,
			current: &currentSpecKey{tenant: cur.TenantID, dagID: cur.DagID},
		})
	}
	return out
}

// warmRuns returns the active runs that need warm workers: every run except a
// queued run of a paused DAG with no live task. Such a run dispatches nothing
// until the unpause (#1705), but a clear can put a running run back in queued
// while some of its tasks still run on warm workers, and those keep the
// target, as does a running run of a paused DAG, which goes on dispatching.
// The task instances are read only for the waiting runs, so the common case
// adds no query.
func (s *SchedulerStore) warmRuns(ctx context.Context, rows []queries.ListActiveDagRunsRow) ([]queries.DagRun, error) {
	var waiting []queries.ListActiveDagRunsRow
	for _, row := range rows {
		if waitsForUnpause(row) {
			waiting = append(waiting, row)
		}
	}
	tisByRun, err := s.taskInstancesByRun(ctx, waiting)
	if err != nil {
		return nil, err
	}
	runs := make([]queries.DagRun, 0, len(rows))
	for _, row := range rows {
		if waitsForUnpause(row) && !hasLiveTask(tisByRun[row.DagRun.ID]) {
			continue
		}
		runs = append(runs, row.DagRun)
	}
	return runs, nil
}
