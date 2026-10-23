package config

import (
	"fmt"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// ExecutorSection configures how tasks are executed.
type ExecutorSection struct {
	HTTP HTTPExecutorSection `mapstructure:"http"`
	// TaskNamespace is the Kubernetes namespace the server creates task pods and
	// per-run staging PVCs in. It MUST match the namespace the Helm chart grants
	// the executor Role in (chart `taskNamespace` → LEOFLOW_EXECUTOR_TASK_NAMESPACE);
	// a mismatch 403s every dispatch (#480). Defaults to "leoflow".
	TaskNamespace string `mapstructure:"task_namespace"`
	// Type selects the pod-path executor: "kubernetes" (default, pod-per-task) or
	// "subprocess" (dev only, runs the agent on the host without isolation, used
	// by `dexaflow lite`).
	Type string `mapstructure:"type"`
	// AgentPath is the leoflow-agent binary the subprocess executor runs (dev only).
	AgentPath string `mapstructure:"agent_path"`
	// SubprocessWorkDir is the working directory the subprocess executor runs the
	// agent in, so it can import the project's dag.py (dev only). Empty keeps the
	// server's working directory.
	SubprocessWorkDir string `mapstructure:"subprocess_workdir"`
	// AgentControlPlaneAddr is the gRPC address task pods dial back to. Empty
	// falls back to server.grpc_addr; in a local k3d/kind cluster set it to a
	// host-reachable address such as host.k3d.internal:9091.
	AgentControlPlaneAddr string `mapstructure:"agent_control_plane_addr"`
	// AgentTLSCAConfigMap names a ConfigMap (key ca.crt) mounted into task pods so
	// the agent verifies the control plane's gRPC TLS cert (issue #58). Empty =
	// agents use the insecure channel (dev).
	AgentTLSCAConfigMap string `mapstructure:"agent_tls_ca_configmap"`
	// TaskServiceAccount is the ServiceAccount task pods run as when a DAG's task
	// does not set execution.service_account. The chart wires its taskServiceAccount
	// here, so creating that SA makes keyless work without every DAG opting in.
	// Empty keeps pods on the namespace default SA (an explicit per-task value
	// always wins).
	TaskServiceAccount string `mapstructure:"task_service_account"`
	// TaskSecretName names a Kubernetes Secret mounted (read-only) into every task
	// pod at TaskSecretMountPath. It lets a task read a credential that lives in
	// the cluster's secret store (e.g. a GCP service-account key) referenced by a
	// connection's key_path, so Dexaflow never stores the key itself (ADR 0035).
	// Empty = no secret mounted.
	TaskSecretName string `mapstructure:"task_secret_name"`
	// TaskSecretMountPath is where TaskSecretName is mounted in the task pod.
	TaskSecretMountPath string `mapstructure:"task_secret_mount_path"`
	// Defaults holds per-cluster task defaults applied at dispatch to fill gaps the
	// DAG artifact left empty (ADR 0023, layer L0). They never override a value
	// baked into dag.json, keeping the artifact portable across clusters.
	Defaults PlatformDefaultsSection `mapstructure:"defaults"`
	// CollectSettledRunPods deletes a settled run's finished task pods as soon
	// as the reconciler has recorded every outcome, in one DeleteCollection by
	// the run's label instead of one delete per pod after the grace period. It
	// needs the deletecollection verb on pods (the chart grants it only when this
	// is on) and falls back to per-pod deletes without it. Off by default:
	// finished pods stay for the grace period, so they can be inspected with
	// kubectl.
	CollectSettledRunPods bool `mapstructure:"collect_settled_run_pods"`
	// KubeClient sets the client-side rate limits of the control plane's
	// Kubernetes clients.
	KubeClient KubeClientSection `mapstructure:"kube_client"`
	// Unit is the resource unit one pool slot stands for (ADR 0066). Unset (the
	// default) changes nothing. Set, every task pod is sized pool_slots x unit
	// where the task leaves cpu or memory out, and a task that declares more
	// than that is refused at registration and at dispatch.
	Unit ExecutorUnitSection `mapstructure:"unit"`
}

// ExecutorUnitSection is executor.unit: the CPU and memory of one pool slot,
// as Kubernetes quantities (both or neither), how a task that does not fit its
// size is treated (refuse or warn), and the largest pool_slots a task may have
// while the unit is set.
type ExecutorUnitSection struct {
	CPU     string `mapstructure:"cpu"`
	Memory  string `mapstructure:"memory"`
	Enforce string `mapstructure:"enforce"`
	MaxSize int    `mapstructure:"max_size"`
}

// ResourceUnitConfig is the section as the domain parser takes it.
func (u ExecutorUnitSection) ResourceUnitConfig() domain.ResourceUnitConfig {
	return domain.ResourceUnitConfig{CPU: u.CPU, Memory: u.Memory, Enforce: u.Enforce, MaxSize: u.MaxSize}
}

// KubeClientSection sets the client-side rate limits (client-go token buckets)
// of the control plane's Kubernetes clients. The dispatch client creates task
// pods; the agent token exchange builds its own client with the same limits.
// Maintenance work (pod informer, reconciler, reapers, staging GC, warm pool
// reconciler) shares the dispatch client unless MaintenanceQPS is set, in which
// case it gets a separate client and token bucket so a maintenance burst cannot
// starve pod creation.
type KubeClientSection struct {
	// QPS and Burst limit the dispatch client. Defaults are client-go's own
	// (5 and 10); a non-positive value falls back to them.
	QPS   float64 `mapstructure:"qps"`
	Burst int     `mapstructure:"burst"`
	// MaintenanceQPS and MaintenanceBurst limit a separate maintenance client.
	// 0 (default) keeps maintenance on the dispatch client, one shared budget as
	// before. A non-positive burst with a positive QPS falls back to client-go's
	// default burst.
	MaintenanceQPS   float64 `mapstructure:"maintenance_qps"`
	MaintenanceBurst int     `mapstructure:"maintenance_burst"`
}

// PlatformDefaultsSection configures the lowest-precedence (L0) task defaults,
// applied at dispatch to fill gaps the DAG left empty (ADR 0023).
type PlatformDefaultsSection struct {
	// StagingSize/StagingStorageClass default the per-run staging volume when the
	// DAG enabled staging without pinning them (e.g. the cluster's RWX class).
	StagingSize         string `mapstructure:"staging_size"`
	StagingStorageClass string `mapstructure:"staging_storage_class"`
	// StagingAccessMode is the PVC access mode for the staging volume. Defaults to
	// ReadWriteMany (multi-node prod); single-node dev (k3d local-path, no RWX)
	// sets ReadWriteOnce, which is sufficient for a run's sequential same-node pods.
	StagingAccessMode string `mapstructure:"staging_access_mode"`
	// ResourcesCPU/ResourcesMemory default a task's request when neither the task
	// override nor the DAG set any (Kubernetes quantities, e.g. "250m"/"256Mi").
	ResourcesCPU    string `mapstructure:"resources_cpu"`
	ResourcesMemory string `mapstructure:"resources_memory"`
	// LimitsCPU/LimitsMemory are the default task limits (#537): the CPU and
	// memory limit a task pod gets in a dimension its resources leave without
	// one, including a task that declares requests only. Empty (the default)
	// sets no limit, as before. Either may be set alone. Not applied with
	// executor.unit, which always sets both limits.
	LimitsCPU    string `mapstructure:"limits_cpu"`
	LimitsMemory string `mapstructure:"limits_memory"`
	// RunTasksAsNonRoot refuses to start a task container whose image resolves
	// to UID 0, completing Pod Security Admission's `restricted` set. On by
	// default now that the images this repo ships carry a numeric non-root UID:
	// runtime/Dockerfile runs as `USER 65532:65532` and every examples/*/image
	// inherits it, and the executor pairs it with a pod-level fsGroup so the
	// per-run staging PVC stays writable. Turn it off for a cluster whose task
	// images legitimately run as root.
	//
	// Deliberately a cluster setting rather than a DAG field: whether untrusted
	// task code may run as root belongs to whoever operates the cluster, not to
	// whoever authors the DAG.
	RunTasksAsNonRoot bool `mapstructure:"run_tasks_as_non_root"`
	// ReadOnlyTaskRootFilesystem mounts every task container's root filesystem
	// read-only. Off by default because `restricted` does not require it and it
	// breaks ordinary Python tasks (pip cache, /tmp, matplotlib config); turn it
	// on for a fleet of tasks known not to write outside their volumes.
	ReadOnlyTaskRootFilesystem bool `mapstructure:"read_only_task_root_filesystem"`
	// ExecutionAllowlist restricts which task execution (placement) fields a DAG
	// author may set, and optionally which priority classes (issue #629). The
	// default allows everything. Like the two settings above it is a cluster
	// setting: on a shared cluster these fields reach other teams' nodes and
	// preemption order.
	ExecutionAllowlist ExecutionAllowlistSection `mapstructure:"execution_allowlist"`
}

// ExecutionAllowlistSection is executor.defaults.execution_allowlist. A DAG
// that sets a field (or priority class) outside it is refused at registration
// and, for a DAG registered before the list changed, at dispatch.
type ExecutionAllowlistSection struct {
	// Fields lists the execution fields a DAG may set, by their dag.json names
	// (domain.ExecutionPolicyFields). "*" (the default) allows all of them,
	// "none" or an explicitly empty list allows none.
	Fields []string `mapstructure:"fields"`
	// PriorityClasses, when not empty, lists the only priority_class_name values
	// a DAG may set. Empty (the default) allows any.
	PriorityClasses []string `mapstructure:"priority_classes"`
	// ServiceAccounts, when not empty, lists the only execution.service_account
	// values a DAG may name (issue #537 C2). A task that names none runs as
	// executor.task_service_account, which is not checked. Empty allows any.
	ServiceAccounts []string `mapstructure:"service_accounts"`
	// Images, when not empty, lists the DAG images that may run: exact
	// references or prefixes ending in "*" (issue #537 C2). Empty allows any.
	Images []string `mapstructure:"images"`
}

// ExecutionPolicyConfig is the section as the domain parser takes it.
func (s ExecutionAllowlistSection) ExecutionPolicyConfig() domain.ExecutionPolicyConfig {
	return domain.ExecutionPolicyConfig{
		Fields: s.Fields, PriorityClasses: s.PriorityClasses,
		ServiceAccounts: s.ServiceAccounts, Images: s.Images,
	}
}

// ExecutionSection configures warm worker pools, Pro-gated N:1 pod reuse
// (ADR 0058). Every field is operator-set (never DAG-author-set), consistent
// with the secret-scoping stance: whether a pod may be reused across attempts is
// an operator's security decision, not a DAG author's. All fields default to a
// byte-for-byte no-op, warm pools OFF means dedicated pod-per-task, today's
// behavior, and are read for runtime behavior only in a later brick; N1a
// introduces the knobs plus the fail-closed boot guard (validateExecution).
type ExecutionSection struct {
	// WarmPoolsEnabled turns on N:1 pod reuse (ADR 0058). Default false = a
	// dedicated pod per task attempt, today's behavior byte-for-byte. Turning it on
	// is gated at boot on the security prerequisites (token-exchange transport +
	// liveness enforcement) because a warm pod reuses one credential across attempts.
	WarmPoolsEnabled bool `mapstructure:"warm_pools_enabled"`
	// SourceMode runs a version on the operator's runtime image from the dag.py
	// it was registered with, with no image build (ADR 0067 §3). Off by default.
	SourceMode SourceModeSection `mapstructure:"source_mode"`
	// MaxAttemptsPerWorker caps how many attempts a warm worker serves before it is
	// drained and recycled (ADR 0058 D9). Bounds credential-leak and stale-image
	// exposure by forcing a fresh pod periodically. Default 50.
	MaxAttemptsPerWorker int `mapstructure:"max_attempts_per_worker"`
	// MaxWorkerLifetime is the wall-clock cap on a warm worker before it is drained
	// and recycled (ADR 0058 D9), independent of the attempt count. Default 1h. When
	// warm pools are on it MUST be >= auth.max_attempt_credential_lifetime, so a
	// worker is never force-recycled mid-attempt by its token lapsing.
	MaxWorkerLifetime time.Duration `mapstructure:"max_worker_lifetime"`
	// MinIdleWorkers is the number of warm workers kept ready per DAG version
	// (ADR 0058 D6). Default 0 = scale-to-zero, preserving the ADR 0002 zero-idle
	// floor; an operator opts into warmth by raising it.
	MinIdleWorkers int `mapstructure:"min_idle_workers"`
	// WorkerIdleTTL is how long an idle warm worker is kept before it is recycled
	// (ADR 0058 D6). Default 5m.
	WorkerIdleTTL time.Duration `mapstructure:"worker_idle_ttl"`
	// MaxPoolSize caps the total warm workers a single DAG version may hold,
	// registered workers plus in-flight dedicated pods. Default 8, operator-set.
	// N1b1-place records the knob and validates it (>= 1 when warm pools are on)
	// but does NOT enforce the cap yet: defer-at-max needs real pool accounting
	// (registered workers + in-flight pods), which arrives with the worker
	// lifecycle in N1b2/N1d. Today's placer is assign-if-free-else-dedicated.
	MaxPoolSize int `mapstructure:"max_pool_size"`
	// MaxWarmPodsPerTenant caps the TOTAL warm pods a single tenant may hold across
	// ALL its dag_versions on a shared cluster (M4). Where MaxPoolSize bounds one
	// dag_version's pool, this bounds a tenant's aggregate warm footprint so one
	// tenant cannot pin unlimited idle pods and starve neighbors on a shared
	// multi-team cluster. Default 100, operator-set. It is a RESERVE-then-RATION
	// budget, never a starvation lever: the promised idle floors of a tenant's
	// versions with an active run (the sum of their EffectiveMinIdle) are honored
	// even when they exceed this cap (the reconciler raises the effective budget
	// to the floor sum and meters the misconfiguration). A prewarm-only version
	// (ADR 0082) never raises it: the prewarm floors share what the cap leaves,
	// by DAG id, and a prewarm pool above its share shrinks with the worker idle
	// TTL. The floors of versions with an active run are created at once on top
	// of such a pool, so the tenant exceeds the cap until it has shrunk (at
	// most one idle TTL after each share drop). The cap is otherwise enforced
	// only by refusing to CREATE new warm pods, never by deleting a busy worker.
	MaxWarmPodsPerTenant int `mapstructure:"max_warm_pods_per_tenant"`
	// WarmReadOnlyRootFilesystem mounts every warm worker's root filesystem read
	// only and gives each attempt its own HOME and XDG dirs inside the scratch the
	// worker wipes between attempts, plus a sweep of the shared /tmp emptyDir and
	// /dev/shm before each attempt and after it ends. It closes X3.2: on a
	// writable root a file one attempt plants on the image (a module on the
	// working directory's sys.path, a ~/.local site-packages entry) is executed
	// by the next attempt on the same worker. Default false keeps
	// today's writable root, since a task that writes outside $HOME, $TMPDIR, /tmp
	// and /dev/shm would fail with it on. It applies to warm pods created after it
	// is turned on. Dedicated task pods are not affected; they follow
	// executor.defaults.read_only_task_root_filesystem.
	WarmReadOnlyRootFilesystem bool `mapstructure:"warm_read_only_root_filesystem"`
	// WarmRequireAttemptHomeSweep refuses a warm worker that does not confirm,
	// when it registers, that it empties /tmp and /dev/shm between attempts
	// (#1678), so its DAG version's attempts run on dedicated pods. It applies
	// only with WarmReadOnlyRootFilesystem on. Default false logs and meters
	// such a worker and keeps it: agents from runtime v0.5.1 sweep but only the
	// ones built after the report was added can confirm it.
	WarmRequireAttemptHomeSweep bool `mapstructure:"warm_require_attempt_home_sweep"`
	// WarmPoolEventRefill makes warm-pool refill event driven: the reconciler
	// reads the fleet from a dedicated warm-pod informer instead of a LIST per
	// tick, reacts at once to a worker being deleted, failing or claimed, and
	// issues replacement creates concurrently (bounded). Default false keeps
	// today's 30s polling with a LIST and serial creates. The failover reapers
	// and the drained-anchor delete keep their live reads either way.
	WarmPoolEventRefill bool `mapstructure:"warm_pool_event_refill"`
}

// EffectiveMinIdle resolves the warm-worker target for one dag_version under
// model A2 (ADR 0058 N1b2b): the DAG author declares desired warmth per DAG
// (dagMinIdle), the operator caps and floors it.
//
//   - Warm pools OFF => always 0. This is what makes a default deploy a
//     byte-for-byte no-op: with warmth gated off no warm pod is ever targeted, so
//     the reconciler (when it runs at all) reconciles every pool to zero.
//   - The DAG author's value wins when set (> 0); when the DAG declares none (0)
//     it falls back to the operator's execution.min_idle_workers floor.
//   - The resolved value is clamped to [0, max_pool_size] so an author can never
//     provision more warmth than the operator's per-version cap allows, and a
//     nonsensical negative never underflows.
func (e ExecutionSection) EffectiveMinIdle(dagMinIdle int) int {
	if !e.WarmPoolsEnabled {
		return 0
	}
	target := dagMinIdle
	if target == 0 {
		// The author declared no warmth; inherit the operator's floor.
		target = e.MinIdleWorkers
	}
	if target < 0 {
		target = 0
	}
	if e.MaxPoolSize > 0 && target > e.MaxPoolSize {
		target = e.MaxPoolSize
	}
	return target
}

// HTTPExecutorSection configures HTTP-related executor knobs.
type HTTPExecutorSection struct {
	// UserAgent is the default User-Agent header for HTTP requests a task image
	// may make on the platform's behalf.
	UserAgent string `mapstructure:"user_agent"`
}

// SchedulerSection configures the scheduler loop.
type SchedulerSection struct {
	LoopIntervalMS int  `mapstructure:"loop_interval_ms"`
	Enabled        bool `mapstructure:"enabled"`
	// EagerPromotion cuts the latency between a task settling and its
	// downstreams being dispatched. Within a tick, the scheduler re-plans a run
	// after its own state changes (a newly scheduled task is queued at once, a
	// skip cascades), a bounded number of times; and a task's terminal report
	// triggers an early tick instead of waiting for the next interval, which
	// stays the upper bound. Off by default (ADR 0062): the tick then promotes
	// one step per interval, as it always has.
	EagerPromotion bool            `mapstructure:"eager_promotion"`
	Dispatch       DispatchSection `mapstructure:"dispatch"`
	Alerts         AlertsSection   `mapstructure:"alerts"`
	// PoolStarvationThreshold is how long a task held only by its pool waits
	// before the pool is reserved for it, so tasks of fewer slots cannot keep
	// a larger one out forever (ADR 0066 §4). 0 disables reservations. Pro only:
	// Lite has no pools.
	PoolStarvationThreshold time.Duration `mapstructure:"pool_starvation_threshold"`
}

// DispatchSection sizes the BufferedDispatcher (#127). BufferSize=0 keeps the
// scheduler tick synchronous with the inner dispatcher, the right shape for
// Lite (subprocess fork is microseconds). BufferSize>0 enables the worker
// pool, the right shape for Pro (Kubernetes API calls add real latency).
// The defaults are set per-edition by configsetup so the user does not have
// to think about this; an operator can still tune the knobs.
type DispatchSection struct {
	// BufferSize is the depth of the queued-dispatches channel. 0 disables the
	// pool (synchronous passthrough). A full channel returns ErrAtCapacity to
	// the scheduler, which leaves the TI scheduled for the next tick.
	BufferSize int `mapstructure:"buffer_size"`
	// Workers is the number of goroutines draining the queue. Ignored when
	// BufferSize <= 0; otherwise floored to 1.
	Workers int `mapstructure:"workers"`
	// TickBudget bounds the wall time one scheduler tick spends dispatching
	// (#1596). Once it is spent the tick stops dispatching, the remaining ready
	// tasks stay scheduled with no attempt charged, and the next tick starts at
	// the run after the one that was cut, so one wide fan-out under the
	// Kubernetes client rate limit cannot hold every other run, and cron run
	// creation, for minutes. 0 disables it; negative fails boot.
	TickBudget time.Duration `mapstructure:"tick_budget"`
	// MaxPerTenantPerTick caps the tasks one tenant may dispatch in one tick,
	// so a tenant with a wide fan-out or many runs leaves dispatch room for the
	// others. 0 (default) means no cap; negative fails boot.
	MaxPerTenantPerTick int `mapstructure:"max_per_tenant_per_tick"`
}

// RetentionSection configures the leader-only retention janitor, which deletes
// metadata rows past a per-class age. Every class is off by default (0 days):
// nothing is ever deleted unless the operator configures a window. The pacing
// knobs keep each statement small and each cycle bounded.
type RetentionSection struct {
	// DagRunsDays deletes finished (success or failed) dag runs, with their task
	// instances, attempt history and XCom index rows, once they ended more
	// than this many days ago. 0 (default) keeps every run.
	DagRunsDays int `mapstructure:"dag_runs_days"`
	// AuditLogDays deletes audit log entries older than this many days. 0
	// (default) keeps the whole audit log.
	AuditLogDays int `mapstructure:"audit_log_days"`
	// DryRun only counts the rows a cycle would delete, logs the counts and
	// exports them as gauges, and deletes nothing.
	DryRun bool `mapstructure:"dry_run"`
	// Interval is how often a cycle runs. Default 1h.
	Interval time.Duration `mapstructure:"interval"`
	// BatchSize caps the rows one DELETE statement removes, except the task
	// instances of one run larger than it, which go together. Default 1000.
	BatchSize int `mapstructure:"batch_size"`
	// BatchPause is the sleep between two batches, so the janitor never holds
	// the database busy. Default 100ms.
	BatchPause time.Duration `mapstructure:"batch_pause"`
	// MaxRowsPerCycle stops a cycle from starting another batch once this many
	// rows were deleted; the rest waits for the next cycle. Default 100000.
	MaxRowsPerCycle int `mapstructure:"max_rows_per_cycle"`
}

// maxRetentionBatchSize bounds retention.batch_size: a batch is meant to be
// small, and a value in the hundreds of thousands is the unbounded DELETE the
// janitor exists to avoid.
const maxRetentionBatchSize = 50000

// Enabled reports whether any retention class is configured.
func (r RetentionSection) Enabled() bool {
	return r.DagRunsDays > 0 || r.AuditLogDays > 0
}

// Validate rejects a negative window always, and pacing that would make the
// janitor unbounded or a busy loop when a class is on.
func (r RetentionSection) Validate() error {
	if r.DagRunsDays < 0 {
		return fmt.Errorf("retention.dag_runs_days must be >= 0 (got %d); 0 keeps every run", r.DagRunsDays)
	}
	if r.AuditLogDays < 0 {
		return fmt.Errorf("retention.audit_log_days must be >= 0 (got %d); 0 keeps the whole audit log", r.AuditLogDays)
	}
	if !r.Enabled() {
		return nil
	}
	if r.BatchSize < 1 || r.BatchSize > maxRetentionBatchSize {
		return fmt.Errorf("retention.batch_size must be between 1 and %d (got %d)", maxRetentionBatchSize, r.BatchSize)
	}
	if r.BatchPause < 0 {
		return fmt.Errorf("retention.batch_pause must be >= 0 (got %v)", r.BatchPause)
	}
	if r.Interval <= 0 {
		return fmt.Errorf("retention.interval must be > 0 (got %v)", r.Interval)
	}
	if r.MaxRowsPerCycle < 1 {
		return fmt.Errorf("retention.max_rows_per_cycle must be >= 1 (got %d)", r.MaxRowsPerCycle)
	}
	return nil
}

// validateExecution enforces the warm-pool boot gate (ADR 0058 N1a), fail-closed.
// It first checks execution.source_mode, which does not depend on warm pools.
// The whole block is gated on WarmPoolsEnabled: with warm pools OFF (the default)
// none of these fields is validated, so an operator who never turns warm pools on
// is unaffected. With warm pools ON it rejects, rather than silently correcting:
//
//	(a) HIGH #1, the security coupling. A warm pod reuses one credential across
//	    attempts; without token-exchange transport and liveness enforcement a
//	    superseded attempt's token would still resolve secrets. So warm pools
//	    require auth.agent_token_transport=exchange AND auth.secret_liveness_mode=
//	    enforce (ADR 0058 D2/HIGH-1). This half stays fail-closed: there is no safe
//	    degraded mode, the degraded mode IS the vulnerability.
//	(b) sanity, a zero/negative attempts cap, worker lifetime, or idle TTL would
//	    recycle instantly or never; each must be positive.
//
// There is deliberately NO max_worker_lifetime >= max_attempt_credential_lifetime
// ordering guard. That invariant was dropped (ADR 0058 D10 review): recycle is a
// graceful DRAIN, not a hard mid-attempt kill, so a worker lifetime shorter than
// the credential ceiling is harmless, the worker finishes its in-flight attempt
// (on its own renewed credential) before recycling. The guard defended a
// force-recycle that drain forbids, and with the shipped defaults (1h worker vs
// 24h ceiling) it also blocked a naive enable and pushed operators toward less-safe
// config. The real "credential lapses mid-attempt" bound is per-ATTEMPT
// (execution_timeout / the warm-worker watchdog <= the ceiling), enforced on the
// execution path, not here.
func (c *ServerConfig) validateExecution() error {
	if err := c.validateSourceMode(); err != nil {
		return err
	}
	if !c.Execution.WarmPoolsEnabled {
		return nil
	}
	if c.Auth.AgentTokenTransport != AgentTokenTransportExchange || c.Auth.SecretLivenessMode != SecretLivenessEnforce {
		return fmt.Errorf("execution.warm_pools_enabled requires auth.agent_token_transport=%q and auth.secret_liveness_mode=%q (a warm pod reuses one credential across attempts; without token-exchange + liveness enforcement a superseded attempt's token would still resolve secrets, ADR 0058 D2/HIGH-1)",
			AgentTokenTransportExchange, SecretLivenessEnforce)
	}
	if c.Execution.MaxAttemptsPerWorker < 1 {
		return fmt.Errorf("execution.max_attempts_per_worker must be >= 1 when execution.warm_pools_enabled (got %d): a zero cap would recycle a warm worker instantly (ADR 0058 D9)", c.Execution.MaxAttemptsPerWorker)
	}
	if c.Execution.MaxWorkerLifetime <= 0 {
		return fmt.Errorf("execution.max_worker_lifetime must be > 0 when execution.warm_pools_enabled (got %v): a non-positive lifetime would recycle a warm worker instantly (ADR 0058 D9)", c.Execution.MaxWorkerLifetime)
	}
	if c.Execution.WorkerIdleTTL <= 0 {
		return fmt.Errorf("execution.worker_idle_ttl must be > 0 when execution.warm_pools_enabled (got %v): a non-positive TTL would recycle an idle warm worker instantly (ADR 0058 D6)", c.Execution.WorkerIdleTTL)
	}
	if c.Execution.MaxPoolSize < 1 {
		return fmt.Errorf("execution.max_pool_size must be >= 1 when execution.warm_pools_enabled (got %d): a zero cap would forbid every warm worker (ADR 0058)", c.Execution.MaxPoolSize)
	}
	if c.Execution.MaxWarmPodsPerTenant < 1 {
		return fmt.Errorf("execution.max_warm_pods_per_tenant must be >= 1 when execution.warm_pools_enabled (got %d): a zero aggregate tenant cap would forbid every warm worker (M4)", c.Execution.MaxWarmPodsPerTenant)
	}
	return nil
}

// validateExecutorUnit checks executor.unit (ADR 0066): unset, or both
// quantities valid and positive, enforce refuse or warn, max_size positive.
// It combines with warm pools: a warm pod is then one unit and serves only
// size-1 tasks that declare no resources.
//
// It also checks the default task limits (executor.defaults.limits_*, #537),
// the other operator-set bound on task resources: unset, or valid and positive.
func (c *ServerConfig) validateExecutorUnit() error {
	if _, err := domain.ParseResourceUnit(c.Executor.Unit.ResourceUnitConfig()); err != nil {
		return err
	}
	_, err := domain.ParseDefaultLimits(c.Executor.Defaults.LimitsCPU, c.Executor.Defaults.LimitsMemory)
	return err
}

// validateExecutorPolicies checks the operator's task policies under executor:
// the resource unit and the execution allowlist.
func (c *ServerConfig) validateExecutorPolicies() error {
	if err := c.validateExecutorUnit(); err != nil {
		return err
	}
	return c.validateExecutionAllowlist()
}

// validateExecutionAllowlist checks executor.defaults.execution_allowlist
// (issue #629), so a misspelled field name fails boot instead of allowing or
// refusing the wrong thing.
func (c *ServerConfig) validateExecutionAllowlist() error {
	_, err := domain.ParseExecutionPolicy(c.Executor.Defaults.ExecutionAllowlist.ExecutionPolicyConfig())
	return err
}
