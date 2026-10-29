package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"syscall"
	"time"

	"k8s.io/client-go/kubernetes"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/egress"
	"github.com/dexadata/dexaflow/internal/executor"
	"github.com/dexadata/dexaflow/internal/failurealert"
	"github.com/dexadata/dexaflow/internal/gatedtick"
	"github.com/dexadata/dexaflow/internal/logs"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/retention"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/xcom"
)

// reconcileInterval is how often the pod reconciler sweeps for failed pods.
const reconcileInterval = 30 * time.Second

// cleanupInterval is how often expired XCom index rows and old logs are purged.
const cleanupInterval = time.Hour

// logRetention is how long task logs are kept before garbage collection.
const logRetention = 30 * 24 * time.Hour

// lowDiskWarnBytes is the free-space threshold below which the janitor warns: the
// embedded datastore (managed Postgres + logs) lives on this filesystem, and a
// full disk makes Postgres fail writes with a cryptic error.
const lowDiskWarnBytes = 1 << 30 // 1 GiB

// operations are idempotent, so it is safe on every replica.
func startCleanup(ctx context.Context, idx *storage.XComIndex, sink logs.Sink, dataDir string, logger *slog.Logger) {
	// Only a self-managing sink prunes: the disk sink deletes old files, while the
	// object-store sink leaves retention to bucket lifecycle policy and does not
	// implement logs.Pruner, so the janitor skips it.
	pruner, canPrune := sink.(logs.Pruner)
	go func() {
		t := time.NewTicker(cleanupInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				gatedtick.SafeCycle("cleanup", logger, func() {
					if err := idx.PurgeExpired(ctx); err != nil {
						logger.Error("purging expired xcom index", "error", err)
					}
					if canPrune {
						if err := pruner.Prune(time.Now(), logRetention); err != nil {
							logger.Error("pruning old logs", "error", err)
						}
					}
					if free, derr := dirFreeBytes(dataDir); derr == nil && lowDisk(free, lowDiskWarnBytes) {
						logger.Warn("low disk space for the datastore", "dir", dataDir,
							"free_mb", free/(1<<20), "threshold_mb", uint64(lowDiskWarnBytes)/(1<<20))
					}
				})
			}
		}
	}()
}

// lowDisk reports whether free is below the threshold (both in bytes).
func lowDisk(free, threshold uint64) bool { return free < threshold }

// dirFreeBytes returns the bytes available on the filesystem holding dir.
func dirFreeBytes(dir string) (uint64, error) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0, fmt.Errorf("statfs %s: %w", dir, err)
	}
	return st.Bavail * uint64(st.Bsize), nil //nolint:gosec // G115: a filesystem block size is never negative
}

// xcomSweepInterval is how often the embedded Postgres XCom store is swept of
// expired rows. Redis expires keys natively; Postgres needs a sweep (ADR 0026).
const xcomSweepInterval = 10 * time.Minute

// cliDeviceSweepInterval is how often the server forgets device sign-ins
// of the CLI ten minutes past their expiry (ADR 0081), so the address a
// sign-in was started from is gone about 21 minutes after the start at the
// latest, even when nobody starts another.
const cliDeviceSweepInterval = time.Minute

// cliDeviceSweeper deletes the expired device sign-ins; storage implements
// it.
type cliDeviceSweeper interface {
	DeleteExpiredCLIDeviceAuthorizations(ctx context.Context) (int64, error)
}

// startCLIDeviceSweep runs runCLIDeviceSweep every cliDeviceSweepInterval
// until ctx ends. Every replica runs it: the delete is idempotent and cheap.
func startCLIDeviceSweep(ctx context.Context, store cliDeviceSweeper, logger *slog.Logger) {
	t := time.NewTicker(cliDeviceSweepInterval)
	go func() {
		defer t.Stop()
		runCLIDeviceSweep(ctx, t.C, store, logger)
	}()
}

