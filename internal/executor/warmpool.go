package executor

import (
	"context"
	"log/slog"
	"sort"
	"sync"
)

// WarmTarget is one active dag_version the warm-pool reconciler keeps warm workers
// ready for (model A2, ADR 0058 N1d-b). Image is the DAG's image the warm worker
// runs the agent from.
//
// EffectiveMinIdle is the number of IDLE workers to keep ready for this version —
// the warm buffer, already resolved through the clamp/fallback/flag gate (see
// config.ExecutionSection.EffectiveMinIdle). It is NOT a total pool size: busy
// workers do not count against it, so under load the pool grows past min_idle to
// keep the idle buffer available.
//
// MaxPoolSize is the TOTAL ceiling of live workers (idle + busy) the version may
// hold at once (config.ExecutionSection.MaxPoolSize). The reconciler never creates
// past it, so the pool breathes between EffectiveMinIdle-idle + busy and this cap.
// The N1a boot validation guarantees max_pool_size >= 1 when warm pools are on and
// EffectiveMinIdle is clamped to it upstream, so MaxPoolSize >= EffectiveMinIdle in
// practice; the reconciler still handles MaxPoolSize < EffectiveMinIdle defensively
// by taking the effective ceiling as max(EffectiveMinIdle, MaxPoolSize) — the idle
// buffer must always be creatable.
// TenantID is the tenant that owns this dag_version, threaded through so the
// per-tenant aggregate warm-pod cap (M4) can sum a tenant's promised idle floors
// and ration its shared budget across versions. It comes from the active run's
// tenant_id; the reconciler groups targets and live pods by it.
type WarmTarget struct {
	DagVersionID     string
	Image            string
	EffectiveMinIdle int
	MaxPoolSize      int
	TenantID         string
}

// WarmTargetSource yields the currently active dag_versions and their effective
// warm target. It is implemented on the storage/scheduler side (it reads active
// runs and their cached specs and applies the operator's clamp/fallback), and is
// defined HERE so the executor's reconciler depends on a narrow capability rather
// than importing the scheduler or storage package (which would be a dependency
// cycle — storage already imports executor).
type WarmTargetSource interface {
	ActiveWarmTargets(ctx context.Context) ([]WarmTarget, error)
}

// BusyWarmWorkerSource yields the set of warm-worker pod names currently serving a
// `running` attempt (ADR 0058 N1d-b): a warm worker is BUSY iff some `running`
// task_instance is durably bound to it (warm_worker_id = the pod's own name — the
// binding landed in N1d-a1/a2). Returned as a set keyed by pod name so the
// reconciler classifies each live pod in O(1).
//
// Implemented on the storage side and defined HERE so the reconciler depends on a
// narrow capability rather than importing storage. With warm pools off no TI is
// ever bound, so the set is always empty and every worker classifies as idle —
// byte-for-byte today's dedicated pod-per-task behavior.
type BusyWarmWorkerSource interface {
	ListBusyWarmWorkerPods(ctx context.Context) (map[string]bool, error)
}

// WarmPodInfo identifies one existing warm-worker pod and the dag_version it
// serves (read from its labels). The reconciler counts these per version.
//
// Terminal marks a warm pod that has reached a terminal phase (Succeeded or
// Failed). Warm pods are RestartPolicy:Never and the agent has no reconnect, so
// a crashed/drained/finished worker lingers as a terminal pod that can never
// serve again. The reconciler must NOT count terminal pods toward the target
// (or a dead worker never gets replaced) and always reaps them.
// TenantID is the tenant that owns the dag_version this pod serves, read from the
// pod's leoflow.io/tenant-id label (M4). The reconciler counts a tenant's live
// pods by it — including pods of a draining/inactive version, which are absent
// from the active targets — so the per-tenant cap sees the tenant's whole warm
// footprint. A pre-label pod (rolling upgrade) carries "" here; the reconciler
// attributes it via its version when resolvable and NEVER deletes it for the cap.
type WarmPodInfo struct {
	Name         string
	DagVersionID string
	Terminal     bool
	TenantID     string
}

