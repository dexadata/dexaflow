package observability

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// Metrics holds every Prometheus collector required by ADR 0010. Construct it
// once with NewMetrics and pass it to the components that emit values.
type Metrics struct {
	// Scheduler
	SchedulerLoopDuration prometheus.Histogram
	ScheduleDelay         prometheus.Histogram
	SchedulerWokenTicks   prometheus.Counter
	SchedulerDecisions    *prometheus.CounterVec
	TasksUndispatchable   *prometheus.CounterVec
	SchedulerStepDowns    *prometheus.CounterVec // #311 leader churn observability
	SchedulerReacquire    prometheus.Histogram   // #311 step-down → re-acquire latency
	AlertsDispatched      *prometheus.CounterVec // #424 on-failure alerts, by outcome

	// Task lifecycle
	TaskStateTransitions    *prometheus.CounterVec
	TaskDuration            *prometheus.HistogramVec
	TaskRetries             *prometheus.CounterVec
	TaskPodCreationDuration prometheus.Histogram
	TaskColdStart           *prometheus.HistogramVec

	// XCom
	XComSize     *prometheus.HistogramVec
	XComPush     *prometheus.CounterVec
	XComPull     *prometheus.CounterVec
	XComRejected *prometheus.CounterVec

	// API
	HTTPRequests        *prometheus.CounterVec
	HTTPRequestDuration *prometheus.HistogramVec
	AuthFailures        *prometheus.CounterVec

	// Agent credentials: task tokens authenticated without an attempt_epoch
	// claim, accepted under the epoch-0 rule (ADR 0051 amendment).
	AgentLegacyAttemptTokens prometheus.Counter
	// AgentTokenRejections counts agent RPCs refused for their bearer token, by
	// reason (#1125).
	AgentTokenRejections *prometheus.CounterVec
	// Reconciler: durable SUCCESS records settled over a reaper's provisional
	// infra mark, by the mark overridden (ADR 0052 amendment).
	ReconcileInfraOverrides *prometheus.CounterVec

	// Executor (Kubernetes)
	PodsCreated        *prometheus.CounterVec
	PodPendingDuration prometheus.Histogram
	KubernetesAPICalls *prometheus.CounterVec

	// Dispatch pool (#127, ADR 0031): depth gauge, capacity-saturation counter,
	// per-dispatch latency, inner-dispatcher error counter.
	DispatchQueueDepth  prometheus.Gauge
	DispatchAtCapacity  prometheus.Counter
	DispatchLatency     prometheus.Histogram
	DispatchInnerErrors prometheus.Counter
	// UnitMisfits counts tasks let through under executor.unit.enforce=warn
	// although they do not fit their size, by stage (register, dispatch;
	// ADR 0066 §3).
	UnitMisfits *prometheus.CounterVec
	// Admissions counts the dispatch admission hooks' outcomes (ADR 0100);
	// AdmissionDuration is how long the hook chain took for one attempt.
	Admissions        *prometheus.CounterVec
	AdmissionDuration prometheus.Histogram
	// UnpinnedImages counts images not pinned by digest that
	// deploy.require_digest warned about or refused, by stage (register,
	// set_current) and outcome (warned, refused) (#1703).
	UnpinnedImages *prometheus.CounterVec
	// CIOIDCExchanges counts CI OIDC token exchanges by issuer entry
	// ("unknown" when the token named none) and outcome (ADR 0092).
	CIOIDCExchanges *prometheus.CounterVec

	// Redis observability — port of the #311 step-down pattern for Redis (Pro
	// only; Lite uses Postgres + in-process tailer per ADR 0026, so these
	// gauges stay at 0). Operators alert on the per-reason rate, not on log
	// content: a sudden rate spike on RedisCommandFailures{reason="timeout"}
	// or RedisDialFailures{reason="tls_handshake"} catches an outage before
	// the surrounding components (XCom, log tailer) start surfacing tail
	// errors to users.
	RedisCommandFailures *prometheus.CounterVec
	RedisDialFailures    *prometheus.CounterVec
	RedisDialDuration    prometheus.Histogram
	RedisPoolActive      prometheus.Gauge
	RedisPoolIdle        prometheus.Gauge
	RedisPoolTotalConns  prometheus.Gauge
	RedisPoolTimeouts    prometheus.Counter

	// Retention janitor (D5): rows deleted per table, rows a dry run found
	// eligible per table, and the duration of each cycle.
	RetentionRowsDeleted   *prometheus.CounterVec
	RetentionRowsEligible  *prometheus.GaugeVec
	RetentionCycleDuration prometheus.Histogram

	// TLSReloadFailures counts certificate or key files that changed on disk
	// but failed to load, by listener (api, agent_grpc). The listener keeps
	// serving the last good pair, so a non-zero rate means a rotation is stuck
	// and the served certificate will expire unless the files are fixed (#637).
	TLSReloadFailures *prometheus.CounterVec

	// LogTailReplayBytes is what the on_demand live-tail gates hold for replay
	// together, and LogTailReplayEvictions the rings logs.tail.replay_budget_bytes
	// dropped to stay under it (#1621).
	LogTailReplayBytes     prometheus.Gauge
	LogTailReplayEvictions prometheus.Counter

	// ResilienceBackstopDisabled is 1 for each resilience backstop a boot
	// setting removed and 0 for each one in place, set once at boot (#941).
	ResilienceBackstopDisabled *prometheus.GaugeVec

	// schema backs dexaflow_schema_ready and dexaflow_schema_wait_seconds
	// (RecordSchemaState, ADR 0090 decision 6).
	schema *schemaState
}