// runCLIDeviceSweep sweeps the expired device sign-ins at each tick until
// ctx ends. A failed sweep is logged and retried at the next tick.
func runCLIDeviceSweep(ctx context.Context, ticks <-chan time.Time, store cliDeviceSweeper, logger *slog.Logger) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticks:
			gatedtick.SafeCycle("cli-device-sweep", logger, func() {
				if n, err := store.DeleteExpiredCLIDeviceAuthorizations(ctx); err != nil {
					logger.Error("sweeping expired cli device sign-ins", "error", err)
				} else if n > 0 {
					logger.Debug("swept expired cli device sign-ins", "rows", n)
				}
			})
		}
	}
}

// startXComSweep periodically deletes expired rows from the embedded Postgres
// XCom store (the durable equivalent of Redis's native key expiry, ADR 0026).
func startXComSweep(ctx context.Context, backend *xcom.PostgresBackend, logger *slog.Logger) {
	go func() {
		t := time.NewTicker(xcomSweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				gatedtick.SafeCycle("xcom-sweep", logger, func() {
					if n, err := backend.DeleteExpired(ctx); err != nil {
						logger.Error("sweeping expired xcom", "error", err)
					} else if n > 0 {
						logger.Debug("swept expired xcom", "rows", n)
					}
				})
			}
		}
	}()
}

// startRetention starts the retention janitor (D5) when it has work and
// reports whether it did: a run or audit class, or the usage events window
// while usageRecording is on. Runs and the audit log are kept by default and
// usage events are not recorded, so a default install starts nothing and
// deletes nothing. The janitor runs on the scheduler leader only and rechecks
// leadership before every batch.
func startRetention(ctx context.Context, sec config.RetentionSection, usageRecording bool, store retention.Store, leading func() bool, rec retention.Recorder, logger *slog.Logger) bool {
	if !sec.Active(usageRecording) {
		return false
	}
	j := retention.New(store, retentionConfig(sec), rec, logger)
	j.SetLeading(leading)
	logger.Info("retention janitor enabled", "dag_runs_days", sec.DagRunsDays, "audit_log_days", sec.AuditLogDays,
		"usage_events_days", sec.UsageEventsDays, "dry_run", sec.DryRun, "interval", sec.Interval, "batch_size", sec.BatchSize, "max_rows_per_cycle", sec.MaxRowsPerCycle)
	gatedtick.Start(ctx, "retention", sec.Interval, leading, logger, func() { j.RunCycle(ctx) })
	return true
}

// retentionConfig maps the config section onto the janitor's settings.
func retentionConfig(sec config.RetentionSection) retention.Config {
	return retention.Config{
		DagRunsDays: sec.DagRunsDays, AuditLogDays: sec.AuditLogDays, UsageEventsDays: sec.UsageEventsDays, DryRun: sec.DryRun,
		BatchSize: sec.BatchSize, BatchPause: sec.BatchPause, MaxRowsPerCycle: sec.MaxRowsPerCycle,
	}
}