// WarmPodClient is the cluster side of warm-pool reconciliation: list the warm
// fleet, create a new warm worker for a target (which mints the bootstrap token,
// builds the pod via BuildWarmPod, and Creates it — the auth/config-aware half,
// wired in main.go), delete one by name, and manage the per-dag-version GC-anchor
// ConfigMap (ADR 0058 D11). Kept as a narrow seam so the reconciler is unit-tested
// with a fake and the executor imports neither auth nor config. KubernetesWarmPods
// is the production implementation.
//
// EnsureWarmAnchor creates (idempotently) the version's anchor ConfigMap and
// returns its UID; every warm pod created for the version is stamped with an
// ownerReference to it, so on control-plane loss / namespace teardown the pods are
// cascade-GC'd. CreateWarmPod threads that anchor name+UID onto the pod. The
// reconciler ensures the anchor before any create and deletes it (DeleteWarmAnchor)
// ONLY once the version has fully drained to zero live pods — so the cascade is
// always a no-op and never kills a live attempt.
type WarmPodClient interface {
	ListWarmPods(ctx context.Context) ([]WarmPodInfo, error)
	CreateWarmPod(ctx context.Context, t WarmTarget, anchorName, anchorUID string) error
	DeleteWarmPod(ctx context.Context, name string) error
	EnsureWarmAnchor(ctx context.Context, dagVersionID string) (uid string, err error)
	DeleteWarmAnchor(ctx context.Context, dagVersionID string) error
}

// WarmPoolReconciler maintains the IDLE warm-worker buffer per active dag_version
// (ADR 0058 N1b2b + N1d-b, model A2). Each tick it reads the active targets, the
// existing warm fleet, and the busy set (pods serving a running attempt), then per
// version partitions the live pods into BUSY and IDLE and:
//   - CREATEs enough workers to restore EffectiveMinIdle IDLE workers, capped by
//     MaxPoolSize (the total ceiling) — so the pool grows past min_idle under load
//     and never exceeds the cap;
//   - DELETEs only EXCESS IDLE workers (idle over the target), never a busy one, so
//     a scale-down or a drain never kills an in-flight attempt (review M1);
//   - reaps terminal pods unconditionally (H1);
//   - drains a no-longer-active version (target 0) down to its idle workers +
//     terminal pods, LEAVING busy workers to finish (they are deleted a later tick
//     once idle).
//
// It is leader-gated (run on a gated ticker, like the pod reconciler) so at
// replicaCount>1 only the leader mutates the fleet. Idempotent (it reconciles to
// the idle target, so re-running converges), panic-safe and per-version isolated
// (one bad dag_version never blocks the others), and O(active versions) per tick.
// It is only constructed and started when warm pools are enabled; with them off
// the reconciler never runs, every warm pool stays empty, and dispatch is
// byte-for-byte today's dedicated pod-per-task.
//
// The busy set is a hard safety input: without it every worker looks idle and a
// busy worker could be deleted, so a nil busy source or a busy-list error aborts
// the tick with zero mutations (do-no-harm).
//
// GC anchor (D11): before creating any pod for a version the reconciler ensures a
// per-dag-version anchor ConfigMap and stamps every warm pod with an ownerReference
// to it, so on control-plane loss / namespace teardown the fleet is cascade-GC'd.
// The anchor is create-only during a version's active life and deleted only once an
// inactive version has fully drained to zero pods (the footgun guard in Reconcile),
// so the cascade never kills a live attempt.
type WarmPoolReconciler struct {
	targets WarmTargetSource
	pods    WarmPodClient
	busy    BusyWarmWorkerSource
	logger  *slog.Logger
	rec     DecisionRecorder
	// maxWarmPodsPerTenant is the operator's per-tenant aggregate cap (M4,
	// execution.max_warm_pods_per_tenant): the total warm pods one tenant may hold
	// across all its dag_versions. <= 0 means "no tenant cap" — the reconciler skips
	// all tenant accounting and behaves byte-for-byte as the pre-M4 per-version
	// reconciler (this is what the pre-M4 unit tests exercise). In production it is
	// always >= 1 (boot validation), so the tenant budget always applies.
	maxWarmPodsPerTenant int

	// cache, when set (execution.warm_pool_event_refill), replaces the per-tick
	// LIST with the WarmPodInformer's view, and createParallelism bounds the
	// creates a tick issues at once. Nil keeps the live LIST and serial creates.
	cache             WarmPodCache
	createParallelism int
}