// NewMetrics registers every ADR 0010 collector with reg and returns the set.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	sizeBuckets := prometheus.ExponentialBuckets(64, 4, 8)
	return &Metrics{
		schema: newSchemaState(f),
		SchedulerLoopDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "dexaflow_scheduler_loop_duration_seconds", Help: "Duration of one scheduler loop iteration.",
		}),
		ScheduleDelay: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "dexaflow_scheduler_schedule_delay_seconds",
			Help:    "Time from a scheduled run's slot to its creation, for the latest slot a tick created for a DAG.",
			Buckets: []float64{0.5, 1, 2, 5, 10, 30, 60, 300, 900, 3600},
		}),
		SchedulerDecisions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_scheduler_decisions_total", Help: "Scheduler decisions by type.",
		}, []string{"decision_type"}),
		SchedulerWokenTicks: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_scheduler_woken_ticks_total",
			Help: "Scheduler ticks started early by a task completion instead of the loop interval (scheduler.eager_promotion).",
		}),
		TasksUndispatchable: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_tasks_undispatchable_total", Help: "Tasks queued with no executor to launch them, by reason.",
		}, []string{"reason"}),
		SchedulerStepDowns: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_scheduler_step_downs_total",
			Help: "Scheduler leadership step-downs by reason (lock_released, check_timeout, shutdown). " +
				"Operators alert on rate(...[5m]); a sudden uptick usually indicates a Postgres connection-stability issue (#311).",
		}, []string{"reason"}),
		SchedulerReacquire: f.NewHistogram(prometheus.HistogramOpts{
			Name: "dexaflow_scheduler_reacquire_seconds",
			Help: "Wall-clock seconds between a leader step-down and the same replica reacquiring leadership. " +
				"A growing P99 signals leader churn that affects scheduling latency (#311).",
			// Buckets span ~10ms (transient blip) to ~5min (extended outage); the
			// issue body reports observed re-acquire ~10ms, so the low end is the
			// reportable distribution.
			Buckets: []float64{0.01, 0.05, 0.1, 0.5, 1, 5, 10, 30, 60, 300},
		}),
		AlertsDispatched: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_alerts_dispatched_total",
			Help: "Native on-failure alerts dispatched, by channel type and outcome (sent, failed). " +
				"Operators alert on rate of result=\"failed\" to catch a broken alert path (#424).",
		}, []string{"dag_id", "type", "result"}),

		TaskStateTransitions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_task_state_transitions_total", Help: "Task state transitions.",
		}, []string{"from_state", "to_state", "dag_id"}),
		TaskDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dexaflow_task_duration_seconds", Help: "Task execution duration.",
		}, []string{"dag_id", "task_id", "task_type"}),
		TaskRetries: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_task_retries_total", Help: "Task retries.",
		}, []string{"dag_id", "task_id"}),
		TaskPodCreationDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "dexaflow_task_pod_creation_duration_seconds", Help: "Time to create a task pod.",
		}),
		TaskColdStart: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dexaflow_task_cold_start_seconds", Help: "Task cold start time.",
		}, []string{"dag_id"}),

		XComSize: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dexaflow_xcom_size_bytes", Help: "XCom payload size in bytes.", Buckets: sizeBuckets,
		}, []string{"dag_id"}),
		XComPush: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_xcom_push_total", Help: "XCom pushes.",
		}, []string{"dag_id"}),
		XComPull: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_xcom_pull_total", Help: "XCom pulls.",
		}, []string{"dag_id"}),
		XComRejected: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_xcom_rejected_total", Help: "Rejected XCom writes by reason.",
		}, []string{"reason"}),

		HTTPRequests: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_http_requests_total", Help: "HTTP requests.",
		}, []string{"method", "path", "status"}),
		HTTPRequestDuration: f.NewHistogramVec(prometheus.HistogramOpts{
			Name: "dexaflow_http_request_duration_seconds", Help: "HTTP request duration.",
		}, []string{"method", "path"}),
		AuthFailures: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_auth_failures_total", Help: "Authentication failures by reason.",
		}, []string{"reason"}),

		AgentLegacyAttemptTokens: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_agent_legacy_attempt_token_total",
			Help: "Agent RPCs authenticated by a task token without the attempt_epoch claim: one minted before the upgrade, " +
				"or one an old replica renewed or exchanged during a rolling upgrade. Reports from such a token are read as epoch 0. " +
				"It falls to zero once the rollout has finished and every pre-upgrade attempt has finished; a later release rejects such tokens.",
		}),

		AgentTokenRejections: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_agent_token_rejections_total",
			Help: "Agent RPCs refused for their bearer token, by reason: expired (the credential lapsed: the agent could not renew it for a whole TTL, " +
				"or its pod started after it ran out), wrong_audience (a token for another surface), bad_signature (another signing key), " +
				"wrong_issuer, not_yet_valid, malformed, invalid, or missing (no bearer at all).",
		}, []string{"reason"}),

		ReconcileInfraOverrides: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_reconcile_infra_override_total",
			Help: "Durable SUCCESS records the reconciler settled over a reaper's provisional infra mark (agent_lost, pod_lost, dispatch_lost), " +
				"by the mark overridden: a task that finished while the control plane lost track of it, recovered instead of re-run.",
		}, []string{"mark"}),

		PodsCreated: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_pods_created_total", Help: "Pods created by dag and result.",
		}, []string{"dag_id", "result"}),
		PodPendingDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "dexaflow_pod_pending_duration_seconds", Help: "Pod pending duration.",
		}),
		KubernetesAPICalls: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_kubernetes_api_calls_total", Help: "Kubernetes API calls.",
		}, []string{"operation", "result"}),

		DispatchQueueDepth: f.NewGauge(prometheus.GaugeOpts{
			Name: "dexaflow_dispatch_queue_depth", Help: "Number of dispatch requests currently buffered.",
		}),
		DispatchAtCapacity: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_dispatch_at_capacity_total", Help: "Dispatch requests rejected because the buffer was full.",
		}),
		DispatchLatency: f.NewHistogram(prometheus.HistogramOpts{
			Name: "dexaflow_dispatch_latency_seconds", Help: "End-to-end latency of one buffered dispatch (enqueue to worker completion).",
		}),
		DispatchInnerErrors: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_dispatch_inner_errors_total", Help: "Errors returned by the inner dispatcher inside a worker.",
		}),
		UnitMisfits: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_unit_misfit_total",
			Help: "Tasks accepted under executor.unit.enforce=warn although their resources exceed pool_slots x unit, by stage (register, set_current, dispatch).",
		}, []string{"stage"}),
		Admissions: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_dispatch_admission_total",
			Help: "Task attempts seen by the dispatch admission hooks (ADR 0100), by outcome (admitted, changed, held, refused, error, fail_open, breaker_open).",
		}, []string{"outcome"}),
		AdmissionDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "dexaflow_dispatch_admission_duration_seconds",
			Help:    "Time the dispatch admission hooks took for one task attempt.",
			Buckets: []float64{0.0005, 0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1},
		}),
		UnpinnedImages: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_unpinned_image_total",
			Help: "Images not pinned by digest that deploy.require_digest warned about (outcome=warned) or refused (outcome=refused), by stage (register, set_current). Dry runs are not counted.",
		}, []string{"stage", "outcome"}),
		CIOIDCExchanges: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_ci_oidc_exchange_total",
			Help: "CI OIDC token exchanges (auth.ci_oidc, ADR 0092) by issuer entry (unknown when none matched) and outcome " +
				"(issued, invalid_token, unknown_issuer, no_rule, role_missing, keys_unavailable, error).",
		}, []string{"issuer", "outcome"}),

		RedisCommandFailures: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_redis_command_failures_total",
			Help: "Redis command failures classified by reason (timeout, connection_refused, auth, canceled, other). " +
				"Alert on rate(...[5m]) to surface a degrading client-side path before user-visible XCom errors (#312 sibling).",
		}, []string{"reason"}),
		RedisDialFailures: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_redis_dial_failures_total",
			Help: "Failures at TCP/TLS dial time to Redis, classified by reason (tls_handshake, connection_refused, dns, timeout, other).",
		}, []string{"reason"}),
		RedisDialDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "dexaflow_redis_dial_duration_seconds",
			Help:    "Wall-clock time per Redis dial: TCP connect plus, with rediss://, the TLS handshake.",
			Buckets: []float64{0.001, 0.005, 0.01, 0.05, 0.1, 0.5, 1, 5},
		}),
		RedisPoolActive: f.NewGauge(prometheus.GaugeOpts{
			Name: "dexaflow_redis_pool_active_conns", Help: "Redis pool: connections currently checked out for a command.",
		}),
		RedisPoolIdle: f.NewGauge(prometheus.GaugeOpts{
			Name: "dexaflow_redis_pool_idle_conns", Help: "Redis pool: idle connections available for the next command.",
		}),
		RedisPoolTotalConns: f.NewGauge(prometheus.GaugeOpts{
			Name: "dexaflow_redis_pool_total_conns", Help: "Redis pool: total connections (active + idle). Saturating against PoolSize is the leading indicator of throughput issues.",
		}),
		TLSReloadFailures: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_tls_reload_failures_total",
			Help: "Rotated TLS certificate or key files that failed to load, by listener (api, agent_grpc). " +
				"The listener keeps serving the last good certificate; alert on any increase (#637).",
		}, []string{"listener"}),
		RedisPoolTimeouts: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_redis_pool_timeouts_total", Help: "Redis pool checkout timeouts: a caller waited PoolTimeout for an idle connection. A non-zero rate means the pool is too small or commands are too slow.",
		}),

		LogTailReplayBytes: f.NewGauge(prometheus.GaugeOpts{
			Name: "dexaflow_log_tail_replay_bytes",
			Help: "Bytes of task-log lines the on_demand live-tail gates hold for a follower who may subscribe, across all log streams. " +
				"Bounded by logs.tail.replay_budget_bytes when set.",
		}),
		LogTailReplayEvictions: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_log_tail_replay_evictions_total",
			Help: "Live-tail replay rings dropped to keep the total under logs.tail.replay_budget_bytes, the longest silent stream first. " +
				"A follower of a dropped stream reads the dropped lines from the stored log.",
		}),

		ResilienceBackstopDisabled: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dexaflow_resilience_backstop_disabled",
			Help: "1 when this process booted with the resilience backstop of config_key disabled, 0 when it is in place. " +
				"Set once at boot by processes that run the scheduler; alert on max(...) > 0.",
		}, []string{"config_key"}),

		RetentionRowsDeleted: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_retention_rows_deleted_total", Help: "Metadata rows the retention janitor deleted, by table.",
		}, []string{"table"}),
		RetentionRowsEligible: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dexaflow_retention_rows_eligible", Help: "Rows the last retention dry run found eligible for deletion, by table, summed across all tenants.",
		}, []string{"table"}),
		RetentionCycleDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name:    "dexaflow_retention_cycle_duration_seconds",
			Help:    "Wall-clock duration of one retention janitor cycle, pauses included.",
			Buckets: []float64{0.1, 1, 10, 30, 60, 300, 900, 1800},
		}),
	}
}