// startMaintenance runs the leader's periodic recovery work, the pod
// reconciler's sweep and the execution reapers, as ONE ordered cycle on one
// leader-gated ticker, every reconcileInterval: reconcile, then reap. The
// reapers used to run from the scheduler's 1s tick while the reconciler swept on
// its own 30s timer; two independent clocks gave no ordering, so a reaper could
// judge a task pod "gone" at 1s that the reconciler would have recovered as
// succeeded at its next sweep. In one cycle the reap always sees post-reconcile
// state, and the reaper's settling gate reads the reconciler's sweep record to
// know a sweep has completed under this leadership. The cost is detection
// latency: a stuck run/TI is now noticed up to one interval (30s) later than at
// the 1s tick, negligible against 60s–5m thresholds, and reaping is a backstop,
// never the primary path. Each phase runs under its own one-interval budget
// (maintenancePhaseTimeout). Lite/subprocess never calls this: no pods, no
// reaping.
func startMaintenance(ctx context.Context, cs kubernetes.Interface, namespace string, reporter executor.OutcomeReporter, confirmer executor.InfraConfirmer, overrides overrideObservers, reaper *executor.Reaper, leading func() bool, logger *slog.Logger, snapshotter executor.PodSnapshotter, settled executor.SettledRunChecker) {
	rec := executor.NewReconciler(cs, namespace, reporter)
	// The reconciler confirms the reapers' provisional infra marks (ADR 0052
	// amendment, part 2); the caller makes the marks provisional in step.
	rec.SetInfraConfirmer(confirmer)
	// A durable SUCCESS settled over an infra mark is metered and noted in the
	// attempt's log (ADR 0052 amendment, part 1).
	overrides.wire(rec)
	// Opt-in (executor.collect_settled_run_pods): collect a settled run's
	// finished pods in one DeleteCollection; nil keeps the age-based GC only.
	if settled != nil {
		rec.SetSettledRunCollection(settled)
	}
	// Read task pods from the shared informer cache instead of a live LIST every
	// tick when the informer is wired (PR-10); nil keeps the live LIST.
	if snapshotter != nil {
		rec.SetPodSnapshotter(snapshotter)
	}
	reaper.SetLastSweepCompleted(rec.LastSweepCompletedAt)
	gatedtick.Start(ctx, "maintenance", reconcileInterval, leading, logger, func() {
		maintenanceCycle(ctx, maintenancePhaseTimeout, rec.Reconcile, reaper.ReapOnce, logger)
	})
}

// overrideObservers is what makes the reconciler's infra overrides visible:
// the metrics (nil when metrics are off) and the marker sink the system line
// is appended to (nil when the log sink cannot append).
type overrideObservers struct {
	metrics *observability.Metrics
	sink    logs.MarkerSink
}

// wire hands the observers to rec, keeping a nil *Metrics from becoming a
// non-nil recorder.
func (o overrideObservers) wire(rec *executor.Reconciler) {
	var recorder executor.InfraOverrideRecorder
	if o.metrics != nil {
		recorder = o.metrics
	}
	rec.SetInfraOverrideObservers(recorder, o.sink)
}

// settledRunCollection returns the reconciler's settled-run checker when the
// operator turned executor.collect_settled_run_pods on, and nil otherwise.
func settledRunCollection(sec config.ExecutorSection, store executor.SettledRunChecker) executor.SettledRunChecker {
	if !sec.CollectSettledRunPods {
		return nil
	}
	return store
}

// maintenancePhaseTimeout bounds each phase of a maintenance cycle, the
// reconciler's sweep and the reaper pass, separately, at one interval. The
// reapers used to run inside the scheduler tick under its step timeout; sharing
// one goroutine with the reconciler must not lose that bound: a sweep hung on a
// slow apiserver would otherwise starve the reap indefinitely, and a reap that
// issues one live LIST per running task instance (plus lock-blocked UPDATEs)
// would starve the very sweep it depends on. Each phase gets its own FRESH
// budget, so a reconcile that spends all of its own still hands the reap a live
// context, the reaper's destructive gate reads ctx.Err() and a dead context
// would silently disable reaping rather than fail loudly. A cycle can therefore
// take up to two intervals; the ticker coalesces the ticks it overruns, and the
// settling gate's "two cycles inside the grace" still holds by construction.
const maintenancePhaseTimeout = reconcileInterval

// maintenanceCycle is one pass of the maintenance loop: reconcile, then reap,
// in that order, always, each under its own phaseTimeout. A reconcile error
// (the LIST failed) is logged and the reapers still run, they are independent
// backstops with their own fail-closed liveness reads, and whether a leader that
// has never completed a sweep may reap is the reaper's settling gate's decision,
// not this loop's.
func maintenanceCycle(ctx context.Context, phaseTimeout time.Duration, reconcile, reap func(context.Context) error, logger *slog.Logger) {
	runMaintenancePhase(ctx, "pod reconcile", phaseTimeout, reconcile, logger)
	runMaintenancePhase(ctx, "execution reaper", phaseTimeout, reap, logger)
}

