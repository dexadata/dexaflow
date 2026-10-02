// Package domain defines the core Dexaflow types (DAG, Task, project config)
// and validates them against the canonical JSON Schemas in docs/api.
package domain

import (
	"encoding/json"
	"fmt"

	"k8s.io/apimachinery/pkg/api/resource"
)

// TaskType enumerates the kinds of work a task can perform.
type TaskType string

// Supported task types. See docs/api/dag-schema.json.
const (
	// TaskTypePython runs a Python callable identified by an entrypoint.
	TaskTypePython TaskType = "python"
	// TaskTypeBash runs a shell command supplied as the entrypoint.
	TaskTypeBash TaskType = "bash"
	// TaskTypeAirflowOperator runs a captured Airflow provider operator/sensor in
	// the task pod via the generic executor (ADR 0040): the runtime instantiates
	// OperatorClass with OperatorArgs and calls execute(). The provider is
	// installed in the image via connectors:/dependencies:.
	TaskTypeAirflowOperator TaskType = "airflow_operator"
	// TaskTypeDbtGroup is a transient placeholder for a dbt project embedded in a
	// DAG (ADR 0043). The compiler expands it into one task per dbt node and the
	// type never appears in a finished dag.json.
	TaskTypeDbtGroup TaskType = "dbt_group"
)

// ExecutionMode selects how a task runs. Every task runs inside a worker pod;
// the field is retained for forward compatibility and defaults to pod.
type ExecutionMode string

// Supported execution modes. See docs/api/dag-schema.json.
const (
	// ExecutionModePod runs a task inside a worker pod via the agent.
	ExecutionModePod ExecutionMode = "pod"
)

// TriggerRule decides whether a task runs based on its upstreams' states.
type TriggerRule string

// Supported trigger rules for the MVP. See docs/api/dag-schema.json.
const (
	// TriggerRuleAllSuccess runs when every upstream succeeded (default).
	TriggerRuleAllSuccess TriggerRule = "all_success"
	// TriggerRuleAllFailed runs when every upstream failed.
	TriggerRuleAllFailed TriggerRule = "all_failed"
	// TriggerRuleAllDone runs once every upstream finished, regardless of state.
	TriggerRuleAllDone TriggerRule = "all_done"
	// TriggerRuleOneSuccess runs as soon as one upstream succeeds.
	TriggerRuleOneSuccess TriggerRule = "one_success"
	// TriggerRuleOneFailed runs as soon as one upstream fails.
	TriggerRuleOneFailed TriggerRule = "one_failed"
)