// WarmPodCache is the reconciler's cached read of the warm fleet (the
// WarmPodInformer), plus create expectations: a create the cache has not
// observed yet holds back further creates for its version, so a lagging cache
// cannot push a pool past its target or MaxPoolSize.
type WarmPodCache interface {
	CachedWarmPods() ([]WarmPodInfo, bool)
	ExpectCreate(dagVersionID string)
	CreateFailed(dagVersionID string)
	CreatesPending(dagVersionID string) bool
}

// SetCache switches the reconciler to the cached fleet view and parallel
// creates bounded by parallelism (values below 1 mean 1). The cascading anchor
// delete is still confirmed with a live LIST.
func (r *WarmPoolReconciler) SetCache(c WarmPodCache, parallelism int) {
	r.cache = c
	r.createParallelism = max(parallelism, 1)
}

// listFleet reads the warm fleet from the synced cache when there is one, else
// live. fromCache tells the caller the view may lag the cluster.
func (r *WarmPoolReconciler) listFleet(ctx context.Context) (pods []WarmPodInfo, fromCache bool, err error) {
	if r.cache != nil {
		if cached, synced := r.cache.CachedWarmPods(); synced {
			return cached, true, nil
		}
	}
	pods, err = r.pods.ListWarmPods(ctx)
	return pods, false, err
}

// anchorDrainedLive confirms with a live LIST that no pod of dagVersionID
// remains before its anchor is deleted. The delete cascades to every pod still
// referencing the anchor, so a cached "drained" reading never authorizes it.
func (r *WarmPoolReconciler) anchorDrainedLive(ctx context.Context, dagVersionID string) bool {
	live, err := r.pods.ListWarmPods(ctx)
	if err != nil {
		r.logger.ErrorContext(ctx, "confirming a drained warm version live failed; keeping its anchor", "dag_version", dagVersionID, "error", err)
		r.record("warm_pool_anchor_confirm_error")
		return false
	}
	for _, p := range live {
		if p.DagVersionID == dagVersionID {
			return false
		}
	}
	return true
}

// NewWarmPoolReconciler builds a reconciler over the given target source, pod
// client, and busy-worker source. busy is REQUIRED: it classifies each live pod as
// busy or idle so scale-down never kills an in-flight attempt (ADR 0058 N1d-b M1);
// without it every worker would look idle and a busy worker could be deleted, so a
// nil busy source (or a busy-list error at tick time) makes the tick do nothing —
// do-no-harm. maxWarmPodsPerTenant is the per-tenant aggregate cap (M4); <= 0
// disables tenant accounting (pre-M4 per-version behavior). logger and rec
// (metrics) are optional — a nil logger falls back to the default and a nil rec
// skips metering.
func NewWarmPoolReconciler(targets WarmTargetSource, pods WarmPodClient, busy BusyWarmWorkerSource, maxWarmPodsPerTenant int, logger *slog.Logger, rec DecisionRecorder) *WarmPoolReconciler {
	if logger == nil {
		logger = slog.Default()
	}
	return &WarmPoolReconciler{targets: targets, pods: pods, busy: busy, maxWarmPodsPerTenant: maxWarmPodsPerTenant, logger: logger, rec: rec}
}