// runMaintenancePhase runs one phase under its own deadline. Overrunning the
// budget is a load signal (a large namespace, a slow apiserver), not a bug, so
// it logs at WARN with the budget; any other error logs at ERROR. Nothing is
// logged once the parent context is done, that cancel fan-out at shutdown is
// expected, not a finding.
func runMaintenancePhase(ctx context.Context, name string, timeout time.Duration, fn func(context.Context) error, logger *slog.Logger) {
	pctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	err := fn(pctx)
	switch {
	case ctx.Err() != nil:
		return
	case errors.Is(pctx.Err(), context.DeadlineExceeded):
		logger.Warn(name+" exceeded its maintenance budget", "budget", timeout, "error", err)
	case err != nil:
		logger.Error(name, "error", err)
	}
}

// stagingGCInterval is how often the per-run staging-volume GC sweeps; stagingTTL
// is how long a FAILED run's volume is kept after its terminal time before the
// PVC is deleted (ADR 0022, long enough for a clear+re-run to re-attach the
// data). A SUCCEEDED run's volume is freed immediately, regardless of the TTL.
const (
	stagingGCInterval = time.Minute // frequent so a succeeded run's volume is freed ~at DAG end
	// stagingTTL must stay well above the reconciler's finished-pod grace. A pod
	// preserved by a reaper teardown (#928) still holds kubernetes.io/pvc-protection
	// on the run's staging claim, upstream only skips terminated pods on the newer
	// unused-since path, not on the deletion path, so if this TTL ever dropped near
	// that grace, the GC would mark a claim deleted while a pod it cannot see is
	// still keeping the finalizer on. Today the margin is 24h against 10 minutes.
	stagingTTL = 24 * time.Hour
)

// startStagingGC periodically reclaims per-run staging PVCs from the
// metadatabase-tracked lifecycle: succeeded runs immediately, failed runs after
// the TTL, orphaned volumes (run gone) on sight (ADR 0022).
func startStagingGC(ctx context.Context, cs kubernetes.Interface, namespace string, store executor.StagingStore, leading func() bool, logger *slog.Logger, rec executor.DecisionRecorder) {
	exec := executor.NewKubernetesExecutor(cs, namespace)
	exec.SetStagingStore(store)
	exec.SetLogger(logger)
	if rec != nil {
		exec.SetDecisionRecorder(rec)
	}
	gatedtick.Start(ctx, "staging-gc", stagingGCInterval, leading, logger, func() {
		gatedtick.RunLeaderTick(ctx, "staging gc", stagingGCInterval, gatedtick.LeaderTickPoll, leading, logger, func(tctx context.Context) error {
			return exec.GCStagingClaims(tctx, stagingTTL)
		})
	})
}

// alertHTTPTimeout bounds each on-failure alert POST so a slow or hung channel
// endpoint cannot pile up detached alert goroutines (#424).
const alertHTTPTimeout = 10 * time.Second

// alertHTTPClient builds the client on-failure alerts are posted with. An
// alert's URL comes from a tenant's connection, so with
// scheduler.alerts.block_private_destinations on, the client refuses loopback,
// private, link-local and metadata addresses at dial time (internal/egress).
// Off, it is the plain client alerts have always used.
func alertHTTPClient(cfg config.AlertsSection) (*http.Client, error) {
	if !cfg.BlockPrivateDestinations {
		return &http.Client{Timeout: alertHTTPTimeout}, nil
	}
	policy, err := egress.NewPolicy(cfg.AllowedCIDRs)
	if err != nil {
		return nil, fmt.Errorf("scheduler.alerts.allowed_cidrs: %w", err)
	}
	if reopened := policy.ReopenedSensitive(); len(reopened) > 0 {
		// Not an error: an operator may mean it. But a range that lets tenant
		// alerts reach loopback or a metadata endpoint should be a choice.
		slog.Warn("scheduler.alerts.allowed_cidrs lets alerts reach sensitive destinations",
			"destinations", strings.Join(reopened, ", "))
	}
	return policy.Client(alertHTTPTimeout), nil
}