// DAGSpec is the canonical serialized representation of a DAG consumed by the
// control plane. It mirrors docs/api/dag-schema.json.
type DAGSpec struct {
	SchemaVersion string   `json:"schema_version"`
	DagID         string   `json:"dag_id"`
	DagVersion    string   `json:"dag_version"`
	Image         string   `json:"image"`
	Description   string   `json:"description,omitempty"`
	Owner         string   `json:"owner,omitempty"`
	Tags          []string `json:"tags,omitempty"`
	Schedule      *string  `json:"schedule,omitempty"`
	ScheduleTZ    string   `json:"schedule_timezone,omitempty"`
	StartDate     string   `json:"start_date,omitempty"`
	EndDate       *string  `json:"end_date,omitempty"`
	MaxActiveRuns int      `json:"max_active_runs,omitempty"`
	// MaxActiveTasks caps how many of this DAG's task instances may be
	// concurrently non-terminal (queued or running) across all of its active
	// runs — Airflow's per-DAG max_active_tasks (ADR 0053 Stage 1). Zero (the
	// default) means unlimited: a DAG that never sets it, and all of Lite, plans
	// exactly as before. The scheduler enforces it in PlanRun's scheduled→queued
	// admission gate.
	MaxActiveTasks int `json:"max_active_tasks,omitempty"`
	// MinIdleWorkers is a DORMANT seam for per-DAG author-declared warmth: the
	// number of warm workers an author would want kept ready for this DAG version
	// so its tasks skip cold-pod startup (ADR 0058, warm pools model A2). It is NOT
	// author-settable today. There is no author entry point: the field is absent
	// from the authoring schema (leoflow-yaml-schema.json, which is
	// additionalProperties:false) and the parser never emits it, so after the
	// parse→compile path this value is ALWAYS 0. The intended split is that the
	// operator gates IF warmth happens at all (execution.warm_pools_enabled) while
	// the author would only tune HOW MANY, with the operator clamping the request;
	// whether a pod may be reused across attempts stays the operator's security
	// decision. The downstream is intentionally pre-wired around this field —
	// config.ExecutionSection.EffectiveMinIdle already clamps it and the scheduler
	// store already reads it — so exposing it to authors later is a schema+parser
	// change only (add the key to the authoring schema and have the parser emit
	// it), with no domain/scheduler rework. Until then it stays 0 and every DAG,
	// and all of Lite, behaves exactly as before.
	MinIdleWorkers int          `json:"min_idle_workers,omitempty"`
	Catchup        bool         `json:"catchup,omitempty"`
	DefaultArgs    *DefaultArgs `json:"default_args,omitempty"`
	// Staging, when enabled, requests an ephemeral RWX volume shared by the run's
	// tasks at /staging (ADR 0022). nil/disabled means no staging volume.
	Staging *StagingConfig `json:"staging,omitempty"`
	// Alerts declares native on-failure alerting (#424), overlaid from dexaflow.yaml
	// at compile time so the scheduler fires it from the artifact without re-reading
	// the project config. nil means no alerting.
	Alerts *AlertsConfig `json:"alerts,omitempty"`
	// Variables and Connections are the secret names this DAG declares (ADR 0045,
	// ADR 0055). A task receives a variable or connection only if the DAG declared
	// it; TaskSpec may narrow the set further per task. Absent (empty) is always
	// valid and means the DAG declares nothing — the additive, back-compatible
	// default. These carry the declaration only; secret delivery still ships the
	// whole tenant vault until enforcement lands on a later increment.
	Variables   []string `json:"variables,omitempty"`
	Connections []string `json:"connections,omitempty"`
	// Params are the DAG's author-declared run parameters (Airflow's params=),
	// keyed by name. Each carries a Default (materialized into a run's conf when
	// the trigger omits that key) and an optional JSON Schema the trigger-time
	// conf value is validated against. Absent (empty) means the DAG declares no
	// params — the additive, back-compatible default, so the compiled shape of a
	// param-free DAG is unchanged. Part of the immutable spec (CanonicalHash), so
	// changing a default or schema produces a new DAG version.
	Params map[string]ParamSpec `json:"params,omitempty"`
	Tasks  []TaskSpec           `json:"tasks"`
	// Source is the original dag.py text, captured at compile time so the UI's
	// Code tab can show the Python a human wrote (not the compiled spec). It is
	// part of the artifact: changing it produces a new version.
	Source string `json:"source,omitempty"`
}

// ParamSpec is one author-declared DAG-run parameter: a default value and the
// JSON Schema its trigger-time conf value is validated against. Both are carried
// as raw JSON so an arbitrary default and an arbitrary schema round-trip
// verbatim. Schema is {} (or absent) when the author declared a bare default
// with no constraints, in which case any conf value for that key is accepted.
type ParamSpec struct {
	// Default is the value merged into a run's conf when the trigger omits this
	// key. Absent (omitempty, len 0) means the param is REQUIRED — the trigger
	// must supply it — as distinct from an explicit JSON null default.
	Default json.RawMessage `json:"default,omitempty"`
	Schema  json.RawMessage `json:"schema,omitempty"`
}

// StagingConfig is the opt-in per-DAG-run shared staging volume (ADR 0022). Size
// is a Kubernetes quantity (e.g. "5Gi"); StorageClass empty uses the cluster
// default RWX class.
type StagingConfig struct {
	Enabled      bool   `json:"enabled" yaml:"enabled"`
	Size         string `json:"size,omitempty" yaml:"size,omitempty"`
	StorageClass string `json:"storage_class,omitempty" yaml:"storage_class,omitempty"`
}