// RecordRetentionDeleted counts n rows the retention janitor deleted from table.
func (m *Metrics) RecordRetentionDeleted(table string, n int64) {
	m.RetentionRowsDeleted.WithLabelValues(table).Add(float64(n))
}

// RecordRetentionEligible sets the rows a retention dry run found eligible in table.
func (m *Metrics) RecordRetentionEligible(table string, n int64) {
	m.RetentionRowsEligible.WithLabelValues(table).Set(float64(n))
}

// ObserveRetentionCycle observes the duration of one retention cycle.
func (m *Metrics) ObserveRetentionCycle(d time.Duration) {
	m.RetentionCycleDuration.Observe(d.Seconds())
}

// RecordRedisCommandFailure increments the per-reason command failure counter
// (#312 sibling, paralleling #311's step-down reason). Caller is the go-redis
// hook's ProcessHook — it classifies the error before incrementing so the
// label cardinality stays bounded.
func (m *Metrics) RecordRedisCommandFailure(reason string) {
	m.RedisCommandFailures.WithLabelValues(reason).Inc()
}

// RecordRedisDialFailure increments the per-reason dial failure counter.
// Separated from command failures because a dial failure means we never even
// reached the Redis instance — the cause is upstream (DNS, TLS, network),
// not Redis itself.
func (m *Metrics) RecordRedisDialFailure(reason string) {
	m.RedisDialFailures.WithLabelValues(reason).Inc()
}