// connEndpointResolver adapts the connection store to failurealert.EndpointResolver:
// an alert channel's endpoint URL is the connection's decrypted secret (#424).
type connEndpointResolver struct{ repo *storage.Repository }

// ResolveAlertEndpoint returns the connection's endpoint, its secret URL and any
// headers from the connection extra, for the tenant, or an error when it is
// missing/undecryptable.
func (c connEndpointResolver) ResolveAlertEndpoint(ctx context.Context, tenantID, connID string) (failurealert.Endpoint, error) {
	url, headers, err := c.repo.AlertEndpoint(ctx, tenantID, connID)
	if err != nil {
		return failurealert.Endpoint{}, err
	}
	return failurealert.Endpoint{URL: url, Headers: headers}, nil
}

// liteLeadership is the slice of the scheduler the Lite reaper is gated on: when
// this instance acquired leadership (the settling grace), whether it still
// leads, and whether it is stepping down.
type liteLeadership interface {
	LeaderSince() time.Time
	IsLeading() bool
	SteppingDown() bool
}

// newLiteReaper builds the execution reaper for Lite (#916). Lite has no pods,
// so it passes no pod manager, presence cache or warm lister: the pod-lost and
// warm-worker-lost reapers are no-ops, and orphan-run is purely a metadatabase
// signal. Agent-lost and dispatch-lost are gated on the agent process instead of
// a pod (procs): Lite cannot stop an abandoned agent the way a pod delete does,
// and the infra re-place after either reap keeps the try number, so failing an
// attempt whose agent is still alive could run user code twice (#911). Both
// therefore reap only an attempt whose agent process is gone. The exception is
// an attempt still running past credentialCeiling: procs (the subprocess
// executor) can stop its task, so agent-lost fails it as credential_ceiling, a
// task failure that is never re-placed, and stops it, the Lite counterpart of
// a task pod's activeDeadlineSeconds (#1511).
//
// The reaper sits behind the same leader-settling gate as the pod path, measured
// from leadership: a Lite restart leaves detached agents alive with a stale
// heartbeat, and they get the grace to re-heartbeat before anything is judged.
// There is no informer and no reconciler, so those two conditions stay
// satisfied. markers, when non-nil, receives the agent-lost log marker (#861).
func newLiteReaper(store executor.ReaperStore, procs executor.ProcessLiveness, lead liteLeadership, markers logs.MarkerSink, rec executor.DecisionRecorder, logger *slog.Logger, credentialCeiling time.Duration) *executor.Reaper {
	reaper := executor.NewReaper(store, nil, nil, nil, rec, logger, executor.DefaultReaperConfig(), lead.SteppingDown)
	reaper.SetProcessLiveness(procs)
	// An attempt that outlived auth.max_attempt_credential_lifetime fails for
	// that reason instead of being re-placed as agent_lost (#1461), and one still
	// running past it is stopped (#1511).
	reaper.SetAttemptLifetimeCeiling(credentialCeiling)
	if markers != nil {
		reaper.SetLogSink(markers)
	}
	reaper.SetLeaderSince(lead.LeaderSince)
	reaper.SetLeading(lead.IsLeading)
	return reaper
}

// startLiteMaintenance runs Lite's maintenance loop: the reaper pass alone,
// leader-gated, at the pod path's cadence and under the same per-phase budget.
// There is no reconcile phase because Lite has no pods to sweep.
func startLiteMaintenance(ctx context.Context, reaper *executor.Reaper, leading func() bool, logger *slog.Logger) {
	gatedtick.Start(ctx, "lite-maintenance", reconcileInterval, leading, logger, func() {
		runMaintenancePhase(ctx, "execution reaper", maintenancePhaseTimeout, reaper.ReapOnce, logger)
	})
}