// DefaultArgs holds retry and timeout defaults applied to every task in a DAG.
type DefaultArgs struct {
	Retries                 int `json:"retries,omitempty"`
	RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty"`
}

// TaskSpec describes a single unit of work within a DAG.
type TaskSpec struct {
	TaskID      string      `json:"task_id"`
	Type        TaskType    `json:"type"`
	DependsOn   []string    `json:"depends_on,omitempty"`
	TriggerRule TriggerRule `json:"trigger_rule,omitempty"`
	// Pool is the named task pool this task draws a slot from (Airflow's `pool`),
	// the cross-DAG concurrency budget admission enforces (ADR 0053 Stage 3). Empty
	// (the default) means the implicit default_pool, so every task is always in a
	// well-defined pool. The pool gate is Pro-only; Lite ignores this field, so a
	// DAG that sets it plans identically on Lite.
	Pool                    string            `json:"pool,omitempty"`
	Retries                 *int              `json:"retries,omitempty"`
	RetryDelaySeconds       *int              `json:"retry_delay_seconds,omitempty"`
	ExecutionTimeoutSeconds *int              `json:"execution_timeout_seconds,omitempty"`
	ExecutionMode           ExecutionMode     `json:"execution_mode,omitempty"`
	Entrypoint              string            `json:"entrypoint,omitempty"`
	Env                     map[string]string `json:"env,omitempty"`
	// Variables and Connections narrow the DAG's declared secret set to this task
	// (ADR 0045 §Settled #1, ADR 0055). Absent (empty) means the task inherits the
	// DAG-level declaration. Carries the declaration only; delivery is unchanged.
	Variables   []string            `json:"variables,omitempty"`
	Connections []string            `json:"connections,omitempty"`
	Resources   *Resources          `json:"resources,omitempty"`
	Execution   *Execution          `json:"execution,omitempty"`
	XComInput   map[string][]string `json:"xcom_input,omitempty"`
	XComSchema  map[string]any      `json:"xcom_schema,omitempty"`
	// CallArgs carries TaskFlow literal call arguments captured at compile time
	// (#115). The agent serializes the whole map as a single env var
	// LEOFLOW_CALL_ARGS_JSON; the runtime decodes and delivers each value to
	// the user function. XCom upstreams take precedence at runtime over a
	// same-name literal (the deterministic merge owned by leoflow_runtime).
	// Named call_args (not params) to leave the term free for Airflow's
	// DAG-run params semantic (#148).
	CallArgs map[string]any `json:"call_args,omitempty"`
	// OperatorClass is the dotted Airflow operator/sensor class for an
	// airflow_operator task (ADR 0040), e.g.
	// "airflow.providers.snowflake.operators.snowflake.SQLExecuteQueryOperator".
	OperatorClass string `json:"operator_class,omitempty"`
	// OperatorArgs are the operator's constructor kwargs captured at compile time.
	// The agent serializes them as the env var LEOFLOW_OPERATOR_ARGS; the runtime
	// instantiates the operator with them.
	OperatorArgs map[string]any `json:"operator_args,omitempty"`
	// OnFailureCallback marks that the task declares an Airflow on_failure_callback
	// (#424). The callable itself is not carried (it can't be serialized); the
	// runtime re-imports dag.py and runs it in the task process on failure. The
	// flag lets the agent/UI know a callback will run without importing user code.
	OnFailureCallback bool `json:"on_failure_callback,omitempty"`
}