// ObserveRedisDialDuration records one TCP/TLS dial latency sample.
func (m *Metrics) ObserveRedisDialDuration(d time.Duration) {
	m.RedisDialDuration.Observe(d.Seconds())
}

// UpdateRedisPoolStats refreshes the three pool gauges. The cmd/dexaflow-server
// goroutine that calls this scrapes go-redis's PoolStats every N seconds.
func (m *Metrics) UpdateRedisPoolStats(active, idle, total uint32) {
	m.RedisPoolActive.Set(float64(active))
	m.RedisPoolIdle.Set(float64(idle))
	m.RedisPoolTotalConns.Set(float64(total))
}

// RecordRedisPoolTimeout increments the pool-checkout timeout counter. The
// go-redis hook fires this when a caller waited PoolTimeout for an idle
// connection — the leading indicator that the pool is too small for the
// command rate.
func (m *Metrics) RecordRedisPoolTimeout() { m.RedisPoolTimeouts.Inc() }

// SetLogTailReplayBytes reports what the live-tail gates hold for replay.
// No-op on a nil receiver.
func (m *Metrics) SetLogTailReplayBytes(n int64) {
	if m == nil {
		return
	}
	m.LogTailReplayBytes.Set(float64(n))
}

// RecordLogTailReplayEviction counts a replay ring the budget dropped. No-op
// on a nil receiver.
func (m *Metrics) RecordLogTailReplayEviction() {
	if m == nil {
		return
	}
	m.LogTailReplayEvictions.Inc()
}

