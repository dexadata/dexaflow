---
title: "internal/domain"
linkTitle: "internal/domain"
weight: 1
---

```go
import "github.com/dexadata/dexaflow/internal/domain"
```

Package domain defines the core Dexaflow types \(DAG, Task, project config\) and validates them against the canonical JSON Schemas in docs/api.

## Index

- [Constants](<#constants>)
- [Variables](<#variables>)
- [func IsCronlessSchedule\(expr string\) bool](<#IsCronlessSchedule>)
- [func IsOnceSchedule\(expr string\) bool](<#IsOnceSchedule>)
- [func ValidateRunID\(v string\) error](<#ValidateRunID>)
- [type AlertRule](<#AlertRule>)
- [type AlertsConfig](<#AlertsConfig>)
- [type AuditLogEntry](<#AuditLogEntry>)
- [type BuildConfig](<#BuildConfig>)
- [type ConfigDefaults](<#ConfigDefaults>)
- [type Connection](<#Connection>)
- [type ConnectionPatch](<#ConnectionPatch>)
- [type DAG](<#DAG>)
- [type DAGSpec](<#DAGSpec>)
  - [func \(d \*DAGSpec\) CanonicalHash\(\) \(string, error\)](<#DAGSpec.CanonicalHash>)
  - [func \(d \*DAGSpec\) Validate\(\) error](<#DAGSpec.Validate>)
  - [func \(d \*DAGSpec\) ValidateSchedule\(\) error](<#DAGSpec.ValidateSchedule>)
- [type DagRun](<#DagRun>)
- [type DagRunState](<#DagRunState>)
  - [func \(s DagRunState\) IsTerminal\(\) bool](<#DagRunState.IsTerminal>)
- [type DagStats](<#DagStats>)
- [type DagVersion](<#DagVersion>)
- [type DbtConfig](<#DbtConfig>)
- [type DefaultArgs](<#DefaultArgs>)
- [type DefaultResources](<#DefaultResources>)
  - [func \(d \*DefaultResources\) AsResources\(\) \*Resources](<#DefaultResources.AsResources>)
- [type Execution](<#Execution>)
- [type ExecutionMode](<#ExecutionMode>)
- [type HistoricalMetrics](<#HistoricalMetrics>)
- [type ImportError](<#ImportError>)
- [type LeoflowConfig](<#LeoflowConfig>)
  - [func \(c \*LeoflowConfig\) ApplyDefaults\(\)](<#LeoflowConfig.ApplyDefaults>)
  - [func \(c \*LeoflowConfig\) EffectiveDependencies\(\) \(\[\]string, error\)](<#LeoflowConfig.EffectiveDependencies>)
  - [func \(c \*LeoflowConfig\) Validate\(\) error](<#LeoflowConfig.Validate>)
- [type ParamSpec](<#ParamSpec>)
- [type Pool](<#Pool>)
- [type PoolUsage](<#PoolUsage>)
- [type RegistryConfig](<#RegistryConfig>)
- [type ResourceQuantity](<#ResourceQuantity>)
- [type Resources](<#Resources>)
- [type StagingConfig](<#StagingConfig>)
- [type StagingVolumeState](<#StagingVolumeState>)
- [type TaskConfig](<#TaskConfig>)
- [type TaskInstance](<#TaskInstance>)
- [type TaskSpec](<#TaskSpec>)
  - [func \(t TaskSpec\) EffectiveExecutionMode\(\) ExecutionMode](<#TaskSpec.EffectiveExecutionMode>)
- [type TaskState](<#TaskState>)
  - [func \(s TaskState\) IsTerminal\(\) bool](<#TaskState.IsTerminal>)
- [type TaskType](<#TaskType>)
- [type TriggerRule](<#TriggerRule>)
- [type User](<#User>)
- [type Variable](<#Variable>)
- [type VariablePatch](<#VariablePatch>)
- [type XComEntryMeta](<#XComEntryMeta>)


## Constants

<a name="DefaultPoolName"></a>DefaultPoolName is the implicit pool a task with no declared pool draws from, so the admission gate is always well\-defined. Matches Airflow's default\_pool.

```go
const DefaultPoolName = "default_pool"
```

## Variables

<a name="ErrCyclicDAG"></a>

```go
var (
    // ErrCyclicDAG reports a task graph with no valid execution order.
    ErrCyclicDAG = errors.New("cyclic task graph")
    // ErrUnknownDependency reports a depends_on entry naming no declared task.
    ErrUnknownDependency = errors.New("unknown task dependency")
    // ErrDuplicateTaskID reports two tasks declaring the same task_id.
    ErrDuplicateTaskID = errors.New("duplicate task_id")
)
```

<a name="ErrConflict"></a>ErrConflict is returned when a write conflicts with an existing resource \(e.g. a duplicate dag run for the same logical date\). The API maps it to 409.

```go
var ErrConflict = errors.New("resource already exists")
```

<a name="ErrInvalidDbtProject"></a>ErrInvalidDbtProject reports a dbt.project path the compiler cannot use.

```go
var ErrInvalidDbtProject = errors.New("invalid dbt.project")
```

<a name="ErrInvalidRunID"></a>ErrInvalidRunID reports a dag\_run\_id a caller may not use.

```go
var ErrInvalidRunID = errors.New("invalid run_id")
```

<a name="ErrNotFound"></a>ErrNotFound is returned when a requested resource does not exist.

```go
var ErrNotFound = errors.New("resource not found")
```

<a name="ErrUnknownAlertPlaceholder"></a>ErrUnknownAlertPlaceholder reports an alert message template referencing a substitution Dexaflow does not perform.

```go
var ErrUnknownAlertPlaceholder = errors.New("unknown alert placeholder")
```

<a name="ErrValidation"></a>ErrValidation is returned when input fails a business\-rule check that the caller can fix \(e.g. creating a user with a role that does not exist\). The API maps it to 400.

```go
var ErrValidation = errors.New("invalid input")
```

<a name="IsCronlessSchedule"></a>
## func [IsCronlessSchedule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L19>)

```go
func IsCronlessSchedule(expr string) bool
```

IsCronlessSchedule reports whether expr is empty \(manual\-only\) or a recognized non\-cron Airflow schedule. Such a schedule is valid but is never run on a cron, so callers skip cron handling for it without treating it as an error.

<a name="IsOnceSchedule"></a>
## func [IsOnceSchedule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L26>)

```go
func IsOnceSchedule(expr string) bool
```

IsOnceSchedule reports whether expr is Airflow's "@once" — a DAG that runs exactly one time \(on first scheduler sight\) and never again.

<a name="ValidateRunID"></a>
## func [ValidateRunID](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/identifiers.go#L27>)

```go
func ValidateRunID(v string) error
```

ValidateRunID checks a caller\-supplied dag\_run\_id.

The trigger endpoint accepts dag\_run\_id verbatim from the request body, and a run id ends up as a path segment in the log sink — so a value carrying a separator or a parent reference steers the control plane's own writes outside the log root, with content the caller also controls.

The sink refuses such a value independently; this exists so the request fails as a readable 400 instead of creating a run that appears to work and then silently produces no logs. Two gates, different jobs: this one is UX, the sink's is the security boundary and must never be relaxed to match this.

Separators are banned, punctuation is not: Airflow\-generated ids embed an RFC3339 timestamp \("manual\_\_2026\-07\-30T12:00:00\+00:00"\), so rejecting ':' or '\+' would reject every run the scheduler creates.

<a name="AlertRule"></a>
## type [AlertRule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L71-L80>)

AlertRule is one channel to notify on an alert event. The endpoint and its secret always come from a managed connection \(Conn\), never a literal URL or token in leoflow.yaml — that keeps credentials out of the compiled dag.json and mirrors the env\-ref secret discipline.

```go
type AlertRule struct {
    // Type is the channel: "slack" (Slack incoming webhook) or "webhook" (a generic
    // HTTP POST, e.g. PagerDuty/Opsgenie/Teams). Validated by the schema enum.
    Type string `json:"type" yaml:"type"`
    // Conn is the managed Dexaflow connection id holding the endpoint (and secret).
    Conn string `json:"conn" yaml:"conn"`
    // Message is the optional notification body; it is templated at fire time with
    // run context ({{dag}}, {{run_id}}, {{task}}, …). Empty uses a default summary.
    Message string `json:"message,omitempty" yaml:"message,omitempty"`
}
```

<a name="AlertsConfig"></a>
## type [AlertsConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L62-L65>)

AlertsConfig groups alert rules by the lifecycle event that fires them. Only on\_failure is wired today \(\#424\); on\_success/on\_retry are reserved for a later increment so the surface can grow without a breaking change.

```go
type AlertsConfig struct {
    // OnFailure lists the rules dispatched when a DagRun reaches failed.
    OnFailure []AlertRule `json:"on_failure,omitempty" yaml:"on_failure,omitempty"`
}
```

<a name="AuditLogEntry"></a>
## type [AuditLogEntry](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/audit.go#L7-L15>)

AuditLogEntry is one recorded action against a resource — the source for the UI's Audit Log table. ResourceID carries the DAG id for dag\-scoped events.

```go
type AuditLogEntry struct {
    ID           int64
    When         time.Time
    Action       string
    ResourceType string
    ResourceID   string
    Owner        string
    Extra        string
}
```

<a name="BuildConfig"></a>
## type [BuildConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L126-L131>)

BuildConfig controls how the container image is built from the project.

```go
type BuildConfig struct {
    Dockerfile string            `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
    Context    string            `json:"context,omitempty" yaml:"context,omitempty"`
    Platforms  []string          `json:"platforms,omitempty" yaml:"platforms,omitempty"`
    Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
}
```

<a name="ConfigDefaults"></a>
## type [ConfigDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L143-L153>)

ConfigDefaults holds task defaults applied to every task generated from the project at compile time.

```go
type ConfigDefaults struct {
    Retries                 int               `json:"retries,omitempty" yaml:"retries,omitempty"`
    RetryDelaySeconds       int               `json:"retry_delay_seconds,omitempty" yaml:"retry_delay_seconds,omitempty"`
    ExecutionTimeoutSeconds int               `json:"execution_timeout_seconds,omitempty" yaml:"execution_timeout_seconds,omitempty"`
    Resources               *DefaultResources `json:"resources,omitempty" yaml:"resources,omitempty"`
    // NodeSelector is the DAG-wide pod placement fallback applied to every task
    // that declares no execution.node_selector of its own. Like Resources it is a
    // default, so the most-specific per-task value always wins. Consumed at
    // compile time by the overlay, which bakes it onto each task in dag.json.
    NodeSelector map[string]string `json:"node_selector,omitempty" yaml:"node_selector,omitempty"`
}
```

<a name="Connection"></a>
## type [Connection](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/connection.go#L6-L16>)

Connection is an Airflow\-style connection: credentials/endpoints for operators, managed from the Admin UI. Password and Extra are encrypted at rest \(ADR 0019\); Password is write\-only and never returned by the API.

```go
type Connection struct {
    ConnID      string
    ConnType    string
    Host        string
    Schema      string
    Login       string
    Password    string
    Port        *int
    Extra       string
    Description string
}
```

<a name="ConnectionPatch"></a>
## type [ConnectionPatch](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/connection.go#L31-L41>)

ConnectionPatch is a tri\-state write to a Connection \(\#887\). Each nullable field is one of three states the write path must keep distinct:

- nil pointer \-\> absent: preserve the stored value \(the upsert passes NULL and COALESCE keeps the current column\).
- non\-nil "" \-\> present and empty: clear the field to empty.
- non\-nil value \-\> present: set the field.

A plain string cannot carry "absent" vs "present and empty", which is why the safe\-merge upsert alone \(v0.4.4\) could neither clear a field nor stop a round\-tripped mask from overwriting a secret. ConnType is always written \(required on every upsert\). Secret\-mask handling \(the mask means "unchanged"\) is resolved by the caller before it builds the patch, so the repository only ever sees the three states above.

```go
type ConnectionPatch struct {
    ConnID      string
    ConnType    string
    Host        *string
    Schema      *string
    Login       *string
    Password    *string
    Port        *int
    Extra       *string
    Description *string
}
```

<a name="DAG"></a>
## type [DAG](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L10-L23>)

DAG is a registered DAG with its scheduling metadata \(distinct from DAGSpec, which is the compiled artifact\).

```go
type DAG struct {
    DagID          string
    Description    string
    Owner          string
    Tags           []string
    Schedule       *string
    ScheduleTZ     string
    StartDate      *time.Time
    IsPaused       bool
    IsActive       bool
    MaxActiveRuns  int
    Catchup        bool
    LastParsedTime *time.Time
}
```

<a name="DAGSpec"></a>
## type [DAGSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L61-L128>)

DAGSpec is the canonical serialized representation of a DAG consumed by the control plane. It mirrors docs/api/dag\-schema.json.

```go
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
    // Alerts declares native on-failure alerting (#424), overlaid from leoflow.yaml
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
```

<a name="DAGSpec.CanonicalHash"></a>
### func \(\*DAGSpec\) [CanonicalHash](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/hash.go#L13>)

```go
func (d *DAGSpec) CanonicalHash() (string, error)
```

CanonicalHash returns the SHA\-256 of the spec's canonical JSON encoding. Go's struct marshaling is deterministic \(fixed field order, sorted map keys\), so identical specs hash identically — used to deduplicate DAG versions.

<a name="DAGSpec.Validate"></a>
### func \(\*DAGSpec\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L287>)

```go
func (d *DAGSpec) Validate() error
```

Validate checks the DAGSpec against the canonical dag.json schema and returns a joined error describing every schema violation, or nil when valid.

<a name="DAGSpec.ValidateSchedule"></a>
### func \(\*DAGSpec\) [ValidateSchedule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L38>)

```go
func (d *DAGSpec) ValidateSchedule() error
```

ValidateSchedule checks that a DAG's cron schedule is parseable. An empty or absent schedule \(manual\-only\) and the recognized non\-cron Airflow schedules \(@once, @continuous\) are valid. A malformed cron expression — a 4\-field cron, a typo — is rejected here so it fails loudly at compile time; otherwise the scheduler silently can't parse it and the DAG simply never runs, with no error surfaced anywhere \(the worst failure mode\). The parser is robfig/cron's ParseStandard, the same one the scheduler uses, so what validates here is exactly what the scheduler can run \(see scheduler/cron.go\).

<a name="DagRun"></a>
## type [DagRun](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L38-L52>)

DagRun is an execution of a DAG, identified by dag\_id \+ run\_id.

```go
type DagRun struct {
    DagID       string
    RunID       string
    LogicalDate time.Time
    State       DagRunState
    RunType     string
    QueuedAt    time.Time
    StartedAt   *time.Time
    EndedAt     *time.Time
    Note        string
    // Conf is the run's configuration as a JSON object, supplied at trigger time
    // and exposed to tasks as params. Empty means no configuration; storage
    // persists the empty-object default so downstream readers never see NULL.
    Conf json.RawMessage
}
```

<a name="DagRunState"></a>
## type [DagRunState](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L46>)

DagRunState is the lifecycle state of a DagRun. The values mirror the dag\_run\_state enum in the database \(migration 003\).

```go
type DagRunState string
```

<a name="DagRunStateQueued"></a>DAG run lifecycle states.

```go
const (
    // DagRunStateQueued means the run has been created but not started.
    DagRunStateQueued DagRunState = "queued"
    // DagRunStateRunning means at least one task instance is active.
    DagRunStateRunning DagRunState = "running"
    // DagRunStateSuccess means every leaf task reached a successful terminal state.
    DagRunStateSuccess DagRunState = "success"
    // DagRunStateFailed means the run finished with at least one failure.
    DagRunStateFailed DagRunState = "failed"
)
```

<a name="DagRunState.IsTerminal"></a>
### func \(DagRunState\) [IsTerminal](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L61>)

```go
func (s DagRunState) IsTerminal() bool
```

IsTerminal reports whether the dag run state is final.

<a name="DagStats"></a>
## type [DagStats](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dashboard.go#L5-L10>)

DagStats holds the home dashboard's DAG counters: the number of active DAGs and how many have a latest run in each state.

```go
type DagStats struct {
    Active  int
    Failed  int
    Running int
    Queued  int
}
```

<a name="DagVersion"></a>
## type [DagVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L27-L35>)

DagVersion is a registered version of a DAG. VersionNumber is the 1\-based ordinal the UI uses \(the stored version label is free\-form\).

```go
type DagVersion struct {
    ID            string
    VersionNumber int
    CreatedAt     time.Time
    // Version is the deployment label that produced this snapshot: a git describe
    // (tag/SHA) in production, or "dev-<timestamp>" in dev. It is the stable
    // per-deployment identifier under a stable dag_id.
    Version string
}
```

<a name="DbtConfig"></a>
## type [DbtConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L85-L105>)

DbtConfig declares a dbt project as the DAG source \(ADR 0042\). The compiler reads the project's manifest.json and renders one task per dbt node \(or per group\), so a dbt project becomes a Dexaflow DAG with no Cosmos or Airflow.

```go
type DbtConfig struct {
    // Project is the directory containing dbt_project.yml.
    Project string `json:"project,omitempty" yaml:"project,omitempty"`
    // Granularity is the task partition strategy: node, level, folder, or tag
    // (ADR 0042 §5). Empty means node.
    Granularity string `json:"granularity,omitempty" yaml:"granularity,omitempty"`
    // Manifest optionally points to a pre-built manifest.json (the Pro/CI baked
    // path); empty means run `dbt parse` to generate it at compile time.
    Manifest string `json:"manifest,omitempty" yaml:"manifest,omitempty"`
    // Schedule is the DAG's cron expression or preset (e.g. "@daily",
    // "0 6 * * *"). dbt carries no schedule, so it is declared here; empty means
    // an unscheduled DAG (run on demand).
    Schedule string `json:"schedule,omitempty" yaml:"schedule,omitempty"`
    // Connection is a managed Dexaflow connection id (ADR 0043 #2). When set, the
    // dbt task generates its profiles.yml from the connection delivered to the pod
    // instead of a profiles.yml baked into the image — use one or the other.
    Connection string `json:"connection,omitempty" yaml:"connection,omitempty"`
    // Schema overrides the dbt target schema in the generated profile (where models
    // materialize); empty uses the connection's or dbt's default.
    Schema string `json:"schema,omitempty" yaml:"schema,omitempty"`
}
```

<a name="DefaultArgs"></a>
## type [DefaultArgs](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L153-L157>)

DefaultArgs holds retry and timeout defaults applied to every task in a DAG.

```go
type DefaultArgs struct {
    Retries                 int `json:"retries,omitempty"`
    RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty"`
    ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty"`
}
```

<a name="DefaultResources"></a>
## type [DefaultResources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L156-L159>)

DefaultResources expresses default CPU and memory for generated tasks.

```go
type DefaultResources struct {
    CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
    Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}
```

<a name="DefaultResources.AsResources"></a>
### func \(\*DefaultResources\) [AsResources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L171>)

```go
func (d *DefaultResources) AsResources() *Resources
```

AsResources expands the simplified default cpu/memory into a full Resources with requests == limits \(the QoS story of \#725\). This mirrors how the per\-cluster platform default is built at dispatch. Returns nil when the receiver is nil or declares no quantity, so callers can treat a missing default as "leave the task untouched".

Guaranteed QoS is reached only when BOTH cpu and memory are set \(and thus equal across requests and limits\). A partial default — only cpu, or only memory — leaves the other dimension unset, so Kubernetes classifies the pod as Burstable, not Guaranteed.

<a name="Execution"></a>
## type [Execution](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L237-L274>)

Execution carries executor\-specific placement and scheduling hints for a task. Every field beyond NodeSelector/Tolerations/ServiceAccount is applied only by the Kubernetes executor; Lite \(subprocess, no pods\) ignores them.

```go
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
```

<a name="ExecutionMode"></a>
## type [ExecutionMode](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L34>)

ExecutionMode selects how a task runs. Every task runs inside a worker pod; the field is retained for forward compatibility and defaults to pod.

```go
type ExecutionMode string
```

<a name="ExecutionModePod"></a>Supported execution modes. See docs/api/dag\-schema.json.

```go
const (
    // ExecutionModePod runs a task inside a worker pod via the agent.
    ExecutionModePod ExecutionMode = "pod"
)
```

<a name="HistoricalMetrics"></a>
## type [HistoricalMetrics](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dashboard.go#L14-L17>)

HistoricalMetrics holds run\- and task\-instance counts grouped by state over a time window, keyed by the Dexaflow state name \(e.g. "success", "up\_for\_retry"\).

```go
type HistoricalMetrics struct {
    RunStates map[string]int
    TIStates  map[string]int
}
```

<a name="ImportError"></a>
## type [ImportError](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/import_error.go#L9-L20>)

ImportError is a DAG parse/compile failure surfaced as Airflow's "Import Errors" banner on the home dashboard. It is keyed by Filename; a successful re\-import of the same file clears it. The \`leoflow dev\` watcher writes these on a failed compile and removes them on the next good compile.

```go
type ImportError struct {
    // ID is the stable identifier of the error record.
    ID  string
    // Filename is the DAG source path that failed to import.
    Filename string
    // StackTrace is the human-readable parse/compile error (traceback).
    StackTrace string
    // BundleName is the originating bundle (empty when unknown).
    BundleName string
    // Timestamp is when the error was recorded.
    Timestamp time.Time
}
```

<a name="LeoflowConfig"></a>
## type [LeoflowConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L13-L57>)

LeoflowConfig is the developer\-facing project configuration parsed from leoflow.yaml. It mirrors docs/api/leoflow\-yaml\-schema.json and is consumed by \`leoflow compile\` to build an image and emit a DAGSpec.

```go
type LeoflowConfig struct {
    SchemaVersion string   `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
    DagID         string   `json:"dag_id" yaml:"dag_id"`
    Description   string   `json:"description,omitempty" yaml:"description,omitempty"`
    Owner         string   `json:"owner,omitempty" yaml:"owner,omitempty"`
    Tags          []string `json:"tags,omitempty" yaml:"tags,omitempty"`
    PythonVersion string   `json:"python_version,omitempty" yaml:"python_version,omitempty"`
    BaseImage     string   `json:"base_image,omitempty" yaml:"base_image,omitempty"`
    Dependencies  []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
    Connectors    []string `json:"connectors,omitempty" yaml:"connectors,omitempty"`
    // Connections and Variables are the per-DAG declared secret sets (ADR 0045,
    // ADR 0055). Carried verbatim to the parser, which emits them into dag.json.
    // Distinct from Connectors (pip provider packages, ADR 0038) — a different key
    // one letter away. Empty declares nothing.
    Connections    []string        `json:"connections,omitempty" yaml:"connections,omitempty"`
    Variables      []string        `json:"variables,omitempty" yaml:"variables,omitempty"`
    SystemPackages []string        `json:"system_packages,omitempty" yaml:"system_packages,omitempty"`
    DagSource      string          `json:"dag_source,omitempty" yaml:"dag_source,omitempty"`
    IncludePaths   []string        `json:"include_paths,omitempty" yaml:"include_paths,omitempty"`
    ExcludePaths   []string        `json:"exclude_paths,omitempty" yaml:"exclude_paths,omitempty"`
    Build          *BuildConfig    `json:"build,omitempty" yaml:"build,omitempty"`
    Registry       *RegistryConfig `json:"registry,omitempty" yaml:"registry,omitempty"`
    Defaults       *ConfigDefaults `json:"defaults,omitempty" yaml:"defaults,omitempty"`
    // Staging requests the opt-in per-DAG-run shared volume (ADR 0022). It is a
    // Dexaflow deployment concern (not an Airflow DAG attribute), so it lives in
    // leoflow.yaml and the compiler overlays it onto the produced dag.json.
    Staging *StagingConfig `json:"staging,omitempty" yaml:"staging,omitempty"`
    // Dbt declares a dbt project as the DAG source (ADR 0042). Its presence routes
    // `dexaflow compile` to the dbt renderer instead of the Python parser.
    Dbt *DbtConfig `json:"dbt,omitempty" yaml:"dbt,omitempty"`
    // DbtGroups configures dbt projects embedded as task groups in a dag.py (ADR
    // 0043), keyed by the name passed to `dbt_group(name)`. Schedule does not apply
    // to a group (the DAG owns the schedule).
    DbtGroups map[string]*DbtConfig `json:"dbt_groups,omitempty" yaml:"dbt_groups,omitempty"`
    // Tasks holds per-task overrides bound by task_id (ADR 0023). Each entry's
    // key must match a task_id in the compiled DAG; the compiler errors on an
    // unknown id rather than silently dropping it.
    Tasks map[string]*TaskConfig `json:"tasks,omitempty" yaml:"tasks,omitempty"`
    // Alerts declares native on-failure alerting (#424): the scheduler fires the
    // listed rules when a DagRun reaches the terminal failed state, in Go, with no
    // task pod and no Python in the hot path. A Dexaflow deployment concern (not an
    // Airflow DAG attribute), so it lives in leoflow.yaml and the compiler overlays
    // it onto the produced dag.json.
    Alerts *AlertsConfig `json:"alerts,omitempty" yaml:"alerts,omitempty"`
}
```

<a name="LeoflowConfig.ApplyDefaults"></a>
### func \(\*LeoflowConfig\) [ApplyDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L191>)

```go
func (c *LeoflowConfig) ApplyDefaults()
```

ApplyDefaults fills zero\-valued fields with the defaults declared in the canonical JSON Schema \(internal/domain/schemas/leoflow\-yaml\-schema.json\). Explicit user\-set values are preserved; nested structs \(Build, Registry\) are instantiated when nil so their own defaults can be applied. The method is idempotent: a second call after the first is a no\-op.

Centralizing defaults here \(instead of scattered \`if x == ""\` fallbacks at each consumer\) is what lets the multi\-DAG workspace synthesize a working config when a subdir ships no leoflow.yaml, while keeping the resolved values debuggable from one place.

<a name="LeoflowConfig.EffectiveDependencies"></a>
### func \(\*LeoflowConfig\) [EffectiveDependencies](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L237>)

```go
func (c *LeoflowConfig) EffectiveDependencies() ([]string, error)
```

EffectiveDependencies resolves the full pip install list the image/venv needs: the \`connectors:\` short names expanded to their apache\-airflow\-providers\-\* packages \(ADR 0038's sugar\), followed by the explicit \`dependencies:\` verbatim. Providers come first so a transitive driver pinned in dependencies resolves against the provider declared via the sugar.

An unknown connector name is a compile error, not a silent drop: a typo that slipped through would otherwise surface as a ModuleNotFoundError inside the task pod, far from its cause. The message names the offender, lists the known types, and points at the dependencies: escape hatch.

<a name="LeoflowConfig.Validate"></a>
### func \(\*LeoflowConfig\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L258>)

```go
func (c *LeoflowConfig) Validate() error
```

Validate checks the LeoflowConfig against the canonical leoflow.yaml schema and returns a joined error describing every violation, or nil when valid.

<a name="ParamSpec"></a>
## type [ParamSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L135-L141>)

ParamSpec is one author\-declared DAG\-run parameter: a default value and the JSON Schema its trigger\-time conf value is validated against. Both are carried as raw JSON so an arbitrary default and an arbitrary schema round\-trip verbatim. Schema is \{\} \(or absent\) when the author declared a bare default with no constraints, in which case any conf value for that key is accepted.

```go
type ParamSpec struct {
    // Default is the value merged into a run's conf when the trigger omits this
    // key. Absent (omitempty, len 0) means the param is REQUIRED — the trigger
    // must supply it — as distinct from an explicit JSON null default.
    Default json.RawMessage `json:"default,omitempty"`
    Schema  json.RawMessage `json:"schema,omitempty"`
}
```

<a name="Pool"></a>
## type [Pool](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/pool.go#L8-L13>)

Pool is a named, tenant\-scoped, cross\-DAG task\-concurrency budget \(Airflow's pool\). Slots is the cap: a task in the pool is admitted to \`queued\` only while the pool has a free slot, counting queued\+running task instances across every DAG \(ADR 0053 Stage 3\). IsDefault marks the implicit default\_pool a task with no declared pool falls back to. Pools are a Pro\-only concept.

```go
type Pool struct {
    Name        string
    Slots       int
    Description string
    IsDefault   bool
}
```

<a name="PoolUsage"></a>
## type [PoolUsage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/pool.go#L22-L27>)

PoolUsage is a pool's per\-state occupancy, feeding the Airflow PoolResponse slot fields. The slots admission actually spends are queued\+running; scheduled and deferred are reported for the UI but do not hold a slot.

```go
type PoolUsage struct {
    Running   int
    Queued    int
    Scheduled int
    Deferred  int
}
```

<a name="RegistryConfig"></a>
## type [RegistryConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L134-L139>)

RegistryConfig describes where the built image is pushed and how it is tagged.

```go
type RegistryConfig struct {
    URL         string `json:"url,omitempty" yaml:"url,omitempty"`
    AuthMethod  string `json:"auth_method,omitempty" yaml:"auth_method,omitempty"`
    ImageName   string `json:"image_name,omitempty" yaml:"image_name,omitempty"`
    TagStrategy string `json:"tag_strategy,omitempty" yaml:"tag_strategy,omitempty"`
}
```

<a name="ResourceQuantity"></a>
## type [ResourceQuantity](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L224-L232>)

ResourceQuantity expresses CPU, memory, and ephemeral\-storage in Kubernetes notation.

```go
type ResourceQuantity struct {
    CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
    Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
    // EphemeralStorage bounds the node-local scratch (writable layer, emptyDir,
    // logs) a task may use. Setting it keeps a runaway task from filling a shared
    // node's disk and evicting its neighbors under disk pressure (ADR 0054).
    // Kubernetes quantity, e.g. "2Gi".
    EphemeralStorage string `json:"ephemeral_storage,omitempty" yaml:"ephemeral_storage,omitempty"`
}
```

<a name="Resources"></a>
## type [Resources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L210-L220>)

Resources holds Kubernetes\-style resource requests and limits for a task.

```go
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
```

<a name="StagingConfig"></a>
## type [StagingConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L146-L150>)

StagingConfig is the opt\-in per\-DAG\-run shared staging volume \(ADR 0022\). Size is a Kubernetes quantity \(e.g. "5Gi"\); StorageClass empty uses the cluster default RWX class.

```go
type StagingConfig struct {
    Enabled      bool   `json:"enabled" yaml:"enabled"`
    Size         string `json:"size,omitempty" yaml:"size,omitempty"`
    StorageClass string `json:"storage_class,omitempty" yaml:"storage_class,omitempty"`
}
```

<a name="StagingVolumeState"></a>
## type [StagingVolumeState](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/staging_volume.go#L9-L21>)

StagingVolumeState is a tracked per\-run staging volume joined with its DAG run's state, used by the GC to decide deletion \(ADR 0022\). RunState is empty when the run row is gone \(orphan\); RunEndedAt is the run's terminal time, used for the post\-terminal TTL on failed runs.

```go
type StagingVolumeState struct {
    // PVCName is the staging PersistentVolumeClaim's name.
    PVCName string
    // RunState is the DAG run's state ("success", "failed", "running", …), or
    // empty when the run no longer exists.
    RunState string
    // RunEndedAt is when the run reached a terminal state, if known.
    RunEndedAt *time.Time
    // CreatedAt is when the volume was provisioned. The GC never deletes a volume
    // younger than the TTL when its run cannot be resolved, so a lookup miss can
    // never reclaim an active run's fresh volume.
    CreatedAt time.Time
}
```

<a name="TaskConfig"></a>
## type [TaskConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L111-L123>)

TaskConfig holds the leoflow.yaml per\-task overrides bound by task\_id \(ADR 0023\). Every field is optional; a set field overrides the value compiled from the DAG \(most specific wins: task override \> DAG default\_args\). These are Leoflow deployment concerns, not Airflow operator attributes.

```go
type TaskConfig struct {
    Retries                 *int              `json:"retries,omitempty" yaml:"retries,omitempty"`
    RetryDelaySeconds       *int              `json:"retry_delay_seconds,omitempty" yaml:"retry_delay_seconds,omitempty"`
    ExecutionTimeoutSeconds *int              `json:"execution_timeout_seconds,omitempty" yaml:"execution_timeout_seconds,omitempty"`
    Env                     map[string]string `json:"env,omitempty" yaml:"env,omitempty"`
    // Connections and Variables narrow the DAG-level declared secret set to this
    // task (ADR 0045 §Settled #1, ADR 0055). Empty means the task inherits the
    // DAG-level declaration.
    Connections []string   `json:"connections,omitempty" yaml:"connections,omitempty"`
    Variables   []string   `json:"variables,omitempty" yaml:"variables,omitempty"`
    Resources   *Resources `json:"resources,omitempty" yaml:"resources,omitempty"`
    Execution   *Execution `json:"execution,omitempty" yaml:"execution,omitempty"`
}
```

<a name="TaskInstance"></a>
## type [TaskInstance](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L55-L87>)

TaskInstance is an execution of a task within a DagRun.

```go
type TaskInstance struct {
    DagID     string
    RunID     string
    TaskID    string
    MapIndex  int
    TryNumber int
    MaxTries  int
    State     TaskState
    Operator  string
    // ScheduledAt and QueuedAt record when the instance first entered the
    // scheduled and queued states (Airflow's scheduled_when / queued_when).
    ScheduledAt *time.Time
    QueuedAt    *time.Time
    StartedAt   *time.Time
    EndedAt     *time.Time
    Duration    *float64
    Hostname    string
    // Note is operational context shown in the UI's task panel — e.g. why a task
    // is queued but not running (no executor available).
    Note string
    // FailureReason is a short, human-readable cause for a terminal failure,
    // recorded by whichever component observed it: the agent's own report, the
    // reconciler reading the pod (image pull, OOM, exit code), a reaper declaring
    // the pod or agent lost, or the agent's pre-registration classification. It is
    // the answer to "why did this fail?" for an attempt that streamed no logs
    // because its agent never started — the case where the only remaining source
    // of truth used to be `kubectl logs` against the cluster.
    //
    // It is best-effort and often empty: a healthy instance has none, and a cause
    // nobody observed cannot be invented. It carries a classification, never a
    // credential or a raw internal error.
    FailureReason string
}
```

<a name="TaskSpec"></a>
## type [TaskSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L160-L207>)

TaskSpec describes a single unit of work within a DAG.

```go
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
```

<a name="TaskSpec.EffectiveExecutionMode"></a>
### func \(TaskSpec\) [EffectiveExecutionMode](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L278>)

```go
func (t TaskSpec) EffectiveExecutionMode() ExecutionMode
```

EffectiveExecutionMode returns the task's execution mode, defaulting to pod when unset. Every task runs in a worker pod.

<a name="TaskState"></a>
## type [TaskState](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L5>)

TaskState is the lifecycle state of a TaskInstance. The values mirror the task\_state enum in the database \(migration 003\).

```go
type TaskState string
```

<a name="TaskStateNone"></a>Task lifecycle states.

```go
const (
    // TaskStateNone is the initial state: the task has not been considered yet.
    TaskStateNone TaskState = "none"
    // TaskStateScheduled means dependencies are satisfied and the task is queued for dispatch.
    TaskStateScheduled TaskState = "scheduled"
    // TaskStateQueued means the executor has been asked to start the task.
    TaskStateQueued TaskState = "queued"
    // TaskStateRunning means the task is executing.
    TaskStateRunning TaskState = "running"
    // TaskStateSuccess means the task finished successfully.
    TaskStateSuccess TaskState = "success"
    // TaskStateFailed means the task finished with an error.
    TaskStateFailed TaskState = "failed"
    // TaskStateSkipped means the task was deliberately not run.
    TaskStateSkipped TaskState = "skipped"
    // TaskStateUpstreamFailed means a required upstream failed, so the task cannot run.
    TaskStateUpstreamFailed TaskState = "upstream_failed"
    // TaskStateUpForRetry means the task failed but has retries remaining.
    TaskStateUpForRetry TaskState = "up_for_retry"
    // TaskStateUpForReschedule means a reschedule-mode sensor poked not-ready and
    // released its pod; the scheduler re-dispatches it once reschedule_at is reached,
    // without consuming retry budget (ADR 0040 Phase B, #380). Non-terminal.
    TaskStateUpForReschedule TaskState = "up_for_reschedule"
)
```

<a name="TaskState.IsTerminal"></a>
### func \(TaskState\) [IsTerminal](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L35>)

```go
func (s TaskState) IsTerminal() bool
```

IsTerminal reports whether the task state is final \(no further automatic transitions occur from it\).

<a name="TaskType"></a>
## type [TaskType](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L13>)

TaskType enumerates the kinds of work a task can perform.

```go
type TaskType string
```

<a name="TaskTypePython"></a>Supported task types. See docs/api/dag\-schema.json.

```go
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
```

<a name="TriggerRule"></a>
## type [TriggerRule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L43>)

TriggerRule decides whether a task runs based on its upstreams' states.

```go
type TriggerRule string
```

<a name="TriggerRuleAllSuccess"></a>Supported trigger rules for the MVP. See docs/api/dag\-schema.json.

```go
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
```

<a name="User"></a>
## type [User](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/user.go#L10-L16>)

User is a control\-plane account as returned by the admin user\-management API. It never carries the password or its hash — those are write\-only. Roles is the full set of role names the user holds: the list path aggregates every role grant, and the create path echoes back the roles it granted \(empty when none were requested\).

```go
type User struct {
    ID        string
    Email     string
    Roles     []string
    IsActive  bool
    CreatedAt time.Time
}
```

<a name="Variable"></a>
## type [Variable](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/variable.go#L6-L10>)

Variable is a tenant\-scoped key/value setting consumed by DAGs and managed from the Admin UI. Value is stored as\-is \(plaintext for the MVP\); the API masks values of secret\-ish keys.

```go
type Variable struct {
    Key         string
    Value       string
    Description string
}
```

<a name="VariablePatch"></a>
## type [VariablePatch](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/variable.go#L18-L22>)

VariablePatch is a tri\-state write to a Variable \(\#887\). Description mirrors domain.ConnectionPatch: nil preserves the stored value \(COALESCE\), non\-nil "" clears, and a value sets. Value is subtly different because the \`value\` column is NOT NULL: the caller resolves an omitted or masked \("\*\*\*" for a sensitive key\) value to the stored value BEFORE building the patch, so Value is expected non\-nil here — a non\-nil "" still clears. Key is always written.

```go
type VariablePatch struct {
    Key         string
    Value       *string
    Description *string
}
```

<a name="XComEntryMeta"></a>
## type [XComEntryMeta](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/xcom.go#L8-L12>)

XComEntryMeta is the metadata for one stored XCom value \(without the value payload\) — the source for a task instance's XCom list. Dexaflow XComs are unmapped, so MapIndex is \-1.

```go
type XComEntryMeta struct {
    Key       string
    Timestamp time.Time
    MapIndex  int
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