// Resources holds Kubernetes-style resource requests and limits for a task.
type Resources struct {
	Requests *ResourceQuantity `json:"requests,omitempty" yaml:"requests,omitempty"`
	Limits   *ResourceQuantity `json:"limits,omitempty" yaml:"limits,omitempty"`
	// Claims lists the ResourceClaims (declared in Execution.ResourceClaims) this
	// task's container consumes — the container half of Dynamic Resource Allocation
	// (DRA, GA in Kubernetes 1.34). Untyped []map[string]any carried verbatim from
	// the DAG spec; the executor round-trips it to []corev1.ResourceClaim. Each
	// entry names a claim (and optionally a specific request within it) that makes
	// an accelerator available inside the container.
	Claims []map[string]any `json:"claims,omitempty" yaml:"claims,omitempty"`
}

// ResourceQuantity expresses CPU, memory, and ephemeral-storage in Kubernetes
// notation.
type ResourceQuantity struct {
	CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
	Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
	// EphemeralStorage bounds the node-local scratch (writable layer, emptyDir,
	// logs) a task may use. Setting it keeps a runaway task from filling a shared
	// node's disk and evicting its neighbors under disk pressure (ADR 0054).
	// Kubernetes quantity, e.g. "2Gi".
	EphemeralStorage string `json:"ephemeral_storage,omitempty" yaml:"ephemeral_storage,omitempty"`
}

// Execution carries executor-specific placement and scheduling hints for a task.
// Every field beyond NodeSelector/Tolerations/ServiceAccount is applied only by
// the Kubernetes executor; Lite (subprocess, no pods) ignores them.
type Execution struct {
	NodeSelector    map[string]string `json:"node_selector,omitempty" yaml:"node_selector,omitempty"`
	Tolerations     []map[string]any  `json:"tolerations,omitempty" yaml:"tolerations,omitempty"`
	ServiceAccount  string            `json:"service_account,omitempty" yaml:"service_account,omitempty"`
	ImagePullPolicy string            `json:"image_pull_policy,omitempty" yaml:"image_pull_policy,omitempty"`

	// PriorityClassName ranks this task pod against its neighbors on a shared
	// cluster; the named PriorityClass is a platform-owned, cluster-scoped object,
	// so under genuine contention the scheduler preempts Dexaflow's ETL rather than
	// production services (ADR 0054).
	PriorityClassName string `json:"priority_class_name,omitempty" yaml:"priority_class_name,omitempty"`
	// TerminationGracePeriodSeconds is how long the pod is given to shut down after
	// a delete/preempt before SIGKILL. Nil leaves the cluster default (30s).
	TerminationGracePeriodSeconds *int64 `json:"termination_grace_period_seconds,omitempty" yaml:"termination_grace_period_seconds,omitempty"`
	// RuntimeClassName selects an alternate container runtime (e.g. a sandboxed or
	// GPU runtime) registered as a RuntimeClass. Nil uses the cluster default.
	RuntimeClassName *string `json:"runtime_class_name,omitempty" yaml:"runtime_class_name,omitempty"`
	// TopologySpreadConstraints spread a DAG's task pods across failure domains
	// (zones, nodes). Untyped []map[string]any carried verbatim from the DAG spec;
	// the executor round-trips it to []corev1.TopologySpreadConstraint.
	TopologySpreadConstraints []map[string]any `json:"topology_spread_constraints,omitempty" yaml:"topology_spread_constraints,omitempty"`
	// Affinity pins or repels a task pod relative to nodes and other pods
	// (node/pod affinity and anti-affinity). Untyped map[string]any carried verbatim
	// from the DAG spec; the executor round-trips it to *corev1.Affinity.
	Affinity map[string]any `json:"affinity,omitempty" yaml:"affinity,omitempty"`
	// ResourceClaims declares the pod-level ResourceClaims (Dynamic Resource
	// Allocation, GA in Kubernetes 1.34) an accelerator DAG needs — e.g. a GPU from
	// a claim template. Untyped []map[string]any carried verbatim from the DAG spec;
	// the executor round-trips it to []corev1.PodResourceClaim. A container consumes
	// one by naming it in Resources.Claims.
	ResourceClaims []map[string]any `json:"resource_claims,omitempty" yaml:"resource_claims,omitempty"`
	// Labels and Annotations are operator-declared pod metadata merged onto the task
	// pod. Dexaflow's own leoflow.io/* labels and the task-instance-id annotation win
	// any key collision (the reconciler and terminate path select on them), so a DAG
	// cannot shadow them.
	Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
	Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}