// Reconcile brings the warm fleet in line with the active targets for one tick. It
// returns an error only when it could not read the world (the target source or the
// pod list failed) so the ticker logs it and retries next tick without acting on a
// bad view; per-version and per-pod failures are logged/metered and isolated, and
// never abort the sweep.
func (r *WarmPoolReconciler) Reconcile(ctx context.Context) error {
	targets, err := r.targets.ActiveWarmTargets(ctx)
	if err != nil {
		return err
	}
	existing, fromCache, err := r.listFleet(ctx)
	if err != nil {
		return err
	}

	// Read the busy set ONCE per tick. This is the safety input: it tells the
	// reconciler which live workers are serving a running attempt so scale-down and
	// drain delete only IDLE workers (ADR 0058 N1d-b M1). Without it every worker
	// looks idle and a busy worker could be deleted mid-attempt, so a nil source or
	// a store error here ABORTS the tick with zero creates/deletes (do-no-harm):
	// log, meter, and return so the ticker retries next tick on a good view.
	if r.busy == nil {
		r.logger.ErrorContext(ctx, "warm-pool reconcile skipped: no busy-worker source wired; cannot classify workers, taking no action")
		r.record("warm_pool_busy_source_missing")
		return nil
	}
	busy, err := r.busy.ListBusyWarmWorkerPods(ctx)
	if err != nil {
		r.logger.ErrorContext(ctx, "warm-pool reconcile aborted: listing busy warm workers failed; taking no action this tick to avoid deleting a busy worker", "error", err)
		r.record("warm_pool_busy_list_error")
		return nil
	}

	// Group the existing fleet by the dag_version each worker serves.
	byVersion := make(map[string][]WarmPodInfo, len(existing))
	for _, p := range existing {
		byVersion[p.DagVersionID] = append(byVersion[p.DagVersionID], p)
	}

	// Ration each active version's create allowance under the per-tenant aggregate
	// cap (M4): reserve every version's promised idle floor, then ration the tenant's
	// remaining budget across its versions. allow[dagVersionID] is the max NEW warm
	// pods this version may create this tick under the tenant budget; nil means no
	// tenant cap is configured (pre-M4 per-version behavior — every version gets its
	// full idle shortfall). The cap is enforced by capping creates only; deletes
	// (excess idle + terminal) are untouched, so a busy worker is never killed to
	// honor the cap.
	allow := r.createAllowances(ctx, targets, existing, busy)

	// Reconcile every active version to its target, then drain any version that is
	// present in the fleet but no longer active (its target is implicitly 0). A
	// draining version has target 0, so it never creates — its allowance is moot; it
	// still counts against its tenant's live budget (its pods carry the tenant label
	// and were summed into tenant_live above).
	active := make(map[string]bool, len(targets))
	for _, t := range targets {
		active[t.DagVersionID] = true
		// An ACTIVE version's anchor is create-only during its life: reconcileVersion
		// ensures it before creating, and it is NEVER deleted here — so a scale-down
		// (deleting idle excess) can never trigger the cascade. The remaining-pod
		// count is irrelevant for an active version and deliberately ignored.
		r.reconcileVersion(ctx, t, byVersion[t.DagVersionID], busy, allowanceFor(allow, t.DagVersionID))
	}
	for dagVersionID, have := range byVersion {
		if active[dagVersionID] {
			continue
		}
		remaining := r.reconcileVersion(ctx, WarmTarget{DagVersionID: dagVersionID, EffectiveMinIdle: 0}, have, busy, unlimitedAllowance)
		// DELETE FOOTGUN GUARD (ADR 0058 D11). The anchor's ownerReference makes
		// deleting it CASCADE-delete every pod that still references it. So the anchor
		// is deleted ONLY when, in THIS tick, the version is INACTIVE (not in
		// ActiveWarmTargets, drained with target 0) AND the drain above left ZERO live
		// pods referencing it (remaining == 0 — busy workers, un-reaped idle, and
		// failed deletes all keep it > 0). At zero pods the cascade is a no-op, so it
		// can NEVER kill a live (busy OR idle) warm attempt. The anchor's normal role
		// is create-only; this delete is bookkeeping to stop the anchor leaking once
		// its version is gone — the cascade itself is the BACKSTOP for external
		// teardown (namespace delete / uninstall), never for scale-down.
		if remaining == 0 && (!fromCache || r.anchorDrainedLive(ctx, dagVersionID)) {
			r.deleteWarmAnchor(ctx, dagVersionID)
		}
	}
	return nil
}

// unlimitedAllowance is the sentinel create allowance meaning "no per-tenant cap
// applies to this version" (either no cap is configured, or the version is
// draining and cannot create anyway). reconcileVersion treats a negative allowance
// as unbounded and falls back to the per-version floor/MaxPoolSize gate alone.
const unlimitedAllowance = -1

// allowanceFor reads a version's rationed create allowance. A nil map (no tenant
// cap configured) yields unlimited, preserving the pre-M4 per-version behavior.
func allowanceFor(allow map[string]int, dagVersionID string) int {
	if allow == nil {
		return unlimitedAllowance
	}
	return allow[dagVersionID]
}