// RecordResilienceBackstop sets the boot posture of the backstop of key:
// disabled or in place. No-op on a nil receiver.
func (m *Metrics) RecordResilienceBackstop(key string, disabled bool) {
	if m == nil {
		return
	}
	v := 0.0
	if disabled {
		v = 1
	}
	m.ResilienceBackstopDisabled.WithLabelValues(key).Set(v)
}

// RecordTLSReloadFailure counts one rotated certificate pair that failed to
// load on the given listener. It is a no-op on a nil receiver so a caller
// without metrics wired needs no guard.
func (m *Metrics) RecordTLSReloadFailure(listener string) {
	if m == nil {
		return
	}
	m.TLSReloadFailures.WithLabelValues(listener).Inc()
}

// RecordHTTPRequest records a completed HTTP request (count + duration).
func (m *Metrics) RecordHTTPRequest(method, path string, status int, dur time.Duration) {
	m.HTTPRequests.WithLabelValues(method, path, strconv.Itoa(status)).Inc()
	m.HTTPRequestDuration.WithLabelValues(method, path).Observe(dur.Seconds())
}

// RecordSchedulerWokenTick counts one scheduler tick started early by a task
// completion (scheduler.eager_promotion).
func (m *Metrics) RecordSchedulerWokenTick() { m.SchedulerWokenTicks.Inc() }