// EffectiveExecutionMode returns the task's execution mode, defaulting to pod
// when unset. Every task runs in a worker pod.
func (t TaskSpec) EffectiveExecutionMode() ExecutionMode {
	if t.ExecutionMode != "" {
		return t.ExecutionMode
	}
	return ExecutionModePod
}

// Validate checks the DAGSpec against the canonical dag.json schema and
// returns a joined error describing every schema violation, or nil when valid.
func (d *DAGSpec) Validate() error {
	// The native inline http_api task type is removed (ADR 0047/0048, #512): it
	// executed an author-supplied request in the control-plane process (an SSRF
	// surface). Reject it with a clear, actionable message — a literal string
	// match (the type constant no longer exists) so a hand-written dag.json is
	// refused at registration. HttpOperator runs in a task pod instead.
	for _, t := range d.Tasks {
		if t.Type == "http_api" {
			return fmt.Errorf("task %q uses the removed task type \"http_api\" (ADR 0047): "+
				"the native inline HTTP executor ran in the control plane and was an SSRF surface. "+
				"Use an HttpOperator, which runs in a task pod (declare connectors: [http])", t.TaskID)
		}
	}
	// Refuse a declared param whose schema is invalid or whose default violates
	// it now, at registration — not on every trigger (fail while the author can
	// see it, matching the resource-quantity philosophy below).
	for name, p := range d.Params {
		if err := validateParamSpec(name, p.Schema, p.Default); err != nil {
			return err
		}
	}
	s, err := schemas()
	if err != nil {
		return err
	}
	if err := validateAgainst(s.dag, d); err != nil {
		return err
	}
	if err := d.validateGraph(); err != nil {
		return err
	}
	return d.validateResourceQuantities()
}

// validateResourceQuantities rejects a CPU or memory value Kubernetes cannot
// parse. The schema types these as strings — a JSON Schema cannot express the
// quantity grammar — so without this they pass validation and are dropped at pod
// build (executor.quantities keeps only what ParseQuantity accepts).
//
// Dropping is the dangerous outcome, not the parse failure: a task declaring
// `memory: 2GB` got a pod with NO memory limit, which is the opposite of what it
// asked for, on a shared node, silently. And `2GB` is the plausible typo — it is
// how memory is written everywhere except Kubernetes, which wants `2Gi` or `2G`.
//
// Checked here so `dexaflow compile` fails while the author is still looking at
// it, rather than at registration or, worse, at dispatch.
func (d *DAGSpec) validateResourceQuantities() error {
	for _, t := range d.Tasks {
		if t.Resources == nil {
			continue
		}
		for _, q := range []struct {
			field string
			val   *ResourceQuantity
		}{
			{"requests", t.Resources.Requests},
			{"limits", t.Resources.Limits},
		} {
			if q.val == nil {
				continue
			}
			for _, f := range []struct{ name, value string }{
				{"cpu", q.val.CPU},
				{"memory", q.val.Memory},
				{"ephemeral-storage", q.val.EphemeralStorage},
			} {
				if f.value == "" {
					continue
				}
				if _, err := resource.ParseQuantity(f.value); err != nil {
					return fmt.Errorf(
						"task %q declares resources.%s.%s = %q, which is not a valid Kubernetes quantity: %w. "+
							"Use Kubernetes notation — CPU as cores or millicores (\"2\", \"500m\"), memory with a binary or decimal suffix (\"2Gi\", \"2G\", \"512Mi\"). "+
							"Note \"2GB\" is not valid; Kubernetes spells it \"2Gi\" (1024-based) or \"2G\" (1000-based)",
						t.TaskID, q.field, f.name, f.value, err)
				}
			}
		}
	}
	return nil
}