// createAllowances computes, per active dag_version, the maximum number of NEW
// warm pods it may create this tick under the per-tenant aggregate cap (M4). It
// returns nil when no cap is configured (maxWarmPodsPerTenant <= 0), so the caller
// falls back to the unbounded pre-M4 per-version behavior.
//
// The algorithm is RESERVE-then-RATION per tenant, tuned so the cap never starves
// a legitimate tenant and never has to delete a busy worker:
//
//   - min_idle is SACRED. The tenant's effective budget is
//     max(max_warm_pods_per_tenant, Σ EffectiveMinIdle over its ACTIVE versions).
//     If the promised floors already exceed the cap it is a misconfiguration, not a
//     reason to starve work: the budget is raised to the floor sum so every floor
//     is still creatable, and warm_pool_tenant_cap_below_min_idle_sum is logged +
//     metered loudly so an operator fixes the config.
//   - tenant_live is the count of the tenant's LIVE (non-terminal) warm pods across
//     the whole listed fleet, grouped by the tenant label — so it includes pods of a
//     draining/inactive version (absent from the active targets). A pre-label pod
//     ("" tenant) is attributed via its version→tenant mapping when resolvable
//     (metered warm_pool_untenanted_pod) and otherwise left uncounted — it is never
//     deleted for the cap.
//   - headroom = budget − tenant_live. It is rationed across the tenant's active
//     versions still short of their idle floor, in a STABLE order (by DagVersionID),
//     each taking min(idle shortfall, its own MaxPoolSize headroom, remaining
//     headroom). Because the budget already covers Σ floors, every floor is granted
//     unless busy pods have legitimately consumed the budget — and in that case the
//     cap holds by refusing to create, never by deleting the busy workers (they are
//     reaped on a later tick once idle).
func (r *WarmPoolReconciler) createAllowances(ctx context.Context, targets []WarmTarget, existing []WarmPodInfo, busy map[string]bool) map[string]int {
	if r.maxWarmPodsPerTenant <= 0 {
		return nil
	}

	// Active versions: their owning tenant, the per-tenant floor sum, and the set of
	// versions to ration across.
	versionTenant := make(map[string]string, len(targets))
	tenantFloors := make(map[string]int)
	tenantVersions := make(map[string][]WarmTarget)
	for _, t := range targets {
		versionTenant[t.DagVersionID] = t.TenantID
		tenantFloors[t.TenantID] += t.EffectiveMinIdle
		tenantVersions[t.TenantID] = append(tenantVersions[t.TenantID], t)
	}

	stat, tenantLive := r.countLiveByTenant(existing, busy, versionTenant)

	// Reserve-then-ration per tenant: raise the budget to the promised floor sum,
	// then ration the tenant's remaining headroom across its versions.
	allow := make(map[string]int, len(targets))
	for tenant, versions := range tenantVersions {
		budget := r.tenantBudget(ctx, tenant, tenantFloors[tenant])
		r.rationTenant(versions, stat, budget-tenantLive[tenant], allow)
	}
	return allow
}

// warmVerStat is one dag_version's live-pod accounting for one tick: idle is the
// count of live non-busy workers, live is idle + busy (terminal pods excluded).
type warmVerStat struct{ idle, live int }

// countLiveByTenant classifies the listed fleet ONCE (no second query): per
// dag_version it counts live idle/total workers, and per tenant it counts live
// pods attributed by the tenant label. A pre-label pod ("" tenant) is attributed
// via its version→tenant mapping when resolvable (metered warm_pool_untenanted_pod)
// and otherwise left uncounted — it is never deleted for the cap either way.
func (r *WarmPoolReconciler) countLiveByTenant(existing []WarmPodInfo, busy map[string]bool, versionTenant map[string]string) (stat map[string]warmVerStat, tenantLive map[string]int) {
	stat = make(map[string]warmVerStat, len(versionTenant))
	tenantLive = make(map[string]int)
	for _, p := range existing {
		if p.Terminal {
			continue
		}
		s := stat[p.DagVersionID]
		s.live++
		if !busy[p.Name] {
			s.idle++
		}
		stat[p.DagVersionID] = s

		tenant := p.TenantID
		if tenant == "" {
			vt, ok := versionTenant[p.DagVersionID]
			if !ok || vt == "" {
				r.record("warm_pool_untenanted_pod")
				continue // unmappable: never counted, never deleted for the cap
			}
			r.record("warm_pool_untenanted_pod")
			tenant = vt
		}
		tenantLive[tenant]++
	}
	return stat, tenantLive
}

