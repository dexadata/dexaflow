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
	SchedulerWokenTicks   prometheus.Counter
	SchedulerDecisions    *prometheus.CounterVec
	SchedulerLeader       *prometheus.GaugeVec
	ActiveDAGRuns         *prometheus.GaugeVec
	QueuedTasks           *prometheus.GaugeVec
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

	// Executor (Kubernetes)
	PodsCreated        *prometheus.CounterVec
	PodsRunning        prometheus.Gauge
	PodPendingDuration prometheus.Histogram
	KubernetesAPICalls *prometheus.CounterVec

	// Dispatch pool (#127, ADR 0031): depth gauge, capacity-saturation counter,
	// per-dispatch latency, inner-dispatcher error counter.
	DispatchQueueDepth  prometheus.Gauge
	DispatchAtCapacity  prometheus.Counter
	DispatchLatency     prometheus.Histogram
	DispatchInnerErrors prometheus.Counter

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
}

// NewMetrics registers every ADR 0010 collector with reg and returns the set.
func NewMetrics(reg prometheus.Registerer) *Metrics {
	f := promauto.With(reg)
	sizeBuckets := prometheus.ExponentialBuckets(64, 4, 8)
	return &Metrics{
		SchedulerLoopDuration: f.NewHistogram(prometheus.HistogramOpts{
			Name: "dexaflow_scheduler_loop_duration_seconds", Help: "Duration of one scheduler loop iteration.",
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
		SchedulerLeader: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dexaflow_scheduler_leader", Help: "1 when this replica is the scheduler leader.",
		}, []string{"replica_id"}),
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
		ActiveDAGRuns: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dexaflow_active_dag_runs", Help: "Active dag runs by dag and state.",
		}, []string{"dag_id", "state"}),
		QueuedTasks: f.NewGaugeVec(prometheus.GaugeOpts{
			Name: "dexaflow_queued_tasks", Help: "Queued task instances by dag.",
		}, []string{"dag_id"}),
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

		PodsCreated: f.NewCounterVec(prometheus.CounterOpts{
			Name: "dexaflow_pods_created_total", Help: "Pods created by dag and result.",
		}, []string{"dag_id", "result"}),
		PodsRunning: f.NewGauge(prometheus.GaugeOpts{
			Name: "dexaflow_pods_running", Help: "Currently running pods.",
		}),
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
			Help:    "Wall-clock time per Redis dial — TCP connect + (when rediss://) TLS handshake.",
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
		RedisPoolTimeouts: f.NewCounter(prometheus.CounterOpts{
			Name: "dexaflow_redis_pool_timeouts_total", Help: "Redis pool checkout timeouts — a caller waited PoolTimeout for an idle connection. A non-zero rate means the pool is too small or commands are too slow.",
		}),
	}
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
// enqueue to the worker's inner-dispatcher return. Reserved for future use by
// the BufferedDispatcher.
func (m *Metrics) RecordDispatchLatencySeconds(seconds float64) {
	m.DispatchLatency.Observe(seconds)
}

// RecordDispatchInnerError counts one error returned by the inner dispatcher
// inside a worker — typically a Kubernetes API failure or pod-create rejection.
func (m *Metrics) RecordDispatchInnerError() { m.DispatchInnerErrors.Inc() }