// RecordSchedulerDecision records one scheduler decision by type.
func (m *Metrics) RecordSchedulerDecision(decisionType string) {
	m.SchedulerDecisions.WithLabelValues(decisionType).Inc()
}

// RecordAlert counts one on-failure alert dispatch (#424) by dag, channel type,
// and outcome ("sent" or "failed"). Satisfies failurealert.Recorder.
func (m *Metrics) RecordAlert(dagID, channelType, result string) {
	m.AlertsDispatched.WithLabelValues(dagID, channelType, result).Inc()
}

// RecordSchedulerStepDown increments the leader step-down counter (#311). The
// reason label lets operators distinguish a transient lock_released (pgx blip)
// from a check_timeout (the lock-check failed repeatedly) or a clean shutdown,
// so they can alert on the rate of the noisy reasons without paging on
// expected shutdowns.
func (m *Metrics) RecordSchedulerStepDown(reason string) {
	m.SchedulerStepDowns.WithLabelValues(reason).Inc()
}

// ObserveSchedulerReacquire records the wall-clock duration of a step-down →
// re-acquire cycle (#311). Use the histogram's P99 in alerts: a P99 growing
// past a second indicates churn that is starting to delay scheduling.
func (m *Metrics) ObserveSchedulerReacquire(d time.Duration) {
	m.SchedulerReacquire.Observe(d.Seconds())
}

// ObserveSchedulerLoop records the wall time of one leader scheduler tick, the
// SLI dexaflow_scheduler_loop_duration_seconds exists for (#1186). It satisfies
// scheduler.Recorder.
func (m *Metrics) ObserveSchedulerLoop(d time.Duration) {
	m.SchedulerLoopDuration.Observe(d.Seconds())
}

// ObserveScheduleDelay records how late a scheduled run was created, from its
// slot to its creation (#2175). It satisfies scheduler.Recorder.
func (m *Metrics) ObserveScheduleDelay(d time.Duration) {
	m.ScheduleDelay.Observe(d.Seconds())
}

// RecordTaskRetry counts one retry the scheduler released for a task (its
// up_for_retry reset to none with a new try number). It satisfies
// scheduler.Recorder.
func (m *Metrics) RecordTaskRetry(dagID, taskID string) {
	m.TaskRetries.WithLabelValues(dagID, taskID).Inc()
}

// ObservePodCreation counts one task pod create by dag and result ("success"
// or "error", ADR 0010) and observes the API call's latency. It satisfies
// executor.PodCreationRecorder.
func (m *Metrics) ObservePodCreation(dagID, result string, d time.Duration) {
	m.PodsCreated.WithLabelValues(dagID, result).Inc()
	m.TaskPodCreationDuration.Observe(d.Seconds())
}

// RecordTaskTransition records a task instance state transition.
func (m *Metrics) RecordTaskTransition(from, to, dagID string) {
	m.TaskStateTransitions.WithLabelValues(from, to, dagID).Inc()
}