// tenantBudget is the tenant's effective aggregate budget: the operator's cap,
// raised to the promised idle-floor sum when the floors exceed the cap so no
// promised buffer is starved (a misconfiguration, logged + metered loudly).
func (r *WarmPoolReconciler) tenantBudget(ctx context.Context, tenant string, floorSum int) int {
	if floorSum <= r.maxWarmPodsPerTenant {
		return r.maxWarmPodsPerTenant
	}
	r.logger.WarnContext(ctx, "warm-pool per-tenant cap is below the tenant's promised idle-floor sum; honoring the floors and raising the effective budget so no promised idle buffer is starved (fix execution.max_warm_pods_per_tenant or the DAGs' min_idle_workers)",
		"tenant", tenant, "max_warm_pods_per_tenant", r.maxWarmPodsPerTenant, "min_idle_sum", floorSum)
	r.record("warm_pool_tenant_cap_below_min_idle_sum")
	return floorSum
}

// rationTenant distributes a tenant's create headroom across its versions in a
// stable order (by DagVersionID), writing each version's create allowance into
// allow. Each version takes min(idle shortfall to its floor, its own MaxPoolSize
// headroom, remaining tenant headroom). Because the budget already covers the
// floor sum, every floor is granted unless busy pods have legitimately consumed
// the budget — in which case the cap holds by granting less, never by deleting.
func (r *WarmPoolReconciler) rationTenant(versions []WarmTarget, stat map[string]warmVerStat, headroom int, allow map[string]int) {
	if headroom < 0 {
		headroom = 0
	}
	sort.Slice(versions, func(i, j int) bool { return versions[i].DagVersionID < versions[j].DagVersionID })
	for _, t := range versions {
		s := stat[t.DagVersionID]
		maxPool := t.MaxPoolSize
		if maxPool < t.EffectiveMinIdle {
			maxPool = t.EffectiveMinIdle
		}
		want := t.EffectiveMinIdle - s.idle // idle shortfall to the floor
		if poolHeadroom := maxPool - s.live; poolHeadroom < want {
			want = poolHeadroom
		}
		if want < 0 {
			want = 0
		}
		grant := want
		if headroom < grant {
			grant = headroom
		}
		allow[t.DagVersionID] = grant
		headroom -= grant
	}
}

// reconcileVersion brings one dag_version's IDLE warm-worker buffer to its target
// under model A2 (ADR 0058 N1d-b), isolated behind a recover so a panic in one
// version's create/delete path never takes down the whole sweep. busy is the
// tick's live busy set (pod names serving a running attempt).
//
// The unit here is IDLE workers, not total workers: EffectiveMinIdle is how many
// idle workers to keep ready and MaxPoolSize is the total ceiling. So the pool
// grows past min_idle under load (busy workers do not consume the idle buffer) and
// never exceeds the ceiling, and scale-down/drain deletes only IDLE workers so an
// in-flight attempt is never killed (M1).
//
// createAllowance is the per-tenant cap's gate on NEW pods this tick (M4): the
// version creates min(idle shortfall, MaxPoolSize headroom, createAllowance). A
// negative allowance (unlimitedAllowance) means no tenant cap applies, so the
// create is bounded by the per-version floor/MaxPoolSize gate alone — the pre-M4
// behavior. The allowance gates CREATES only; the terminal reap and the
// excess-idle delete are unchanged, so the cap never deletes a busy worker.
func (r *WarmPoolReconciler) reconcileVersion(ctx context.Context, t WarmTarget, have []WarmPodInfo, busy map[string]bool, createAllowance int) (remaining int) {
	defer func() {
		if p := recover(); p != nil {
			r.logger.ErrorContext(ctx, "warm-pool reconcile panicked for a dag_version; other versions unaffected",
				"dag_version", t.DagVersionID, "panic", p)
			r.record("warm_pool_version_panic")
			// Conservative on panic: report every listed pod as still present so the
			// caller's footgun guard never deletes an anchor after a half-done drain.
			remaining = len(have)
		}
	}()

	// Reap every terminal warm pod first and keep only the live ones. Warm pods
	// are RestartPolicy:Never with no agent reconnect, so a Succeeded/Failed pod
	// is a dead worker: it can never serve again, must not count toward the
	// target (or a crashed/drained worker would never be replaced and the pool
	// would silently die), and must not leak. This runs regardless of target, so
	// the drain path (inactive version, target 0) reaps terminal pods too. Then
	// partition the live pods into BUSY (currently serving a running attempt) and
	// IDLE (the rest) — the reconciler only ever creates toward, or deletes from,
	// the IDLE set; busy workers are never touched (M1).
	//
	// `remaining` counts pods that STILL reference the version's anchor after this
	// tick's deletes (busy workers, kept idle, and any delete/reap that FAILED). The
	// caller uses it as the footgun guard: an inactive version's anchor is deleted
	// only when remaining == 0, so the cascade can never catch a live pod.
	idle := make([]WarmPodInfo, 0, len(have))
	busyCount := 0
	for _, p := range have {
		if p.Terminal {
			if !r.deleteWarmPod(ctx, t, p, "deleting terminal warm worker") {
				remaining++ // reap failed: the pod still exists and references the anchor
			}
			continue
		}
		if busy[p.Name] {
			busyCount++
			continue
		}
		idle = append(idle, p)
	}
	remaining += busyCount // busy workers are never deleted this tick — they still reference the anchor
	live := busyCount + len(idle)

	// Effective ceiling: the idle buffer must always be creatable, so never let the
	// ceiling fall below the idle target even if an operator (or a stale target)
	// hands us MaxPoolSize < EffectiveMinIdle. In practice MaxPoolSize >=
	// EffectiveMinIdle already (boot validation + upstream clamp), so this is a
	// defensive floor, not a normal path.
	maxPool := t.MaxPoolSize
	if maxPool < t.EffectiveMinIdle {
		maxPool = t.EffectiveMinIdle
	}

	switch {
	case len(idle) < t.EffectiveMinIdle && live < maxPool:
		// Idle buffer is short AND the pool is below its total ceiling: create
		// enough to restore the buffer, but never past the ceiling OR the tenant's
		// rationed allowance. create = min(idle shortfall, headroom to the ceiling,
		// tenant allowance). Each create is isolated so one apiserver rejection does
		// not stop the rest of this version's workers.
		create := t.EffectiveMinIdle - len(idle)
		if headroom := maxPool - live; headroom < create {
			create = headroom
		}
		if createAllowance >= 0 && create > createAllowance {
			create = createAllowance
		}
		remaining += len(idle) // no idle deleted on the create path; every idle pod survives
		r.ensureAnchorAndCreate(ctx, t, create)
	case len(idle) > t.EffectiveMinIdle:
		// Too many idle workers (over target, or the version is no longer active so
		// the idle target is 0): delete the excess IDLE workers only. Busy workers
		// are excluded from `idle` entirely, so a scale-down or a drain can never
		// kill an in-flight attempt (M1) — a busy worker is deleted only on a later
		// tick, once it has gone idle. Deleting the tail is arbitrary but stable:
		// every idle worker of a version is interchangeable.
		remaining += t.EffectiveMinIdle // the idle workers kept within the target survive
		for _, p := range idle[t.EffectiveMinIdle:] {
			if !r.deleteWarmPod(ctx, t, p, "deleting excess idle warm worker") {
				remaining++ // delete failed: the pod still exists and references the anchor
			}
		}
	default:
		remaining += len(idle) // steady state / at-ceiling: the idle workers survive
	}
	return remaining
}