// RecordUndispatchable records a task that became queued but has no executor to
// launch it (e.g. pod dispatch disabled), so an operator can distinguish a
// resource/config gap from an actual bug.
func (m *Metrics) RecordUndispatchable(reason string) {
	m.TasksUndispatchable.WithLabelValues(reason).Inc()
}

// RecordTaskDuration records how long a task took to execute, in seconds.
func (m *Metrics) RecordTaskDuration(dagID, taskID, taskType string, seconds float64) {
	m.TaskDuration.WithLabelValues(dagID, taskID, taskType).Observe(seconds)
}

// RecordDispatchQueueDepth sets the current depth of the buffered dispatch
// queue (#127). Sampled on every successful enqueue.
func (m *Metrics) RecordDispatchQueueDepth(depth int) {
	m.DispatchQueueDepth.Set(float64(depth))
}

// RecordDispatchAtCapacity counts one rejection because the buffered dispatch
// queue was full. Each rejection means the scheduler will retry on the next
// tick — a rising rate signals the pool needs more workers or a deeper queue.
func (m *Metrics) RecordDispatchAtCapacity() { m.DispatchAtCapacity.Inc() }

// RecordDispatchLatencySeconds observes one end-to-end dispatch latency, from
// enqueue to the worker's inner-dispatcher return, for every buffered dispatch
// (successful or not).
func (m *Metrics) RecordDispatchLatencySeconds(seconds float64) {
	m.DispatchLatency.Observe(seconds)
}

// RecordLegacyAttemptToken counts one agent RPC authenticated by a task token
// without an attempt_epoch claim (ADR 0051 amendment). It satisfies
// agentrpc.LegacyTokenRecorder.
func (m *Metrics) RecordLegacyAttemptToken() { m.AgentLegacyAttemptTokens.Inc() }

// RecordAgentTokenRejection counts one agent RPC refused for its bearer token.
// It satisfies agentrpc.TokenRejectionRecorder.
func (m *Metrics) RecordAgentTokenRejection(reason string) {
	m.AgentTokenRejections.WithLabelValues(reason).Inc()
}

// RecordInfraOverride counts one durable SUCCESS settled over an infra mark.
// It satisfies executor.InfraOverrideRecorder.
func (m *Metrics) RecordInfraOverride(mark string) {
	m.ReconcileInfraOverrides.WithLabelValues(mark).Inc()
}

// RecordDispatchInnerError counts one error returned by the inner dispatcher
// inside a worker — typically a Kubernetes API failure or pod-create rejection.
func (m *Metrics) RecordDispatchInnerError() { m.DispatchInnerErrors.Inc() }

// RecordUnitMisfit counts one task let through under executor.unit.enforce=warn
// although it does not fit its size. It satisfies dispatch.UnitMisfitRecorder
// and api.UnitMisfitRecorder.
func (m *Metrics) RecordUnitMisfit(stage string) { m.UnitMisfits.WithLabelValues(stage).Inc() }

// RecordAdmission counts one attempt's admission outcome and how long the
// hooks took. It satisfies dispatch.AdmissionRecorder.
func (m *Metrics) RecordAdmission(outcome string, took time.Duration) {
	m.Admissions.WithLabelValues(outcome).Inc()
	m.AdmissionDuration.Observe(took.Seconds())
}

// RecordUnpinnedImage counts one image not pinned by digest that
// deploy.require_digest warned about or refused at stage. It satisfies
// api.UnpinnedImageRecorder.
func (m *Metrics) RecordUnpinnedImage(stage, outcome string) {
	m.UnpinnedImages.WithLabelValues(stage, outcome).Inc()
}

// RecordCIOIDCExchange counts one CI OIDC token exchange. It satisfies
// api.CIOIDCExchangeRecorder.
func (m *Metrics) RecordCIOIDCExchange(issuer, outcome string) {
	m.CIOIDCExchanges.WithLabelValues(issuer, outcome).Inc()
}