// ensureAnchorAndCreate creates `create` new warm workers for the version, after
// ensuring the version's GC anchor exists and learning its UID so every pod is
// stamped with an ownerReference to it (ADR 0058 D11). It is the ONLY place the
// reconciler creates the anchor — create-only during a version's active life. If
// the anchor cannot be ensured it skips ALL creates for this version this tick
// (do-no-harm: a missed create beats a warm pod with no GC owner) and lets other
// versions proceed. Each create is isolated so one apiserver rejection does not
// stop the rest of this version's workers.
func (r *WarmPoolReconciler) ensureAnchorAndCreate(ctx context.Context, t WarmTarget, create int) {
	if create <= 0 {
		return
	}
	if r.cache != nil && r.cache.CreatesPending(t.DagVersionID) {
		// Earlier creates are not in the cache yet; it would undercount this pool.
		// Their watch events re-trigger the reconcile once they land.
		r.record("warm_pool_create_held_pending")
		return
	}
	uid, err := r.pods.EnsureWarmAnchor(ctx, t.DagVersionID)
	if err != nil {
		r.logger.ErrorContext(ctx, "ensuring warm anchor; skipping creates for this dag_version this tick to avoid a warm pod with no GC owner",
			"dag_version", t.DagVersionID, "error", err)
		r.record("warm_pool_anchor_ensure_error")
		return
	}
	anchorName := warmAnchorName(t.DagVersionID)
	if r.cache == nil {
		for i := 0; i < create; i++ {
			r.createOne(ctx, t, anchorName, uid)
		}
		return
	}
	// Event-driven refill: issue the creates concurrently, at most
	// createParallelism at once, so refilling N workers costs about one apiserver
	// round trip per batch instead of N in a row. Each raises its expectation
	// before the call so the cache's lag cannot cause a duplicate on the next tick.
	sem := make(chan struct{}, r.createParallelism)
	var wg sync.WaitGroup
	for i := 0; i < create; i++ {
		sem <- struct{}{}
		wg.Add(1)
		go func() {
			defer func() { <-sem; wg.Done() }()
			r.cache.ExpectCreate(t.DagVersionID)
			created := false
			defer func() {
				if p := recover(); p != nil {
					r.logger.ErrorContext(ctx, "warm worker create panicked", "dag_version", t.DagVersionID, "panic", p)
					r.record("warm_pool_version_panic")
				}
				if !created {
					r.cache.CreateFailed(t.DagVersionID)
				}
			}()
			created = r.createOne(ctx, t, anchorName, uid)
		}()
	}
	wg.Wait()
}

// createOne creates one warm worker, logging and metering a failure. It reports
// whether the create was accepted.
func (r *WarmPoolReconciler) createOne(ctx context.Context, t WarmTarget, anchorName, uid string) bool {
	if err := r.pods.CreateWarmPod(ctx, t, anchorName, uid); err != nil {
		r.logger.ErrorContext(ctx, "creating warm worker",
			"dag_version", t.DagVersionID, "image", t.Image, "error", err)
		r.record("warm_pool_create_error")
		return false
	}
	r.record("warm_pool_pod_created")
	return true
}

// deleteWarmPod deletes one warm worker by name, logging/metering a failure
// without aborting the sweep. msg names why (terminal cleanup vs excess). It
// returns true iff the delete succeeded, so the caller can tell whether the pod
// still references its anchor (the footgun guard must not delete an anchor while
// any pod — including one whose delete failed — remains).
func (r *WarmPoolReconciler) deleteWarmPod(ctx context.Context, t WarmTarget, p WarmPodInfo, msg string) bool {
	if err := r.pods.DeleteWarmPod(ctx, p.Name); err != nil {
		r.logger.ErrorContext(ctx, msg,
			"dag_version", t.DagVersionID, "pod", p.Name, "error", err)
		r.record("warm_pool_delete_error")
		return false
	}
	r.record("warm_pool_pod_deleted")
	return true
}

// deleteWarmAnchor deletes a fully-drained inactive version's GC anchor, metering
// the outcome. It is called by Reconcile ONLY when the version has zero live pods
// remaining (the footgun guard), so the ownerReference cascade is a no-op.
func (r *WarmPoolReconciler) deleteWarmAnchor(ctx context.Context, dagVersionID string) {
	if err := r.pods.DeleteWarmAnchor(ctx, dagVersionID); err != nil {
		r.logger.ErrorContext(ctx, "deleting drained warm anchor",
			"dag_version", dagVersionID, "error", err)
		r.record("warm_pool_anchor_delete_error")
		return
	}
	r.record("warm_pool_anchor_deleted")
}

func (r *WarmPoolReconciler) record(decision string) {
	if r.rec != nil {
		r.rec.RecordSchedulerDecision(decision)
	}
}
