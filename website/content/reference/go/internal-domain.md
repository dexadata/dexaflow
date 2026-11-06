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
- [func CheckCopySource\(field, src string\) error](<#CheckCopySource>)
- [func CheckDockerfileWord\(field, v string\) error](<#CheckDockerfileWord>)
- [func CheckImageReference\(field, v string\) error](<#CheckImageReference>)
- [func CheckRegistryPrefix\(field, v string\) error](<#CheckRegistryPrefix>)
- [func CheckRunArgument\(field, w string\) error](<#CheckRunArgument>)
- [func CheckScheduleInterval\(spec DAGSpec, minSeconds int\) error](<#CheckScheduleInterval>)
- [func CheckSourceMode\(modeImage string, spec \*DAGSpec\) error](<#CheckSourceMode>)
- [func CheckSourceModeSize\(dagID, source string\) error](<#CheckSourceModeSize>)
- [func CheckTaskPoolSlots\(spec DAGSpec, maxSlots int\) error](<#CheckTaskPoolSlots>)
- [func ClientMessageOr\(err error, fallback string\) string](<#ClientMessageOr>)
- [func DAGSchemaJSON\(\) \[\]byte](<#DAGSchemaJSON>)
- [func IsCronlessSchedule\(expr string\) bool](<#IsCronlessSchedule>)
- [func IsDigestPinned\(image string\) bool](<#IsDigestPinned>)
- [func IsOnceSchedule\(expr string\) bool](<#IsOnceSchedule>)
- [func IsReservedMetadataKey\(key string\) bool](<#IsReservedMetadataKey>)
- [func MinScheduleInterval\(expr string\) \(time.Duration, bool\)](<#MinScheduleInterval>)
- [func NewTenantSchemaCompiler\(\) \*jsonschema.Compiler](<#NewTenantSchemaCompiler>)
- [func ParseCron\(expr string\) \(cron.Schedule, error\)](<#ParseCron>)
- [func Safef\(class error, format string, a ...any\) error](<#Safef>)
- [func SourceModeApplies\(modeImage, image string, hasSource bool\) bool](<#SourceModeApplies>)
- [func SupportedPythonVersions\(\) \(\[\]string, error\)](<#SupportedPythonVersions>)
- [func TenantSchemaURL\(name string\) string](<#TenantSchemaURL>)
- [func ValidateRunID\(v string\) error](<#ValidateRunID>)
- [type AlertRule](<#AlertRule>)
- [type AlertsConfig](<#AlertsConfig>)
- [type AuditLogEntry](<#AuditLogEntry>)
- [type BuildConfig](<#BuildConfig>)
  - [func \(b \*BuildConfig\) LocalOnly\(\) string](<#BuildConfig.LocalOnly>)
- [type BuildInfo](<#BuildInfo>)
  - [func \(b BuildInfo\) IsZero\(\) bool](<#BuildInfo.IsZero>)
  - [func \(b BuildInfo\) Sanitized\(\) BuildInfo](<#BuildInfo.Sanitized>)
  - [func \(b BuildInfo\) Validate\(\) error](<#BuildInfo.Validate>)
- [type BuildKitConfig](<#BuildKitConfig>)
  - [func \(b BuildKitConfig\) Selector\(\) string](<#BuildKitConfig.Selector>)
- [type BuildSecret](<#BuildSecret>)
- [type CIOIDCDefaults](<#CIOIDCDefaults>)
- [type CIOIDCIssuer](<#CIOIDCIssuer>)
- [type ClearOptions](<#ClearOptions>)
- [type ClearVersionKept](<#ClearVersionKept>)
- [type CloudBuildConfig](<#CloudBuildConfig>)
- [type ConfigDefaults](<#ConfigDefaults>)
- [type Connection](<#Connection>)
  - [func ParseConnectionURI\(connID, uri string\) \(Connection, error\)](<#ParseConnectionURI>)
- [type ConnectionPatch](<#ConnectionPatch>)
- [type CurrentVersionChange](<#CurrentVersionChange>)
- [type DAG](<#DAG>)
- [type DAGSpec](<#DAGSpec>)
  - [func \(d \*DAGSpec\) CanonicalHash\(\) \(string, error\)](<#DAGSpec.CanonicalHash>)
  - [func \(d DAGSpec\) RunParams\(task TaskSpec, conf json.RawMessage\) \(json.RawMessage, error\)](<#DAGSpec.RunParams>)
  - [func \(d \*DAGSpec\) Validate\(\) error](<#DAGSpec.Validate>)
  - [func \(d \*DAGSpec\) ValidateSchedule\(\) error](<#DAGSpec.ValidateSchedule>)
- [type DagFilter](<#DagFilter>)
- [type DagPromotion](<#DagPromotion>)
- [type DagRegistered](<#DagRegistered>)
- [type DagRegistration](<#DagRegistration>)
- [type DagRun](<#DagRun>)
- [type DagRunFilter](<#DagRunFilter>)
- [type DagRunState](<#DagRunState>)
  - [func \(s DagRunState\) IsTerminal\(\) bool](<#DagRunState.IsTerminal>)
  - [func \(s DagRunState\) Valid\(\) bool](<#DagRunState.Valid>)
- [type DagSetCurrent](<#DagSetCurrent>)
- [type DagStats](<#DagStats>)
- [type DagVersion](<#DagVersion>)
- [type DagVersionSelector](<#DagVersionSelector>)
- [type DbtConfig](<#DbtConfig>)
- [type DefaultArgs](<#DefaultArgs>)
- [type DefaultLimits](<#DefaultLimits>)
  - [func ParseDefaultLimits\(cpu, memory string\) \(\*DefaultLimits, error\)](<#ParseDefaultLimits>)
  - [func \(l \*DefaultLimits\) Apply\(r Resources\) Resources](<#DefaultLimits.Apply>)
  - [func \(l \*DefaultLimits\) WarmEligible\(t TaskSpec\) bool](<#DefaultLimits.WarmEligible>)
  - [func \(l \*DefaultLimits\) WarmResources\(\) \*Resources](<#DefaultLimits.WarmResources>)
- [type DefaultResources](<#DefaultResources>)
  - [func \(d \*DefaultResources\) AsResources\(\) \*Resources](<#DefaultResources.AsResources>)
- [type DeployDefaults](<#DeployDefaults>)
  - [func \(d DeployDefaults\) IsEmpty\(\) bool](<#DeployDefaults.IsEmpty>)
  - [func \(d DeployDefaults\) Validate\(prefix string\) error](<#DeployDefaults.Validate>)
- [type DigestRequirement](<#DigestRequirement>)
  - [func ParseDigestRequirement\(s string\) \(DigestRequirement, error\)](<#ParseDigestRequirement>)
  - [func \(r DigestRequirement\) Active\(\) bool](<#DigestRequirement.Active>)
  - [func \(r DigestRequirement\) CheckCurrentImage\(image string\) \(warning string, err error\)](<#DigestRequirement.CheckCurrentImage>)
  - [func \(r DigestRequirement\) CheckImage\(image string\) \(warning string, err error\)](<#DigestRequirement.CheckImage>)
- [type Execution](<#Execution>)
- [type ExecutionMode](<#ExecutionMode>)
- [type ExecutionPolicy](<#ExecutionPolicy>)
  - [func DenyAllExecutionPolicy\(\) \*ExecutionPolicy](<#DenyAllExecutionPolicy>)
  - [func ParseExecutionPolicy\(c ExecutionPolicyConfig\) \(\*ExecutionPolicy, error\)](<#ParseExecutionPolicy>)
  - [func \(p \*ExecutionPolicy\) CheckImage\(image string\) error](<#ExecutionPolicy.CheckImage>)
  - [func \(p \*ExecutionPolicy\) CheckPriorityClass\(pc string\) error](<#ExecutionPolicy.CheckPriorityClass>)
  - [func \(p \*ExecutionPolicy\) CheckServiceAccount\(sa string\) error](<#ExecutionPolicy.CheckServiceAccount>)
  - [func \(p \*ExecutionPolicy\) CheckSpec\(spec \*DAGSpec\) error](<#ExecutionPolicy.CheckSpec>)
  - [func \(p \*ExecutionPolicy\) CheckTask\(t TaskSpec\) error](<#ExecutionPolicy.CheckTask>)
- [type ExecutionPolicyConfig](<#ExecutionPolicyConfig>)
- [type HistoricalMetrics](<#HistoricalMetrics>)
- [type ImportError](<#ImportError>)
- [type LeoflowConfig](<#LeoflowConfig>)
  - [func \(c \*LeoflowConfig\) ApplyDefaults\(\)](<#LeoflowConfig.ApplyDefaults>)
  - [func \(c \*LeoflowConfig\) EffectiveDependencies\(\) \(\[\]string, error\)](<#LeoflowConfig.EffectiveDependencies>)
  - [func \(c \*LeoflowConfig\) Validate\(\) error](<#LeoflowConfig.Validate>)
  - [func \(c \*LeoflowConfig\) ValidateDockerfileValues\(\) error](<#LeoflowConfig.ValidateDockerfileValues>)
- [type PackageIndex](<#PackageIndex>)
  - [func \(p \*PackageIndex\) IndexURL\(lookup func\(string\) \(string, bool\)\) \(string, error\)](<#PackageIndex.IndexURL>)
- [type PageCursor](<#PageCursor>)
- [type ParamSpec](<#ParamSpec>)
- [type Pool](<#Pool>)
- [type PoolUsage](<#PoolUsage>)
- [type PythonDeprecation](<#PythonDeprecation>)
  - [func DeprecatedPythonVersion\(v string\) \(PythonDeprecation, bool\)](<#DeprecatedPythonVersion>)
  - [func RetiredPythonVersion\(v string\) \(PythonDeprecation, bool\)](<#RetiredPythonVersion>)
- [type RegistryConfig](<#RegistryConfig>)
- [type RemoteBuilderConfig](<#RemoteBuilderConfig>)
  - [func \(r \*RemoteBuilderConfig\) Validate\(prefix string\) error](<#RemoteBuilderConfig.Validate>)
- [type ResourceQuantity](<#ResourceQuantity>)
- [type ResourceUnit](<#ResourceUnit>)
  - [func ParseResourceUnit\(c ResourceUnitConfig\) \(\*ResourceUnit, error\)](<#ParseResourceUnit>)
  - [func \(u \*ResourceUnit\) Apply\(t TaskSpec\) \*Resources](<#ResourceUnit.Apply>)
  - [func \(u \*ResourceUnit\) Check\(t TaskSpec\) \(warned, refused error\)](<#ResourceUnit.Check>)
  - [func \(u \*ResourceUnit\) CheckSpec\(d \*DAGSpec\) \(warned \[\]error, refused error\)](<#ResourceUnit.CheckSpec>)
  - [func \(u \*ResourceUnit\) WarmEligible\(t TaskSpec\) bool](<#ResourceUnit.WarmEligible>)
  - [func \(u \*ResourceUnit\) WarmResources\(\) \*Resources](<#ResourceUnit.WarmResources>)
- [type ResourceUnitConfig](<#ResourceUnitConfig>)
- [type Resources](<#Resources>)
- [type SafeError](<#SafeError>)
  - [func \(e \*SafeError\) ClientMessage\(\) string](<#SafeError.ClientMessage>)
  - [func \(e \*SafeError\) Error\(\) string](<#SafeError.Error>)
  - [func \(e \*SafeError\) Unwrap\(\) error](<#SafeError.Unwrap>)
- [type StagingConfig](<#StagingConfig>)
- [type StagingVolumeState](<#StagingVolumeState>)
- [type SuspensionMode](<#SuspensionMode>)
  - [func \(m SuspensionMode\) HoldsDispatch\(\) bool](<#SuspensionMode.HoldsDispatch>)
- [type TaskConfig](<#TaskConfig>)
- [type TaskInstance](<#TaskInstance>)
- [type TaskInstanceFilter](<#TaskInstanceFilter>)
- [type TaskSpec](<#TaskSpec>)
  - [func \(t TaskSpec\) EffectivePoolSlots\(\) int](<#TaskSpec.EffectivePoolSlots>)
- [type TaskState](<#TaskState>)
  - [func \(s TaskState\) IsTerminal\(\) bool](<#TaskState.IsTerminal>)
- [type TaskType](<#TaskType>)
- [type TenantLimits](<#TenantLimits>)
- [type TenantLimitsUpdate](<#TenantLimitsUpdate>)
  - [func \(u TenantLimitsUpdate\) IsZero\(\) bool](<#TenantLimitsUpdate.IsZero>)
- [type TenantSuspension](<#TenantSuspension>)
  - [func \(s TenantSuspension\) Active\(\) bool](<#TenantSuspension.Active>)
  - [func \(s TenantSuspension\) RefusalError\(\) error](<#TenantSuspension.RefusalError>)
  - [func \(s TenantSuspension\) Validate\(\) error](<#TenantSuspension.Validate>)
- [type TriggerRule](<#TriggerRule>)
- [type UnitMisfitError](<#UnitMisfitError>)
  - [func \(e \*UnitMisfitError\) Error\(\) string](<#UnitMisfitError.Error>)
- [type UsageCursor](<#UsageCursor>)
  - [func \(c UsageCursor\) Before\(o UsageCursor\) bool](<#UsageCursor.Before>)
- [type UsageEvent](<#UsageEvent>)
- [type UsagePage](<#UsagePage>)
- [type User](<#User>)
- [type Variable](<#Variable>)
- [type VariablePatch](<#VariablePatch>)
- [type WarmPolicy](<#WarmPolicy>)
  - [func \(p WarmPolicy\) DedicatedReason\(v WarmVersion, t TaskSpec\) string](<#WarmPolicy.DedicatedReason>)
  - [func \(p WarmPolicy\) HasWarmTask\(tasks \[\]TaskSpec\) bool](<#WarmPolicy.HasWarmTask>)
  - [func \(p WarmPolicy\) VersionReason\(v WarmVersion\) string](<#WarmPolicy.VersionReason>)
- [type WarmPoolDefaults](<#WarmPoolDefaults>)
  - [func \(w \*WarmPoolDefaults\) EffectiveMinIdle\(dagMinIdle int\) int](<#WarmPoolDefaults.EffectiveMinIdle>)
- [type WarmVersion](<#WarmVersion>)
- [type XComEntryMeta](<#XComEntryMeta>)


## Constants

<a name="ExecutionPolicyAllFields"></a>Keywords of executor.defaults.execution\_allowlist.fields.

```go
const (
    // ExecutionPolicyAllFields allows every governed field (the default).
    ExecutionPolicyAllFields = "*"
    // ExecutionPolicyNoFields allows none of them. It exists because an empty
    // environment variable cannot express an empty list.
    ExecutionPolicyNoFields = "none"
)
```

<a name="UnitEnforceRefuse"></a>Enforcement modes of executor.unit.enforce \(ADR 0066 §3\).

```go
const (
    // UnitEnforceRefuse refuses a task that does not fit its size: registration
    // fails and a DAG registered earlier fails at dispatch. The default.
    UnitEnforceRefuse = "refuse"
    // UnitEnforceWarn accepts such a task and runs it with its own resources,
    // logging and counting it, so an operator can roll a unit out without
    // failing tasks first.
    UnitEnforceWarn = "warn"
)
```

<a name="ExecutionModeWarm"></a>The execution modes of a task attempt: on a warm worker of its DAG version \(ADR 0058\) or on a dedicated pod of its own \(ADR 0002\).

```go
const (
    ExecutionModeWarm      = "warm"
    ExecutionModeDedicated = "dedicated"
)
```

<a name="DedicatedWarmPoolsOff"></a>The reasons a whole DAG version runs on dedicated pods, whatever its tasks declare.

```go
const (
    // DedicatedWarmPoolsOff: execution.warm_pools_enabled is off, the default.
    DedicatedWarmPoolsOff = "warm pools off"
    // DedicatedStaging: a warm worker carries no per-run /staging volume
    // (ADR 0058 D5).
    DedicatedStaging = "staging enabled"
    // DedicatedSourceMode: a warm pod is built before its task is known, so
    // it cannot carry the task's dag.py in its pod spec (ADR 0067 §3), and
    // execution.source_mode.warm_pools, which delivers it with each
    // assignment instead (ADR 0095), is off.
    DedicatedSourceMode = "source mode"
)
```

<a name="DefaultBuildKitPodSelector"></a>DefaultBuildKitPodSelector selects the pod of the chart's builder when BuildKitConfig.PodSelector is empty.

```go
const DefaultBuildKitPodSelector = "app.kubernetes.io/component=builder"
```

<a name="DefaultPoolName"></a>DefaultPoolName is the implicit pool a task with no declared pool draws from, so the admission gate is always well\-defined. Matches Airflow's default\_pool.

```go
const DefaultPoolName = "default_pool"
```

<a name="DefaultUnitMaxSize"></a>DefaultUnitMaxSize is the largest pool\_slots a task may have while a unit is set and executor.unit.max\_size is not \(ADR 0066 §3\).

```go
const DefaultUnitMaxSize = 64
```

<a name="IndexSecretID"></a>IndexSecretID is the id of the build secret that carries the package index URL, credentials included, to a Dockerfile build: the generated Dockerfile mounts it for its pip install, and a project's own Dockerfile may mount it too. A project cannot declare a secret of its own under it.

```go
const IndexSecretID = "dexaflow_index_url"
```

<a name="MaxSourceModeBytes"></a>MaxSourceModeBytes caps the dag.py a source\-mode version carries \(ADR 0067 §3\). The executor ships the source in a pod annotation, and Kubernetes caps all of a pod's annotations together at 256 KiB, so half of that leaves room for the annotations the executor and operators already set. A constant, not a knob.

```go
const MaxSourceModeBytes = 128 << 10
```

<a name="MaxSuspensionReasonLength"></a>MaxSuspensionReasonLength bounds the reason an operator gives, in bytes. It is shown back in refusals and logs, so it stays short.

```go
const MaxSuspensionReasonLength = 500
```

<a name="ReservedMetadataPrefix"></a>ReservedMetadataPrefix is the label and annotation key prefix Dexaflow owns on the pods it creates. Components decide what a pod is from these keys \(a task attempt's identity, a warm worker and the pool it serves, the owning tenant\), so a DAG\-declared key under it would let a DAG change how its own pod is treated.

```go
const ReservedMetadataPrefix = "leoflow.io/"
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

<a name="ErrDatabaseReplaced"></a>ErrDatabaseReplaced is returned when the database is reachable but is not the one this release was installed on: it carries another installation id, none, or no schema at all \(ADR 0090 decision 3\). The health endpoints name it apart from ErrSchemaNotCurrent, because migrating does not fix it: an operator restores the right database, or accepts this one.

```go
var ErrDatabaseReplaced = errors.New("database replaced")
```

<a name="ErrInvalidBuildDockerfile"></a>ErrInvalidBuildDockerfile reports a build.dockerfile path outside the project.

```go
var ErrInvalidBuildDockerfile = errors.New("invalid build.dockerfile")
```

<a name="ErrInvalidDbtProject"></a>ErrInvalidDbtProject reports a dbt.project path the compiler cannot use.

```go
var ErrInvalidDbtProject = errors.New("invalid dbt.project")
```

<a name="ErrInvalidRunID"></a>ErrInvalidRunID reports a dag\_run\_id a caller may not use.

```go
var ErrInvalidRunID = errors.New("invalid run_id")
```

<a name="ErrLimitExceeded"></a>ErrLimitExceeded is returned when a write would take a tenant past one of the limits the operator set for it \(TenantLimits\). The API maps it to 403: the request is well formed, and repeating it will not succeed until the limit or the tenant's usage changes.

```go
var ErrLimitExceeded = errors.New("tenant limit exceeded")
```

<a name="ErrNotFound"></a>ErrNotFound is returned when a requested resource does not exist.

```go
var ErrNotFound = errors.New("resource not found")
```

<a name="ErrSchemaNotCurrent"></a>ErrSchemaNotCurrent is returned when the database is reachable but its migration state is not one the running binary can serve. It lives here, next to the other cross\-cutting sentinels, because two layers need to agree on it: storage produces the verdict, and the health endpoints must tell it apart from "the database could not be read at all". Both answers are a 503, but one sends an operator to the migration Job and the other to the connection pool, and a probe that conflates them costs an on\-call hour.

```go
var ErrSchemaNotCurrent = errors.New("database schema is not current")
```

<a name="ErrTenantSuspended"></a>ErrTenantSuspended is returned when a write would create work for a tenant the operator suspended through the service API. The API maps it to 409: the request is well formed, and it succeeds once the suspension is lifted.

```go
var ErrTenantSuspended = errors.New("tenant suspended")
```

<a name="ErrUnknownAlertPlaceholder"></a>ErrUnknownAlertPlaceholder reports an alert message template referencing a substitution Dexaflow does not perform.

```go
var ErrUnknownAlertPlaceholder = errors.New("unknown alert placeholder")
```

<a name="ErrValidation"></a>ErrValidation is returned when input fails a business\-rule check that the caller can fix \(e.g. creating a user with a role that does not exist\). The API maps it to 400.

```go
var ErrValidation = errors.New("invalid input")
```

<a name="ErrValueUnreadable"></a>ErrValueUnreadable is returned when a stored value is encrypted and no configured secret key opens it. It is a server configuration fault, not the caller's: the API answers 500, with a message that says what happened.

```go
var ErrValueUnreadable = errors.New("stored value cannot be decrypted")
```

<a name="ExecutionPolicyFields"></a>ExecutionPolicyFields are the task execution fields the operator allowlist \(executor.defaults.execution\_allowlist.fields\) governs, by their dag.json names. Each one changes where or how a task pod is placed, or how other cluster objects see it, so on a shared cluster it can affect other teams: a priority class jumps the preemption queue, a node selector, affinity or toleration reaches another team's dedicated nodes, a label can match another team's NetworkPolicy. service\_account and image\_pull\_policy are not on the list.

```go
var ExecutionPolicyFields = []string{
    "node_selector",
    "tolerations",
    "affinity",
    "topology_spread_constraints",
    "priority_class_name",
    "runtime_class_name",
    "resource_claims",
    "termination_grace_period_seconds",
    "labels",
    "annotations",
}
```

<a name="CheckCopySource"></a>
## func [CheckCopySource](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dockerfile_values.go#L88>)

```go
func CheckCopySource(field, src string) error
```

CheckCopySource refuses a COPY source path that no quoting can protect. A space or a tab is NOT refused: the renderer quotes those with the JSON form.

<a name="CheckDockerfileWord"></a>
## func [CheckDockerfileWord](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dockerfile_values.go#L46>)

```go
func CheckDockerfileWord(field, v string) error
```

CheckDockerfileWord refuses a value the Dockerfile format would split into new words or new instructions: a line break, a vertical tab or a form feed.

\#1066 gave \`dependencies\` and \`system\_packages\` a line\-break guard because a newline ends the RUN instruction. Every other value the generator interpolates has the same defect with no shell in sight: a newline in \`base\_image\` renders \`FROM python:3.11\-slim\` followed by an attacker's own instruction, and the COPY operands behave identically.

The value is named in the error because a stray newline in YAML is invisible in the source.

<a name="CheckImageReference"></a>
## func [CheckImageReference](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dockerfile_values.go#L69>)

```go
func CheckImageReference(field, v string) error
```

CheckImageReference refuses a value going into FROM, which has no quoting at all. Any whitespace there is read as the \`FROM \<image\> AS \<stage\>\` form, and an image reference cannot contain whitespace anyway, so refusing it costs nothing.

FROM's operand also goes through the same shell lexer as COPY's \(BuildKit's dispatchFrom calls shlex.ProcessWordWithMatches\), so the lexer's characters are refused here too \(\#1271\). Measured against Docker 29.8.2's builder: \`FROM busybox:1.36$SUFFIX\` with no ARG declared pulls busybox:1.36 behind an UndefinedArgInFrom warning, \`busybox:1.3'6'\` and \`busybox:1.3\\6\` pull busybox:1.36 with no warning at all, and a stray \`"\` fails the parse. No image reference can contain any of them, so refusing them costs nothing either, and a build that pulls a different image than base\_image names is the worst place for a silent rewrite: that image runs every task.

<a name="CheckRegistryPrefix"></a>
## func [CheckRegistryPrefix](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L186>)

```go
func CheckRegistryPrefix(field, v string) error
```

CheckRegistryPrefix refuses a registry prefix that cannot start an image reference: a scheme, credentials or a digest \(\`@\`\), a tag on its last path segment, capitals, a trailing slash or anything CheckImageReference refuses. A host alone, with or without a port, is valid. Empty is valid: no prefix is set.

<a name="CheckRunArgument"></a>
## func [CheckRunArgument](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dockerfile_values.go#L123>)

```go
func CheckRunArgument(field, w string) error
```

CheckRunArgument refuses a RUN argument no shell quoting survives: a line break ends the RUN instruction itself, and everything after it becomes a new Dockerfile line. Everything else is quoted by the renderer, deliberately, because PEP 508 markers legitimately carry \`;\`, \`\<\` and double quotes \(\#1064\).

<a name="CheckScheduleInterval"></a>
## func [CheckScheduleInterval](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_limits.go#L173>)

```go
func CheckScheduleInterval(spec DAGSpec, minSeconds int) error
```

CheckScheduleInterval refuses a schedule whose shortest gap between two runs is below minSeconds. A schedule that never fires on a cron passes. 0 is unlimited. Registration applies it with the tenant's min\_schedule\_interval\_seconds; pkg/dagspec exports it \(\#1877\).

<a name="CheckSourceMode"></a>
## func [CheckSourceMode](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/source_mode.go#L35>)

```go
func CheckSourceMode(modeImage string, spec *DAGSpec) error
```

CheckSourceMode refuses a version on the runtime image whose source is empty or over MaxSourceModeBytes while source mode is on. Any other version, and every version with the mode off, passes unchanged.

<a name="CheckSourceModeSize"></a>
## func [CheckSourceModeSize](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/source_mode.go#L49>)

```go
func CheckSourceModeSize(dagID, source string) error
```

CheckSourceModeSize refuses a source\-mode source over MaxSourceModeBytes. Register calls it through CheckSourceMode; dispatch calls it again for a version registered before source mode was turned on, which register never checked \(ADR 0067 §3\). Both give the same message.

<a name="CheckTaskPoolSlots"></a>
## func [CheckTaskPoolSlots](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_limits.go#L155>)

```go
func CheckTaskPoolSlots(spec DAGSpec, maxSlots int) error
```

CheckTaskPoolSlots refuses a DAG with a task larger than maxSlots, naming the task and both numbers \(ADR 0066 §5\). 0 is unlimited. Registration applies it with the tenant's max\_task\_pool\_slots; pkg/dagspec exports it \(\#1877\).

<a name="ClientMessageOr"></a>
## func [ClientMessageOr](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/safe_error.go#L68>)

```go
func ClientMessageOr(err error, fallback string) string
```

ClientMessageOr returns the phrase Dexaflow composed for err, or fallback when err carries no such phrase. It is the deny\-by\-default rule every client boundary applies: the HTTP API \(safeDetail\) and the agent gRPC service \(safeMessage\) both read it from here, so the rule has one copy \(\#1078\). Only a SafeError, an error someone deliberately wrote as client\-facing, contributes text; everything else, including every fmt.Errorf wrap of a driver error, is replaced, so a newly introduced storage error cannot leak by omission.

<a name="DAGSchemaJSON"></a>
## func [DAGSchemaJSON](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schema.go#L105>)

```go
func DAGSchemaJSON() []byte
```

DAGSchemaJSON returns a copy of the embedded dag.json JSON Schema that DAGSpec.Validate applies \(pkg/dagspec serves it, \#1877\).

<a name="IsCronlessSchedule"></a>
## func [IsCronlessSchedule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L19>)

```go
func IsCronlessSchedule(expr string) bool
```

IsCronlessSchedule reports whether expr is empty \(manual\-only\) or a recognized non\-cron Airflow schedule. Such a schedule is valid but is never run on a cron, so callers skip cron handling for it without treating it as an error.

<a name="IsDigestPinned"></a>
## func [IsDigestPinned](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/source_mode.go#L21>)

```go
func IsDigestPinned(image string) bool
```

IsDigestPinned reports whether image is pinned by a full sha256 digest: a name, then "@sha256:" and exactly 64 lowercase hex characters at the end. The source\-mode runtime image must be \(ADR 0067 §3\).

<a name="IsOnceSchedule"></a>
## func [IsOnceSchedule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L26>)

```go
func IsOnceSchedule(expr string) bool
```

IsOnceSchedule reports whether expr is Airflow's "@once" — a DAG that runs exactly one time \(on first scheduler sight\) and never again.

<a name="IsReservedMetadataKey"></a>
## func [IsReservedMetadataKey](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L300>)

```go
func IsReservedMetadataKey(key string) bool
```

IsReservedMetadataKey reports whether a label or annotation key falls under ReservedMetadataPrefix and therefore may only be set by Dexaflow itself.

<a name="MinScheduleInterval"></a>
## func [MinScheduleInterval](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_limits.go#L69>)

```go
func MinScheduleInterval(expr string) (time.Duration, bool)
```

MinScheduleInterval returns the shortest gap between two consecutive fire times of a DAG schedule: a 5\-field cron, a preset such as @hourly, or "@every \<duration\>". ok is false when the schedule never fires on a cron \(empty, @once, @continuous, a date that does not exist such as 31 February\) or does not parse.

For a cron it is exact over the wall clock. The minutes and hours a cron fires at are the same on every day it fires, so the gaps inside a day are fixed; the only other gaps run from the last time of one firing day to the first time of the next, which is shortest across the closest two firing days. Those days come from a scan of one 28\-year calendar cycle that walks firing days only, so even "\* \* \* \* \*" costs a few thousand steps at most. The scan is in UTC, so the gaps are wall\-clock gaps. With CRON\_TZ the two days a year the clocks change differ: a gap that spans the change is an hour shorter or longer, a time inside the repeated hour fires twice, one hour apart, and a time inside the skipped hour does not fire that day.

<a name="NewTenantSchemaCompiler"></a>
## func [NewTenantSchemaCompiler](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schema_loader.go#L35>)

```go
func NewTenantSchemaCompiler() *jsonschema.Compiler
```

NewTenantSchemaCompiler returns a JSON Schema compiler for a schema a tenant wrote, such as a DAG param schema. Add the schema under TenantSchemaURL so errors never name a local path. It resolves references into the schema itself \(its "$defs"\) and the standard draft meta\-schemas the library ships, and refuses every other "$ref": no local file and no remote URL is loaded.

<a name="ParseCron"></a>
## func [ParseCron](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L62>)

```go
func ParseCron(expr string) (cron.Schedule, error)
```

ParseCron parses a schedule with robfig/cron's ParseStandard. Every schedule parse in Dexaflow goes through it, so registration and the scheduler accept exactly the same expressions.

It first refuses a TZ= or CRON\_TZ= prefix with no space after it. ParseStandard \(robfig/cron v3.0.1\) cuts the zone name at the first space and panics with a slice out of range when there is none, so a schedule such as "TZ=UTC" in a dag.json would otherwise panic at registration and on every scheduler tick.

<a name="Safef"></a>
## func [Safef](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/safe_error.go#L56>)

```go
func Safef(class error, format string, a ...any) error
```

Safef builds a SafeError in class with the formatted message. The message is shown to API clients verbatim, so it must contain only facts the caller supplied or Dexaflow chose, never a driver, filesystem, or network error.

<a name="SourceModeApplies"></a>
## func [SourceModeApplies](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/source_mode.go#L28>)

```go
func SourceModeApplies(modeImage, image string, hasSource bool) bool
```

SourceModeApplies reports whether a version runs in source mode \(ADR 0067 §3\): the mode is on \(modeImage, the operator's runtime image, is set\), the version's image is that runtime image, and it carries a source.

<a name="SupportedPythonVersions"></a>
## func [SupportedPythonVersions](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/python.go#L124>)

```go
func SupportedPythonVersions() ([]string, error)
```

SupportedPythonVersions returns the python\_version values the project publishes a task base image for, in schema order \(ascending\). The slice is a copy: callers may sort or filter it.

This is the one list. scripts/check\-python\-runtime\-matrix.sh asserts that the release matrix, the Makefile target, the runtime Dockerfile and the configuration reference all agree with it, because a published image nobody can select, and a selectable version nobody publishes, both surface as a \`docker pull\` 404 inside the user's own build.

<a name="TenantSchemaURL"></a>
## func [TenantSchemaURL](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schema_loader.go#L28>)

```go
func TenantSchemaURL(name string) string
```

TenantSchemaURL is the location a tenant schema is added to its compiler under. A bare name such as "param.json" is resolved against the control plane's working directory, so every compile and validation error that names the schema, or a relative "$ref" inside it, would reveal that directory to the tenant. Under this non\-file base a relative "$ref" resolves to another dexaflow:// URL, which noRemoteRefs refuses like any other.

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
## type [AlertRule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L85-L94>)

AlertRule is one channel to notify on an alert event. The endpoint and its secret always come from a managed connection \(Conn\), never a literal URL or token in dexaflow.yaml: that keeps credentials out of the compiled dag.json and mirrors the env\-ref secret discipline.

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
## type [AlertsConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L76-L79>)

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
## type [BuildConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L144-L163>)

BuildConfig controls how the container image is built from the project.

```go
type BuildConfig struct {
    Dockerfile string            `json:"dockerfile,omitempty" yaml:"dockerfile,omitempty"`
    Context    string            `json:"context,omitempty" yaml:"context,omitempty"`
    Platforms  []string          `json:"platforms,omitempty" yaml:"platforms,omitempty"`
    Labels     map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
    // RemoteBuilder is the remote builder for builds no daemonless path
    // covers (ADR 0083); nil leaves those to the target's or workspace's
    // remote_builder, then the local daemon.
    RemoteBuilder *RemoteBuilderConfig `json:"remote_builder,omitempty" yaml:"remote_builder,omitempty"`
    // Args are the build arguments (`--build-arg`) the project's own
    // Dockerfile reads with ARG. Plain values: they stay in the image
    // history, so a secret goes in Secrets.
    Args map[string]string `json:"args,omitempty" yaml:"args,omitempty"`
    // Secrets are the build secrets a RUN line mounts with
    // --mount=type=secret,id=<id>, read from the environment of the build.
    Secrets []BuildSecret `json:"secrets,omitempty" yaml:"secrets,omitempty"`
    // PackageIndex is the Python package index pip and uv install from in
    // place of PyPI; nil is PyPI.
    PackageIndex *PackageIndex `json:"package_index,omitempty" yaml:"package_index,omitempty"`
}
```

<a name="BuildConfig.LocalOnly"></a>
### func \(\*BuildConfig\) [LocalOnly](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_inputs.go#L166>)

```go
func (b *BuildConfig) LocalOnly() string
```

LocalOnly says why a build of this project must run on this machine, or "" when any builder may run it: build secrets and the package index reach the build from this machine's environment, and a remote builder would have to store them in its own build record.

<a name="BuildInfo"></a>
## type [BuildInfo](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_info.go#L24-L31>)

BuildInfo is where a DAG version was built \(\#1702\): the git commit and repository of the sources, the CI pipeline run, the builder that produced the image, the base image it was built from and the version of the CLI that built and registered it. The base image carries the task agent, so it also names the agent version. Every field is optional.

It is metadata of the register, not part of dag.json: the same sources built by another pipeline compile to the same dag.json, the same content hash and the same version label, and the first register of that content keeps its build info. It is the caller's claim, recorded as sent and not verified.

```go
type BuildInfo struct {
    Commit      string `json:"commit,omitempty"`
    Repo        string `json:"repo,omitempty"`
    PipelineURL string `json:"pipeline_url,omitempty"`
    Builder     string `json:"builder,omitempty"`
    BaseImage   string `json:"base_image,omitempty"`
    CLIVersion  string `json:"cli_version,omitempty"`
}
```

<a name="BuildInfo.IsZero"></a>
### func \(BuildInfo\) [IsZero](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_info.go#L65>)

```go
func (b BuildInfo) IsZero() bool
```

IsZero reports whether b holds no field.

<a name="BuildInfo.Sanitized"></a>
### func \(BuildInfo\) [Sanitized](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_info.go#L90>)

```go
func (b BuildInfo) Sanitized() BuildInfo
```

Sanitized returns b without the fields Validate would refuse, so a client sends only what a server accepts.

<a name="BuildInfo.Validate"></a>
### func \(BuildInfo\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_info.go#L76>)

```go
func (b BuildInfo) Validate() error
```

Validate returns the first field of b that breaks its rule, named in the error: commit is a lower case hex git object id of 7 to 64 characters; repo and pipeline\_url are absolute http or https URLs without credentials, query string or fragment, at most 2048 bytes; every field is valid UTF\-8; builder \(at most 255 bytes\), base\_image \(512\) and cli\_version \(128\) hold no control or format character, and base\_image and cli\_version no space either. An empty field is always valid.

<a name="BuildKitConfig"></a>
## type [BuildKitConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/remote_builder.go#L29-L40>)

BuildKitConfig is the in\-cluster BuildKit backend of the remote builder. The CLI finds a ready pod of the daemon with kubectl and builds on it with \`buildctl \-\-addr kube\-pod://\<pod\>\`, over kubectl exec, as the person who deploys: the Role the chart binds to builder.subjects decides who may.

```go
type BuildKitConfig struct {
    // Namespace is the namespace of the builder, the chart's
    // builder.namespace. Required.
    Namespace string `json:"namespace,omitempty" yaml:"namespace,omitempty"`
    // PodSelector is the label selector of the builder pod, in equality form
    // (key=value, comma separated); DefaultBuildKitPodSelector when empty.
    PodSelector string `json:"pod_selector,omitempty" yaml:"pod_selector,omitempty"`
    // Context is the kubeconfig context that reaches the builder's cluster;
    // kubectl's current context when empty. Only a local file sets it: a
    // server cannot know the names of a client's contexts.
    Context string `json:"context,omitempty" yaml:"context,omitempty"`
}
```

<a name="BuildKitConfig.Selector"></a>
### func \(BuildKitConfig\) [Selector](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/remote_builder.go#L43>)

```go
func (b BuildKitConfig) Selector() string
```

Selector returns the label selector of the builder pod.

<a name="BuildSecret"></a>
## type [BuildSecret](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_inputs.go#L22-L27>)

BuildSecret is one build secret: a Dockerfile mounts it with \`RUN \-\-mount=type=secret,id=\<ID\>\`, and the build reads its value from the environment variable Env of the machine that builds. The value never reaches dexaflow.yaml, dag.json, the image or the build's command line.

```go
type BuildSecret struct {
    // ID is the id a RUN line mounts the secret under.
    ID  string `json:"id" yaml:"id"`
    // Env is the environment variable that holds the value.
    Env string `json:"env" yaml:"env"`
}
```

<a name="CIOIDCDefaults"></a>
## type [CIOIDCDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L54-L57>)

CIOIDCDefaults are the CI OIDC issuers a deployDefaults answer lists \(ADR 0092\).

```go
type CIOIDCDefaults struct {
    // Issuers are the entries of the caller's tenant, in configuration order.
    Issuers []CIOIDCIssuer `json:"issuers"`
}
```

<a name="CIOIDCIssuer"></a>
## type [CIOIDCIssuer](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L62-L71>)

CIOIDCIssuer is one CI OIDC issuer as \`dexaflow ci init\` needs it: which provider it is and the audience a pipeline asks its token for. It carries no rule and no tenant.

```go
type CIOIDCIssuer struct {
    // Name is the entry's name in the server configuration.
    Name string `json:"name"`
    // Provider is github, gitlab, cloudbuild or other.
    Provider string `json:"provider"`
    // Issuer is the exact iss of the provider's tokens.
    Issuer string `json:"issuer"`
    // Audience is the aud the pipeline asks its token for.
    Audience string `json:"audience"`
}
```

<a name="ClearOptions"></a>
## type [ClearOptions](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/clear.go#L18-L21>)

ClearOptions carries the two independent decisions a clear makes about the run it belongs to.

ResetDagRun re\-opens the run so the scheduler looks at it again. Without it a terminal run stays terminal, and the cleared task instance sits at \`none\` in a run nothing schedules.

RunOnLatestVersion decides WHICH version the re\-run executes: the DAG's current one, or the one the run was pinned to when it was created.

These were a single boolean. Folding the version decision into the re\-open meant there was no way to re\-run a task against the image that produced it, "clear last week's task" always executed today's code, and no way to re\-open a run without also moving it forward a version. Apache Airflow separates them the same way, calling the second \`run\_on\_latest\_version\`.

```go
type ClearOptions struct {
    ResetDagRun        bool
    RunOnLatestVersion bool
}
```

<a name="ClearVersionKept"></a>
## type [ClearVersionKept](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/clear.go#L27-L30>)

ClearVersionKept names a run that a clear asked to move to the DAG's current version \(RunOnLatestVersion\) and that keeps the version it is bound to, with the reason \(ADR 0086\): only a finished run with no queued or running task instance moves.

```go
type ClearVersionKept struct {
    RunID  string
    Reason string
}
```

<a name="CloudBuildConfig"></a>
## type [CloudBuildConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/remote_builder.go#L52-L64>)

CloudBuildConfig is the Cloud Build backend of the remote builder. Every value reaches gcloud as a \-\-flag=value argument.

```go
type CloudBuildConfig struct {
    // Project is the Google Cloud project the build runs in. Required.
    Project string `json:"project,omitempty" yaml:"project,omitempty"`
    // Region is the Cloud Build region, such as us-central1; gcloud's
    // builds/region, else global, when empty.
    Region string `json:"region,omitempty" yaml:"region,omitempty"`
    // WorkerPool is a private pool,
    // projects/<project>/locations/<region>/workerPools/<name>.
    WorkerPool string `json:"worker_pool,omitempty" yaml:"worker_pool,omitempty"`
    // ServiceAccount is the account the build runs as,
    // projects/<project>/serviceAccounts/<email>.
    ServiceAccount string `json:"service_account,omitempty" yaml:"service_account,omitempty"`
}
```

<a name="ConfigDefaults"></a>
## type [ConfigDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L178-L191>)

ConfigDefaults holds task defaults applied to every task generated from the project at compile time.

```go
type ConfigDefaults struct {
    Retries                 int `json:"retries,omitempty" yaml:"retries,omitempty"`
    RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty" yaml:"retry_delay_seconds,omitempty"`
    ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty" yaml:"execution_timeout_seconds,omitempty"`
    // Size is the default pool_slots of every task that sets none, in dag.py or
    // in tasks.<id>.size (ADR 0066).
    Size      *int              `json:"size,omitempty" yaml:"size,omitempty"`
    Resources *DefaultResources `json:"resources,omitempty" yaml:"resources,omitempty"`
    // NodeSelector is the DAG-wide pod placement fallback applied to every task
    // that declares no execution.node_selector of its own. Like Resources it is a
    // default, so the most-specific per-task value always wins. Consumed at
    // compile time by the overlay, which bakes it onto each task in dag.json.
    NodeSelector map[string]string `json:"node_selector,omitempty" yaml:"node_selector,omitempty"`
}
```

<a name="Connection"></a>
## type [Connection](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/connection.go#L6-L21>)

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
    // ExtraUnreadable reports a stored extra that no configured secret key
    // opens (the key was rotated without keeping the old one, for example).
    // Extra is empty then. Only listings set it; reading the connection alone
    // is an ErrValueUnreadable error (#1677).
    ExtraUnreadable bool
}
```

<a name="ParseConnectionURI"></a>
### func [ParseConnectionURI](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/conn_uri_parse.go#L35>)

```go
func ParseConnectionURI(connID, uri string) (Connection, error)
```

ParseConnectionURI reads an Airflow connection URI, the AIRFLOW\_CONN\_\<ID\> form a task consumes, back into a Connection.

It is the inverse of the URI the control plane delivers to tasks, and it follows Airflow's own from\_uri semantics wherever the two could differ, because the value being parsed is one the user wrote for Airflow to read:

- the scheme carries \`\-\` where the conn\_type has \`\_\` \(google\_cloud\_platform and friends are not legal URI schemes\), so it is reversed here exactly as the emitter applies it;
- userinfo is percent\-decoded into login and password;
- the path becomes the schema with ONE leading slash removed, which is what Airflow does; note this is lossy for an absolute path like sqlite's \`sqlite:///tmp/db\`, whose schema comes back as \`tmp/db\`. Matching Airflow is the right loss to take: the task resolves the connection through Airflow, so Airflow's reading is the meaning;
- \`\_\_extra\_\_\` carries the JSON blob, as the emitter writes it.

No error returned here ever contains the URI: the value is a credential, and an error is the most likely thing to be logged, wrapped or printed. Errors name the connection and the problem.

An empty conn\_type is refused. A connection with no type cannot be delivered to a task in a form Airflow will accept, so storing one would replace a missing connection with a broken one.

<a name="ConnectionPatch"></a>
## type [ConnectionPatch](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/connection.go#L36-L46>)

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

<a name="CurrentVersionChange"></a>
## type [CurrentVersionChange](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L124-L128>)

CurrentVersionChange is the outcome of making a version current: the version that was current before \(From\), the one that is current now \(To\), and whether anything changed \(false when To was already current\).

```go
type CurrentVersionChange struct {
    From    DagVersion
    To      DagVersion
    Changed bool
}
```

<a name="DAG"></a>
## type [DAG](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L10-L28>)

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
    // CurrentVersion is the LABEL of the DAG's current registered version, empty
    // when it cannot be resolved. Paired with DagRun.Version by the UI's clear
    // dialog, which offers "Run with latest bundle version" only when the two
    // differ; see that field.
    CurrentVersion string
}
```

<a name="DAGSpec"></a>
## type [DAGSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L67-L133>)

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
    // MinIdleWorkers is the number of idle warm workers the author asks to keep
    // for this DAG version so its tasks skip the cold pod start (ADR 0058, warm
    // pools model A2). It comes from min_idle_workers in dexaflow.yaml, which
    // the compiler overlays onto dag.json. The operator gates whether warmth
    // happens at all (execution.warm_pools_enabled) and caps it
    // (config.ExecutionSection.EffectiveMinIdle clamps it to max_pool_size);
    // whether a pod may be reused across attempts stays the operator's security
    // decision. A version whose DAG declares it is prewarmed when it becomes
    // current (ADR 0082). Zero, the default, declares none.
    MinIdleWorkers int  `json:"min_idle_workers,omitempty"`
    Catchup        bool `json:"catchup,omitempty"`
    // IsPausedUponCreation is Airflow's is_paused_upon_creation DAG argument:
    // whether the DAG starts paused when it is registered for the first time.
    // It overrides the server's dags_are_paused_at_creation; nil leaves the
    // decision to that setting. It is read only on creation: a later version
    // never changes whether the DAG is paused (#1705).
    IsPausedUponCreation *bool        `json:"is_paused_upon_creation,omitempty"`
    DefaultArgs          *DefaultArgs `json:"default_args,omitempty"`
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
```

<a name="DAGSpec.CanonicalHash"></a>
### func \(\*DAGSpec\) [CanonicalHash](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/hash.go#L13>)

```go
func (d *DAGSpec) CanonicalHash() (string, error)
```

CanonicalHash returns the SHA\-256 of the spec's canonical JSON encoding. Go's struct marshaling is deterministic \(fixed field order, sorted map keys\), so identical specs hash identically — used to deduplicate DAG versions.

<a name="DAGSpec.RunParams"></a>
### func \(DAGSpec\) [RunParams](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run_params.go#L18>)

```go
func (d DAGSpec) RunParams(task TaskSpec, conf json.RawMessage) (json.RawMessage, error)
```

RunParams returns the params a task of this DAG runs with, as the JSON object the agent delivers in LEOFLOW\_PARAMS. It layers them the way Airflow does with dag\_run\_conf\_overrides\_params on: the DAG's declared param defaults, then the task's own params \(operator\_args.params for an operator, the task's params field for a bash or python task\), then the run conf, each layer winning per key over the one before. A run created without conf, such as a scheduled run, still gets the DAG defaults and task params \(\#1841, \#1844\).

When neither the DAG nor the task declares params, conf is returned unchanged. A conf that is not a JSON object \(or null\) is an error.

<a name="DAGSpec.Validate"></a>
### func \(\*DAGSpec\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L315>)

```go
func (d *DAGSpec) Validate() error
```

Validate checks the DAGSpec against the canonical dag.json schema and returns a joined error describing every schema violation, or nil when valid.

<a name="DAGSpec.ValidateSchedule"></a>
### func \(\*DAGSpec\) [ValidateSchedule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/schedule.go#L38>)

```go
func (d *DAGSpec) ValidateSchedule() error
```

ValidateSchedule checks that a DAG's cron schedule is parseable. An empty or absent schedule \(manual\-only\) and the recognized non\-cron Airflow schedules \(@once, @continuous\) are valid. A malformed cron expression, a 4\-field cron, a typo, is rejected here so it fails loudly at compile time; otherwise the scheduler silently can't parse it and the DAG simply never runs, with no error surfaced anywhere \(the worst failure mode\). The parser is robfig/cron's ParseStandard \(through ParseCron\), the same one the scheduler uses, so what validates here is exactly what the scheduler can run \(see scheduler/cron.go\).

<a name="DagFilter"></a>
## type [DagFilter](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L31-L46>)

DagFilter specifies filtering, searching, and sorting options for listing DAGs.

```go
type DagFilter struct {
    DagIDPattern                string
    DagIDPrefixPattern          string
    DagDisplayNamePattern       string
    DagDisplayNamePrefixPattern string
    Paused                      *bool
    LastDagRunState             string
    Tags                        []string
    TagsMatchMode               string // "any" or "all"
    Owners                      []string
    IsFavorite                  *bool
    UserID                      string
    OrderBy                     string
    Limit                       int
    Offset                      int
}
```

<a name="DagPromotion"></a>
## type [DagPromotion](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L161-L164>)

DagPromotion names where a promoted version came from: the source target's name as the promoting CLI knows it, and the source control plane's URL \(empty when not sent\).

```go
type DagPromotion struct {
    Target string
    Server string
}
```

<a name="DagRegistered"></a>
## type [DagRegistered](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L171-L175>)

DagRegistered is the outcome of a register: whether a new version was created \(false when the content was already registered\), whether the register created the DAG itself \(its first version\), and the DAG's is\_paused afterwards. For a dry run each field is what the register would have done.

```go
type DagRegistered struct {
    Created    bool
    DagCreated bool
    IsPaused   bool
}
```

<a name="DagRegistration"></a>
## type [DagRegistration](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L147-L156>)

DagRegistration is one register of a compiled DAG version: the spec, its canonical hash, the registering user \(empty when none\) and an optional is\_paused for the DAG. IsPaused nil keeps Airflow's rule: a DAG the register creates starts paused per its is\_paused\_upon\_creation or the server's dags\_are\_paused\_at\_creation, and an existing DAG keeps its state. IsPaused set is the DAG's state after the register, created or not. DryRun runs every check and step of the register and then undoes it, so the outcome says what the register would do and nothing is written, audit included. PromotedFrom, when set, names the control plane the version was promoted from \(\`dexaflow promote\`\), recorded in the register's audit entry. BuildInfo, when set, is where the version was built, stored with the version the register creates and recorded in its audit entry; a register that creates no version \(the content is already registered\) changes no build info. RequireDigest is the deploy.require\_digest the register was admitted under \(\#1703\); while it is warn or enforce, the audit entry records it and whether the image is pinned by digest.

```go
type DagRegistration struct {
    Spec          DAGSpec
    SpecHash      string
    ActorUserID   string
    IsPaused      *bool
    DryRun        bool
    PromotedFrom  *DagPromotion
    BuildInfo     *BuildInfo
    RequireDigest DigestRequirement
}
```

<a name="DagRun"></a>
## type [DagRun](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L178-L200>)

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
    // Version is the LABEL of the dag_version this run is pinned to (the
    // `dag_version` from its dag.json), empty when it cannot be resolved.
    //
    // It exists for the UI: the SPA's clear dialog renders its "Run with latest
    // bundle version" control only when the run's bundle_version differs from the
    // DAG's, and hides it when either is null. With no label the operator cannot
    // ask for the current version at all, whatever the API accepts.
    Version string
}
```

<a name="DagRunFilter"></a>
## type [DagRunFilter](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L49-L60>)

DagRunFilter specifies filtering options for listing DAG runs.

```go
type DagRunFilter struct {
    RunIDPattern       string
    RunIDPrefixPattern string
    RunType            string
    States             []string
    StartDateGte       *time.Time
    StartDateLte       *time.Time
    EndDateGte         *time.Time
    EndDateLte         *time.Time
    Limit              int
    Offset             int
}
```

<a name="DagRunState"></a>
## type [DagRunState](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L52>)

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
### func \(DagRunState\) [IsTerminal](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L79>)

```go
func (s DagRunState) IsTerminal() bool
```

IsTerminal reports whether the dag run state is final.

<a name="DagRunState.Valid"></a>
### func \(DagRunState\) [Valid](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L69>)

```go
func (s DagRunState) Valid() bool
```

Valid reports whether s is one of the dag\_run\_state values. A raw value the database cannot cast must be refused before the query, as the client's mistake, not a server fault \(\#2202\).

<a name="DagSetCurrent"></a>
## type [DagSetCurrent](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L113-L119>)

DagSetCurrent is one request to make a registered version of DagID current again \(\#1702\): the version Selector names, on behalf of ActorUserID \(empty when none\), after Admit accepts its spec. RequireDigest is the deploy.require\_digest it was admitted under \(\#1703\); while it is warn or enforce, the audit entry records it and whether the image is pinned.

```go
type DagSetCurrent struct {
    DagID         string
    Selector      DagVersionSelector
    ActorUserID   string
    Admit         func(DAGSpec) error
    RequireDigest DigestRequirement
}
```

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
## type [DagVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L74-L99>)

DagVersion is a registered version of a DAG. VersionNumber is the 1\-based ordinal the UI uses \(the stored version label is free\-form\).

```go
type DagVersion struct {
    ID            string
    VersionNumber int
    CreatedAt     time.Time
    // Version is the deployment label that produced this snapshot: by default a
    // git describe (tag/SHA) plus a content hash in production, or
    // "dev-<timestamp>" in Lite. It is the stable
    // per-deployment identifier under a stable dag_id.
    Version string
    // ImageReference is the image the version runs, pinned by digest when the
    // deploy resolved one.
    ImageReference string
    // SpecHash is the canonical hash of the version's dag.json.
    SpecHash string
    // IsCurrent reports whether this is the DAG's current version: the one new
    // runs are created with.
    IsCurrent bool
    // CreatedBy is the id of the user who registered the version, or "" when
    // the register had no user of the tenant (Lite's dev principal, a direct
    // call, a deleted user). It is an id, not an email: every reader of the
    // DAG sees it.
    CreatedBy string
    // BuildInfo is where the version was built, as its register sent it
    // (#1702); nil when it sent none.
    BuildInfo *BuildInfo
}
```

<a name="DagVersionSelector"></a>
## type [DagVersionSelector](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L103-L106>)

DagVersionSelector names one registered version of a DAG, by its label \(Version\) or by its version\_number \(Number\). Exactly one is set.

```go
type DagVersionSelector struct {
    Version string
    Number  int
}
```

<a name="DbtConfig"></a>
## type [DbtConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L99-L120>)

DbtConfig declares a dbt project as the DAG source \(ADR 0042\). The compiler reads the project's manifest.json and renders one task per dbt node \(or per group\), so a dbt project becomes a Dexaflow DAG with no Cosmos or Airflow.

```go
type DbtConfig struct {
    // Project is the directory containing dbt_project.yml.
    Project string `json:"project,omitempty" yaml:"project,omitempty"`
    // Granularity is the task partition strategy: node, level or folder (ADR 0042
    // §5). Empty means node. Tag and selector grouping are not implemented: the
    // schema enum is the three above; see #398.
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
## type [DefaultArgs](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L158-L162>)

DefaultArgs holds retry and timeout defaults applied to every task in a DAG.

```go
type DefaultArgs struct {
    Retries                 int `json:"retries,omitempty"`
    RetryDelaySeconds       int `json:"retry_delay_seconds,omitempty"`
    ExecutionTimeoutSeconds int `json:"execution_timeout_seconds,omitempty"`
}
```

<a name="DefaultLimits"></a>
## type [DefaultLimits](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/default_limits.go#L16-L19>)

DefaultLimits is the operator's default task limit \(executor.defaults.limits\_cpu and limits\_memory, \#537\): the CPU and memory limit a task pod gets in a dimension where its resources set none. It closes the gap the L0 resource default \(ADR 0023\) leaves open: that default applies only to a task that declares no resources at all, so a task declaring requests alone, or any task on a cluster without the default, runs with no limit. A nil \*DefaultLimits means none is configured and Apply is a no\-op.

```go
type DefaultLimits struct {
    // contains filtered or unexported fields
}
```

<a name="ParseDefaultLimits"></a>
### func [ParseDefaultLimits](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/default_limits.go#L24>)

```go
func ParseDefaultLimits(cpu, memory string) (*DefaultLimits, error)
```

ParseDefaultLimits parses the configured default limits. Both empty means none \(nil, nil\). Either may be set alone; a set value must be a valid, positive Kubernetes quantity.

<a name="DefaultLimits.Apply"></a>
### func \(\*DefaultLimits\) [Apply](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/default_limits.go#L49>)

```go
func (l *DefaultLimits) Apply(r Resources) Resources
```

Apply returns r with every empty CPU or memory limit filled from the defaults. A declared limit is never changed, and requests are left as they are: where a request is empty Kubernetes sets it to the limit, as it does for a LimitRange default. A filled limit is raised to the request of the same dimension when that is larger, because the API server rejects a request above its limit. r is not modified.

<a name="DefaultLimits.WarmEligible"></a>
### func \(\*DefaultLimits\) [WarmEligible](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/default_limits.go#L94>)

```go
func (l *DefaultLimits) WarmEligible(t TaskSpec) bool
```

WarmEligible reports whether a task fits a warm pod sized by WarmResources: in every dimension with a default limit, the limit the task would get on a dedicated pod \(its declared limit, or the default raised to its request\) is not above the default. A larger task takes a dedicated pod, where it keeps that limit, instead of running under the warm pod's smaller one. Without default limits every task is eligible, as before.

<a name="DefaultLimits.WarmResources"></a>
### func \(\*DefaultLimits\) [WarmResources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/default_limits.go#L80>)

```go
func (l *DefaultLimits) WarmResources() *Resources
```

WarmResources is what a warm pod \(ADR 0058\) gets from the default limits when no executor.unit is set: the limits of a task that declares nothing, with Kubernetes setting the requests to them. A warm pod is created before any task is known, so this is the only way the default limits reach a task placed on a warm worker. Nil without default limits, leaving warm pods unsized as before.

<a name="DefaultResources"></a>
## type [DefaultResources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L194-L197>)

DefaultResources expresses default CPU and memory for generated tasks.

```go
type DefaultResources struct {
    CPU    string `json:"cpu,omitempty" yaml:"cpu,omitempty"`
    Memory string `json:"memory,omitempty" yaml:"memory,omitempty"`
}
```

<a name="DefaultResources.AsResources"></a>
### func \(\*DefaultResources\) [AsResources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L209>)

```go
func (d *DefaultResources) AsResources() *Resources
```

AsResources expands the simplified default cpu/memory into a full Resources with requests == limits \(the QoS story of \#725\). This mirrors how the per\-cluster platform default is built at dispatch. Returns nil when the receiver is nil or declares no quantity, so callers can treat a missing default as "leave the task untouched".

Guaranteed QoS is reached only when BOTH cpu and memory are set \(and thus equal across requests and limits\). A partial default — only cpu, or only memory — leaves the other dimension unset, so Kubernetes classifies the pod as Burstable, not Guaranteed.

<a name="DeployDefaults"></a>
## type [DeployDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L16-L50>)

DeployDefaults are the build and push settings a cluster advertises to \`dexaflow deploy\` \(\#1704\): what a project, its workspace and its target leave unset is taken from here, so a platform team states its registry once instead of in every repository. The task service account and the warm pools \(\#1890\) are only reported by a deploy. The operator sets them in the server's deploy.defaults; GET /api/v2/deployDefaults serves them. None is a secret: registry credentials never go here.

```go
type DeployDefaults struct {
    // Registry is the image prefix a project without registry.url pushes
    // to, such as ghcr.io/acme. The image name defaults to the dag_id.
    Registry string `json:"registry" mapstructure:"registry"`
    // Platforms replaces build.platforms for projects that set none.
    Platforms []string `json:"platforms" mapstructure:"platforms"`
    // BaseImage replaces base_image for projects that set none.
    BaseImage string `json:"base_image" mapstructure:"base_image"`
    // SourceModeImage is the runtime image a deploy registers a DAG's
    // dag.py on, with no build, when nothing local names one (source mode,
    // ADR 0067, #1772). The server fills it from execution.source_mode.image
    // when deploy.defaults.source_mode is on; it is never set by hand, so
    // it cannot drift from the image the server runs.
    SourceModeImage string `json:"source_mode_image" mapstructure:"-"`
    // RemoteBuilder replaces build.remote_builder for projects, targets and
    // workspaces that set none (ADR 0083, #1890); nil advertises none. The
    // server fills it from deploy.defaults.remote_builder.
    RemoteBuilder *RemoteBuilderConfig `json:"remote_builder" mapstructure:"-"`
    // TaskServiceAccount is the ServiceAccount task pods run as when a task
    // sets no execution.service_account (#1890). The server fills it from
    // executor.task_service_account when deploy.defaults.task_service_account
    // is on, so it cannot drift from the account pods get; a deploy reports
    // it and never writes it into dag.json.
    TaskServiceAccount string `json:"task_service_account" mapstructure:"-"`
    // WarmPools is what the cluster does with a DAG's min_idle_workers
    // (ADR 0058, ADR 0082, #1890); nil advertises nothing. The server fills
    // it from execution.* when deploy.defaults.warm_pools is on.
    WarmPools *WarmPoolDefaults `json:"warm_pools" mapstructure:"-"`
    // CIOIDC lists the CI OIDC issuers of the caller's tenant (ADR 0092),
    // so `dexaflow ci init` writes a pipeline that deploys without a stored
    // token; nil when the tenant has none. Unlike the fields above it
    // depends on the caller: the server fills it per request, and it is no
    // deploy setting, so IsEmpty and Validate ignore it.
    CIOIDC *CIOIDCDefaults `json:"ci_oidc" mapstructure:"-"`
}
```

<a name="DeployDefaults.IsEmpty"></a>
### func \(DeployDefaults\) [IsEmpty](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L102>)

```go
func (d DeployDefaults) IsEmpty() bool
```

IsEmpty reports whether d sets no deploy setting \(CIOIDC is not one\).

<a name="DeployDefaults.Validate"></a>
### func \(DeployDefaults\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L110>)

```go
func (d DeployDefaults) Validate(prefix string) error
```

Validate refuses a value a deploy could not use, naming the field under prefix \(deploy.defaults on the server\). An empty field is valid: it leaves the setting to the project and the built\-in default.

<a name="DigestRequirement"></a>
## type [DigestRequirement](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/require_digest.go#L12>)

DigestRequirement is deploy.require\_digest \(\#1703\): what registering a version, or making one current again, does with an image that is not pinned by a full sha256 digest. A tag can name other bytes tomorrow, and nodes that pulled it at different times can run different code for one version.

```go
type DigestRequirement string
```

<a name="DigestRequireOff"></a>The values of deploy.require\_digest.

```go
const (
    // DigestRequireOff accepts any image, as every release before the setting
    // did. The default.
    DigestRequireOff DigestRequirement = "off"
    // DigestRequireWarn accepts an unpinned image with a warning in the
    // register's answer, a log line and a count, so an operator can find the
    // pipelines to fix before enforcing.
    DigestRequireWarn DigestRequirement = "warn"
    // DigestRequireEnforce refuses an unpinned image at register.
    DigestRequireEnforce DigestRequirement = "enforce"
)
```

<a name="ParseDigestRequirement"></a>
### func [ParseDigestRequirement](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/require_digest.go#L36>)

```go
func ParseDigestRequirement(s string) (DigestRequirement, error)
```

ParseDigestRequirement reads a configured deploy.require\_digest: "" is DigestRequireOff, and any value other than off, warn or enforce is an error naming the key.

<a name="DigestRequirement.Active"></a>
### func \(DigestRequirement\) [Active](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/require_digest.go#L48>)

```go
func (r DigestRequirement) Active() bool
```

Active reports whether r checks images at all \(warn or enforce\).

<a name="DigestRequirement.CheckCurrentImage"></a>
### func \(DigestRequirement\) [CheckCurrentImage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/require_digest.go#L76>)

```go
func (r DigestRequirement) CheckCurrentImage(image string) (warning string, err error)
```

CheckCurrentImage is CheckImage for making a registered version current again \(a rollback\): the same decision, with a way out that fits a version already registered.

<a name="DigestRequirement.CheckImage"></a>
### func \(DigestRequirement\) [CheckImage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/require_digest.go#L56>)

```go
func (r DigestRequirement) CheckImage(image string) (warning string, err error)
```

CheckImage applies r to the image of a version being registered. A pinned image \(IsDigestPinned\), and any image while r is off, returns nothing. Under warn an unpinned image returns the warning to give; under enforce it returns the refusal, which is the client's to fix.

<a name="Execution"></a>
## type [Execution](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L252-L289>)

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
    // pod. Keys under ReservedMetadataPrefix belong to Dexaflow (the reconciler,
    // terminate path, warm-pool reconciler and token exchange select on them), so
    // Validate refuses them and the executor drops any that still arrive.
    Labels      map[string]string `json:"labels,omitempty" yaml:"labels,omitempty"`
    Annotations map[string]string `json:"annotations,omitempty" yaml:"annotations,omitempty"`
}
```

<a name="ExecutionMode"></a>
## type [ExecutionMode](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L40>)

ExecutionMode is a reserved field. Since the inline http\_api task type was removed every task runs inside a worker pod, so "pod" is its only value and nothing reads it. It stays in the schema because dag.json is closed \(additionalProperties: false\) and artifacts that carry it must keep validating \(\#521\).

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

<a name="ExecutionPolicy"></a>
## type [ExecutionPolicy](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L61-L73>)

ExecutionPolicy is the operator's allowlist of task execution fields, service accounts and images \(issues \#629 and \#537 C2\). A nil \*ExecutionPolicy allows everything, so callers need not branch on it.

```go
type ExecutionPolicy struct {
    // contains filtered or unexported fields
}
```

<a name="DenyAllExecutionPolicy"></a>
### func [DenyAllExecutionPolicy](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L120>)

```go
func DenyAllExecutionPolicy() *ExecutionPolicy
```

DenyAllExecutionPolicy allows no governed field. It is the safe reading of a configuration that failed to parse.

<a name="ParseExecutionPolicy"></a>
### func [ParseExecutionPolicy](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L77>)

```go
func ParseExecutionPolicy(c ExecutionPolicyConfig) (*ExecutionPolicy, error)
```

ParseExecutionPolicy validates the configured allowlist. It returns nil \(allow everything\) when nothing is restricted, which is the default.

<a name="ExecutionPolicy.CheckImage"></a>
### func \(\*ExecutionPolicy\) [CheckImage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L271>)

```go
func (p *ExecutionPolicy) CheckImage(image string) error
```

CheckImage returns why the DAG image may not run under the policy, or nil. An empty image is not checked: it is refused elsewhere.

<a name="ExecutionPolicy.CheckPriorityClass"></a>
### func \(\*ExecutionPolicy\) [CheckPriorityClass](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L244>)

```go
func (p *ExecutionPolicy) CheckPriorityClass(pc string) error
```

CheckPriorityClass returns why pc may not be used under the policy, or nil. Like CheckServiceAccount, it is for a value that did not come from the DAG.

<a name="ExecutionPolicy.CheckServiceAccount"></a>
### func \(\*ExecutionPolicy\) [CheckServiceAccount](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L234>)

```go
func (p *ExecutionPolicy) CheckServiceAccount(sa string) error
```

CheckServiceAccount returns why sa may not be used under the policy, or nil. It is the service account rule of CheckTask on its own, for a value that did not come from the DAG \(an admission hook's change, ADR 0100 §4\).

<a name="ExecutionPolicy.CheckSpec"></a>
### func \(\*ExecutionPolicy\) [CheckSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L253>)

```go
func (p *ExecutionPolicy) CheckSpec(spec *DAGSpec) error
```

CheckSpec returns every refusal across the DAG's tasks, or nil.

<a name="ExecutionPolicy.CheckTask"></a>
### func \(\*ExecutionPolicy\) [CheckTask](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L207>)

```go
func (p *ExecutionPolicy) CheckTask(t TaskSpec) error
```

CheckTask returns why the task may not run under the policy, or nil.

<a name="ExecutionPolicyConfig"></a>
## type [ExecutionPolicyConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/execution_policy.go#L41-L56>)

ExecutionPolicyConfig is executor.defaults.execution\_allowlist as configured.

```go
type ExecutionPolicyConfig struct {
    // Fields lists the governed fields a DAG author may set. Unset (nil) or "*"
    // allows every one; "none" or an explicitly empty list allows none.
    Fields []string
    // PriorityClasses, when not empty, lists the only values a task's
    // priority_class_name may take.
    PriorityClasses []string
    // ServiceAccounts, when not empty, lists the only execution.service_account
    // values a task may name (issue #537 C2). A task that names none runs as the
    // operator's default task ServiceAccount and is not checked.
    ServiceAccounts []string
    // Images, when not empty, lists the DAG images that may run (issue #537
    // C2): an exact image reference, or a prefix ending in "*" (for example
    // "registry.example.com/team-a/*"). A lone "*" allows any image.
    Images []string
}
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

ImportError is a DAG parse/compile failure surfaced as Airflow's "Import Errors" banner on the home dashboard. It is keyed by Filename; a successful re\-import of the same file clears it. The \`dexaflow lite\` watcher writes these on a failed compile and removes them on the next good compile.

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
## type [LeoflowConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L13-L71>)

LeoflowConfig is the developer\-facing project configuration parsed from dexaflow.yaml. It mirrors docs/api/leoflow\-yaml\-schema.json and is consumed by \`dexaflow compile\` to build an image and emit a DAGSpec.

```go
type LeoflowConfig struct {
    SchemaVersion string   `json:"schema_version,omitempty" yaml:"schema_version,omitempty"`
    DagID         string   `json:"dag_id" yaml:"dag_id"`
    Description   string   `json:"description,omitempty" yaml:"description,omitempty"`
    Owner         string   `json:"owner,omitempty" yaml:"owner,omitempty"`
    Tags          []string `json:"tags,omitempty" yaml:"tags,omitempty"`
    PythonVersion string   `json:"python_version,omitempty" yaml:"python_version,omitempty"`
    // PythonVersionDefaulted reports that ApplyDefaults supplied PythonVersion
    // because the config declared none. It is not part of the authoring surface
    // and never serialized: it exists so a consumer can tell "the author chose
    // 3.11" from "nobody said anything", which are the same string afterwards.
    // `dexaflow lite` needs the distinction: enforcing a version the CLI invented
    // refuses to boot over a choice the user never made (#1092 follow-up).
    PythonVersionDefaulted bool     `json:"-" yaml:"-"`
    BaseImage              string   `json:"base_image,omitempty" yaml:"base_image,omitempty"`
    Dependencies           []string `json:"dependencies,omitempty" yaml:"dependencies,omitempty"`
    Connectors             []string `json:"connectors,omitempty" yaml:"connectors,omitempty"`
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
    // dexaflow.yaml and the compiler overlays it onto the produced dag.json.
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
    // Airflow DAG attribute), so it lives in dexaflow.yaml and the compiler overlays
    // it onto the produced dag.json.
    Alerts *AlertsConfig `json:"alerts,omitempty" yaml:"alerts,omitempty"`
    // MinIdleWorkers is the number of idle warm workers the DAG asks to keep
    // for its current version (ADR 0058, ADR 0082): with warm pools on, an
    // unpaused DAG that declares it is prewarmed from the moment a version
    // becomes current. The compiler overlays it onto dag.json; the server
    // clamps it to execution.max_pool_size, and with warm pools off it does
    // nothing. Zero declares none.
    MinIdleWorkers int `json:"min_idle_workers,omitempty" yaml:"min_idle_workers,omitempty"`
}
```

<a name="LeoflowConfig.ApplyDefaults"></a>
### func \(\*LeoflowConfig\) [ApplyDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L229>)

```go
func (c *LeoflowConfig) ApplyDefaults()
```

ApplyDefaults fills zero\-valued fields with the defaults declared in the canonical JSON Schema \(internal/domain/schemas/leoflow\-yaml\-schema.json\). Explicit user\-set values are preserved; nested structs \(Build, Registry\) are instantiated when nil so their own defaults can be applied. The method is idempotent: a second call after the first is a no\-op.

Centralizing defaults here \(instead of scattered \`if x == ""\` fallbacks at each consumer\) is what lets the multi\-DAG workspace synthesize a working config when a subdir ships no dexaflow.yaml, while keeping the resolved values debuggable from one place.

<a name="LeoflowConfig.EffectiveDependencies"></a>
### func \(\*LeoflowConfig\) [EffectiveDependencies](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L276>)

```go
func (c *LeoflowConfig) EffectiveDependencies() ([]string, error)
```

EffectiveDependencies resolves the full pip install list the image/venv needs: the \`connectors:\` short names expanded to their apache\-airflow\-providers\-\* packages \(ADR 0038's sugar\), followed by the explicit \`dependencies:\` verbatim. Providers come first so a transitive driver pinned in dependencies resolves against the provider declared via the sugar.

An unknown connector name is a compile error, not a silent drop: a typo that slipped through would otherwise surface as a ModuleNotFoundError inside the task pod, far from its cause. The message names the offender, lists the known types, and points at the dependencies: escape hatch.

<a name="LeoflowConfig.Validate"></a>
### func \(\*LeoflowConfig\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L297>)

```go
func (c *LeoflowConfig) Validate() error
```

Validate checks the LeoflowConfig against the canonical dexaflow.yaml schema and returns a joined error describing every violation, or nil when valid.

<a name="LeoflowConfig.ValidateDockerfileValues"></a>
### func \(\*LeoflowConfig\) [ValidateDockerfileValues](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dockerfile_values.go#L146>)

```go
func (c *LeoflowConfig) ValidateDockerfileValues() error
```

ValidateDockerfileValues refuses every value a generated Dockerfile \(or its .dockerignore\) would interpolate in a form the Dockerfile format or Docker's operand lexer would give a meaning the author did not write.

It lives here rather than in the renderer because the renderer only runs under \`compile \-\-build\`: \`validate\` and plain \`compile\` used to call such a project valid, and the refusal first arrived on the CI runner about to build it \(\#1268\). Validate calls it, so every command that reads a dexaflow.yaml gets it, and both Dockerfile generators call it too, so a new generator that takes a config cannot skip it by forgetting a helper.

Every check runs on the RAW value, under the name the author wrote, before any filepath.Clean or filepath.Base: those transforms hide the defect rather than solve it \("evil\\nstuff/.." cleans to "."\).

<a name="PackageIndex"></a>
## type [PackageIndex](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_inputs.go#L32-L39>)

PackageIndex is a Python package index that replaces PyPI for the dependencies of the image. Its credentials are read from the environment of the build, never written in the URL.

```go
type PackageIndex struct {
    // URL is the index's simple API, such as https://pypi.example.com/simple.
    URL string `json:"url" yaml:"url"`
    // UsernameEnv is the environment variable holding the user name.
    UsernameEnv string `json:"username_env,omitempty" yaml:"username_env,omitempty"`
    // PasswordEnv is the environment variable holding the password or token.
    PasswordEnv string `json:"password_env,omitempty" yaml:"password_env,omitempty"`
}
```

<a name="PackageIndex.IndexURL"></a>
### func \(\*PackageIndex\) [IndexURL](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/build_inputs.go#L142>)

```go
func (p *PackageIndex) IndexURL(lookup func(string) (string, bool)) (string, error)
```

IndexURL returns the URL a build installs from: URL with the credentials of UsernameEnv and PasswordEnv, read with lookup \(os.LookupEnv\), or URL as written when it declares none. A declared variable that is not set is an error naming it, so a build never runs anonymously against an index that wants a credential.

<a name="PageCursor"></a>
## type [PageCursor](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/page_cursor.go#L11-L15>)

PageCursor is a keyset position in a list ordered newest first: the next page holds the rows strictly before \(At, Key\). Key breaks ties between rows with the same At: the run id for dag runs, the entry id for the audit log. Total is the number of rows the list had when the cursor was issued, so a later page reports it instead of counting the whole filtered set again; 0 means unknown, and the page counts.

```go
type PageCursor struct {
    At    time.Time
    Key   string
    Total int
}
```

<a name="ParamSpec"></a>
## type [ParamSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L140-L146>)

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
## type [PoolUsage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/pool.go#L24-L29>)

PoolUsage is a pool's per\-state occupancy in slots, feeding the Airflow PoolResponse slot fields: each task instance counts its pool\_slots, the size the admission gate charges it \(ADR 0066\), not 1. The slots admission actually spends are queued\+running; scheduled and deferred are reported for the UI but do not hold a slot.

```go
type PoolUsage struct {
    Running   int
    Queued    int
    Scheduled int
    Deferred  int
}
```

<a name="PythonDeprecation"></a>
## type [PythonDeprecation](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/python.go#L15-L30>)

PythonDeprecation records why a published Python line is on its way out and when it stops being published. It is read from the authoring schema rather than declared in Go so that the enum and the deprecation notes cannot drift apart: they are two fields of the same object.

```go
type PythonDeprecation struct {
    // Version is the deprecated python_version value, e.g. "3.10".
    Version string `json:"-"`
    // EOL is the upstream CPython end-of-life date (YYYY-MM-DD). After it,
    // docker-library/python stops rebuilding the line and the base image stops
    // receiving OS security updates.
    EOL string `json:"eol"`
    // RemoveAfter is the date after which the release stops publishing a base
    // image for this line (YYYY-MM-DD).
    RemoveAfter string `json:"remove_after"`
    // Replacement is the version an author should move to.
    Replacement string `json:"replacement"`
    // Reason is the human-readable justification, quoted verbatim in the CLI
    // warning so the author gets the argument and not just a verdict.
    Reason string `json:"reason"`
}
```

<a name="DeprecatedPythonVersion"></a>
### func [DeprecatedPythonVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/python.go#L134>)

```go
func DeprecatedPythonVersion(v string) (PythonDeprecation, bool)
```

DeprecatedPythonVersion reports the deprecation record for v, if the schema carries one. The second result is false for a version that is fine.

<a name="RetiredPythonVersion"></a>
### func [RetiredPythonVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/python.go#L151>)

```go
func RetiredPythonVersion(v string) (PythonDeprecation, bool)
```

RetiredPythonVersion reports the record of a Python line that is no longer published, if the schema carries one. Such a line is not in the enum, so Validate refuses it; the record is what lets the refusal name the replacement.

<a name="RegistryConfig"></a>
## type [RegistryConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L166-L174>)

RegistryConfig describes where the built image is pushed and how it is tagged.

```go
type RegistryConfig struct {
    URL string `json:"url,omitempty" yaml:"url,omitempty"`
    // AuthMethod is where the CLI reads the registry credential: auto (the
    // default), docker_config or cloud; env and iam still load, deprecated
    // (build.ParseAuthMethod, ADR 0080).
    AuthMethod  string `json:"auth_method,omitempty" yaml:"auth_method,omitempty"`
    ImageName   string `json:"image_name,omitempty" yaml:"image_name,omitempty"`
    TagStrategy string `json:"tag_strategy,omitempty" yaml:"tag_strategy,omitempty"`
}
```

<a name="RemoteBuilderConfig"></a>
## type [RemoteBuilderConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/remote_builder.go#L13-L19>)

RemoteBuilderConfig is build.remote\_builder in dexaflow.yaml, and remote\_builder in a workspace file's defaults and targets: the remote builder that builds the DAG image when a real build is needed \(path 4 of ADR 0073, ADR 0083\). One backend is set.

```go
type RemoteBuilderConfig struct {
    // CloudBuild builds on Google Cloud Build through the gcloud CLI.
    CloudBuild *CloudBuildConfig `json:"cloud_build,omitempty" yaml:"cloud_build,omitempty"`
    // BuildKit builds on the in-cluster BuildKit daemon the Helm chart runs
    // (builder.enabled), through buildctl and kubectl.
    BuildKit *BuildKitConfig `json:"buildkit,omitempty" yaml:"buildkit,omitempty"`
}
```

<a name="RemoteBuilderConfig.Validate"></a>
### func \(\*RemoteBuilderConfig\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/remote_builder.go#L89>)

```go
func (r *RemoteBuilderConfig) Validate(prefix string) error
```

Validate refuses a remote builder a build could not use, naming the field under prefix \(build.remote\_builder in dexaflow.yaml\).

<a name="ResourceQuantity"></a>
## type [ResourceQuantity](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L239-L247>)

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

<a name="ResourceUnit"></a>
## type [ResourceUnit](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L39-L44>)

ResourceUnit is the operator's resource unit \(executor.unit, ADR 0066 §3\): the CPU and memory one pool slot stands for. A task of pool\_slots N may use at most N units, and a task that declares no CPU or memory gets N units. A nil \*ResourceUnit means no unit is configured and every method is a no\-op, so callers need not branch on it.

```go
type ResourceUnit struct {
    // contains filtered or unexported fields
}
```

<a name="ParseResourceUnit"></a>
### func [ParseResourceUnit](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L51>)

```go
func ParseResourceUnit(c ResourceUnitConfig) (*ResourceUnit, error)
```

ParseResourceUnit parses the configured unit. CPU and memory both empty means no unit \(nil, nil\), whatever enforce and max\_size say. Otherwise both must be valid, positive Kubernetes quantities: a unit with only one dimension would leave the other unbounded, which is the opposite of what a unit is for.

<a name="ResourceUnit.Apply"></a>
### func \(\*ResourceUnit\) [Apply](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L108>)

```go
func (u *ResourceUnit) Apply(t TaskSpec) *Resources
```

Apply returns the resources a task's pod gets. With no unit it returns the task's resources unchanged \(nil when it declares none\). With a unit:

- A task that declares no cpu or memory gets requests = limits = pool\_slots x unit \(Guaranteed QoS\), as ADR 0066 §3 says.
- A value the task declares is never changed: the engine does not clamp or rewrite an author's resources.
- A missing request follows the declared limit of the same dimension, the value Kubernetes itself would give it, else pool\_slots x unit.
- A missing limit is pool\_slots x unit, or the declared request when that is larger, which only a misfit tolerated under enforce: warn can be. That task then runs with its own resources, as warn promises.

So every task ends up with a cpu and memory limit, and requests are never above limits, which the API server would reject. The task is not modified.

<a name="ResourceUnit.Check"></a>
### func \(\*ResourceUnit\) [Check](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L204>)

```go
func (u *ResourceUnit) Check(t TaskSpec) (warned, refused error)
```

Check decides a task against the unit. refused is a misfit that must stop it \(registration fails, dispatch refuses\). warned is a misfit tolerated under enforce: warn, which the caller logs and counts and then lets through. A task over max\_size is always refused: pool\_slots is new with the unit, so no task that ran before can trip it. Nil unit: both nil.

<a name="ResourceUnit.CheckSpec"></a>
### func \(\*ResourceUnit\) [CheckSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L220>)

```go
func (u *ResourceUnit) CheckSpec(d *DAGSpec) (warned []error, refused error)
```

CheckSpec runs Check on every task of the DAG: refused is the first refusal, warned every tolerated misfit. Nil unit: both nil.

<a name="ResourceUnit.WarmEligible"></a>
### func \(\*ResourceUnit\) [WarmEligible](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L253>)

```go
func (u *ResourceUnit) WarmEligible(t TaskSpec) bool
```

WarmEligible reports whether a task may be placed on a warm pod. A warm pod is one unit, so with a unit set only a size\-1 task that declares no resources fits on it; any other task takes a dedicated pod. Without a unit every task is eligible, as before.

<a name="ResourceUnit.WarmResources"></a>
### func \(\*ResourceUnit\) [WarmResources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L238>)

```go
func (u *ResourceUnit) WarmResources() *Resources
```

WarmResources is what a warm pod \(ADR 0058\) gets while a unit is set: one unit as requests and limits. Nil without a unit, leaving warm pods as before.

<a name="ResourceUnitConfig"></a>
## type [ResourceUnitConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L27-L32>)

ResourceUnitConfig is the executor.unit section as configured.

```go
type ResourceUnitConfig struct {
    CPU     string
    Memory  string
    Enforce string // "" means UnitEnforceRefuse
    MaxSize int    // 0 means DefaultUnitMaxSize
}
```

<a name="Resources"></a>
## type [Resources](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L225-L235>)

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

<a name="SafeError"></a>
## type [SafeError](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/safe_error.go#L29-L35>)

SafeError carries a message Dexaflow composed itself, attached to one of the sentinel classes above. It exists so the API boundary can tell "a phrase we wrote for the caller" apart from "whatever text the failure happened to carry".

The distinction is a security boundary, not a style choice. A repository error usually wraps a driver error, and a \*pgconn.PgError renders itself as "severity: message \(SQLSTATE code\)" with the constraint, table and column names of our schema inside the message. Echoing that to an authenticated tenant of a multi\-tenant control plane is schema disclosure \(CWE\-209\). The API therefore returns Message verbatim and replaces everything else with a constant phrase, logging the real error instead.

That inverts the safe/unsafe default: a new error is opaque to clients until someone deliberately writes it as a SafeError, so a newly wrapped driver error cannot leak by omission.

Message is returned to API clients verbatim. Never interpolate a driver, filesystem, or network error into it: pass the caller\-facing facts \(a role name, a declared variable, a configured cap\) and let the underlying error reach the log through the wrap chain.

```go
type SafeError struct {
    // Message is the client-facing phrase, without the class's own wording.
    Message string
    // Class is the sentinel this failure belongs to (ErrValidation,
    // ErrConflict, ErrNotFound, ...). errors.Is sees it through Unwrap.
    Class error
}
```

<a name="SafeError.ClientMessage"></a>
### func \(\*SafeError\) [ClientMessage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/safe_error.go#L51>)

```go
func (e *SafeError) ClientMessage() string
```

ClientMessage returns the phrase that may be shown to an API client.

<a name="SafeError.Error"></a>
### func \(\*SafeError\) [Error](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/safe_error.go#L40>)

```go
func (e *SafeError) Error() string
```

Error renders "\<message\>: \<class\>", identical to what fmt.Errorf\("\<message\>: %w", class\) produced before, so logs and error strings are unchanged.

<a name="SafeError.Unwrap"></a>
### func \(\*SafeError\) [Unwrap](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/safe_error.go#L48>)

```go
func (e *SafeError) Unwrap() error
```

Unwrap returns the sentinel class so errors.Is keeps working.

<a name="StagingConfig"></a>
## type [StagingConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L151-L155>)

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

<a name="SuspensionMode"></a>
## type [SuspensionMode](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_suspension.go#L6>)

SuspensionMode says how much of a suspended tenant's work stops \(\#2181\).

```go
type SuspensionMode string
```

<a name="SuspensionHold"></a>The suspension modes, each stopping more than the one before.

```go
const (
    // SuspensionHold creates no new run for the tenant, manual or scheduled,
    // and lets the work already queued or running finish.
    SuspensionHold SuspensionMode = "hold"
    // SuspensionDrain is hold, and also dispatches no task of the tenant: a
    // ready task stays scheduled, a queued run is not started, and the
    // attempts already dispatched finish.
    SuspensionDrain SuspensionMode = "drain"
    // SuspensionStop is drain, and also fails the tenant's queued and running
    // task instances, so their attempts are told to terminate on their next
    // heartbeat, as after a mark failed.
    SuspensionStop SuspensionMode = "stop"
)
```

<a name="SuspensionMode.HoldsDispatch"></a>
### func \(SuspensionMode\) [HoldsDispatch](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_suspension.go#L25>)

```go
func (m SuspensionMode) HoldsDispatch() bool
```

HoldsDispatch reports whether the mode dispatches nothing of the tenant: drain and stop.

<a name="TaskConfig"></a>
## type [TaskConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/config.go#L126-L141>)

TaskConfig holds the dexaflow.yaml per\-task overrides bound by task\_id \(ADR 0023\). Every field is optional; a set field overrides the value compiled from the DAG \(most specific wins: task override \> DAG default\_args\). These are Dexaflow deployment concerns, not Airflow operator attributes.

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
    // Size is the task's pool_slots (ADR 0066): how many slots of its pool it
    // takes while queued or running. It overrides pool_slots from dag.py.
    Size *int `json:"size,omitempty" yaml:"size,omitempty"`
}
```

<a name="TaskInstance"></a>
## type [TaskInstance](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L203-L241>)

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
    // Pool is the pool the instance is charged to, "default_pool" when the task
    // declares none, and PoolSlots the slots the admission gate charges it (ADR
    // 0066). Both are the instance's own: a clear re-binds them from the version
    // the re-run executes (#1589).
    Pool      string
    PoolSlots int
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

<a name="TaskInstanceFilter"></a>
## type [TaskInstanceFilter](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/run.go#L63-L70>)

TaskInstanceFilter specifies filtering options for listing task instances.

```go
type TaskInstanceFilter struct {
    States              []string
    TaskIDPattern       string
    TaskIDPrefixPattern string
    Pool                string
    Limit               int
    Offset              int
}
```

<a name="TaskSpec"></a>
## type [TaskSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L165-L222>)

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
    Pool string `json:"pool,omitempty"`
    // PoolSlots is how many slots of its pool the task takes while it is queued
    // or running (Airflow's pool_slots, ADR 0066). Zero means 1, so a DAG that
    // never sets it weighs exactly what it did before. dexaflow.yaml sets it as
    // `size`. Ignored where Pool is (Lite).
    PoolSlots               int               `json:"pool_slots,omitempty"`
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
    // Params are a bash or python task's own params (Airflow's task-level
    // params=), captured at compile time (#1843). RunParams layers them over the
    // DAG's param defaults and under the run conf. A captured operator carries
    // its params in OperatorArgs instead.
    Params map[string]any `json:"params,omitempty"`
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

<a name="TaskSpec.EffectivePoolSlots"></a>
### func \(TaskSpec\) [EffectivePoolSlots](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L306>)

```go
func (t TaskSpec) EffectivePoolSlots() int
```

EffectivePoolSlots is the number of pool slots the task takes: its PoolSlots, or 1 when unset \(ADR 0066\).

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
    // TaskStateRemoved means the version the run is bound to no longer
    // declares the task: a clear that moved a finished run to the DAG's current
    // version set it on the task instance of a task that version dropped (ADR
    // 0086). Terminal, as in Airflow, and never part of the run's outcome,
    // which counts only the tasks of the run's version.
    TaskStateRemoved TaskState = "removed"
)
```

<a name="TaskState.IsTerminal"></a>
### func \(TaskState\) [IsTerminal](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/state.go#L41>)

```go
func (s TaskState) IsTerminal() bool
```

IsTerminal reports whether the task state is final \(no further automatic transitions occur from it\).

<a name="TaskType"></a>
## type [TaskType](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L16>)

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

<a name="TenantLimits"></a>
## type [TenantLimits](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_limits.go#L13-L26>)

TenantLimits are optional caps on what one tenant may do, set by the operator through the service API. A zero field means unlimited, which is also what a tenant created without limits has.

```go
type TenantLimits struct {
    // MaxDags caps how many DAGs the tenant may register. New versions of a DAG
    // it already has are always accepted.
    MaxDags int
    // MaxRunsPerDay caps how many DAG runs, manual and scheduled alike, the
    // tenant may create in one UTC calendar day.
    MaxRunsPerDay int
    // MinScheduleIntervalSeconds is the shortest gap, in seconds, a DAG's
    // schedule may leave between two consecutive runs (MinScheduleInterval).
    MinScheduleIntervalSeconds int
    // MaxTaskPoolSlots caps a task's pool_slots (its size, ADR 0066): a DAG
    // with a larger task is refused at registration.
    MaxTaskPoolSlots int
}
```

<a name="TenantLimitsUpdate"></a>
## type [TenantLimitsUpdate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_limits.go#L30-L35>)

TenantLimitsUpdate changes some of a tenant's limits: a nil field leaves the stored value as it is, and a pointer to 0 sets that limit back to unlimited.

```go
type TenantLimitsUpdate struct {
    MaxDags                    *int
    MaxRunsPerDay              *int
    MinScheduleIntervalSeconds *int
    MaxTaskPoolSlots           *int
}
```

<a name="TenantLimitsUpdate.IsZero"></a>
### func \(TenantLimitsUpdate\) [IsZero](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_limits.go#L38>)

```go
func (u TenantLimitsUpdate) IsZero() bool
```

IsZero reports whether the update changes nothing.

<a name="TenantSuspension"></a>
## type [TenantSuspension](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_suspension.go#L37-L43>)

TenantSuspension is a tenant's suspension, set by the operator through the service API. The zero value is a tenant that is not suspended. The tenant's DAGs keep their own paused flag, so lifting a suspension restores exactly what the tenant had.

```go
type TenantSuspension struct {
    Mode   SuspensionMode
    Reason string
    // Since is when the tenant was first suspended; changing the reason of a
    // suspension in place keeps it.
    Since time.Time
}
```

<a name="TenantSuspension.Active"></a>
### func \(TenantSuspension\) [Active](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_suspension.go#L46>)

```go
func (s TenantSuspension) Active() bool
```

Active reports whether the tenant is suspended.

<a name="TenantSuspension.RefusalError"></a>
### func \(TenantSuspension\) [RefusalError](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_suspension.go#L64>)

```go
func (s TenantSuspension) RefusalError() error
```

RefusalError is the error a write that would create work for the suspended tenant returns: ErrTenantSuspended, with the reason when there is one.

<a name="TenantSuspension.Validate"></a>
### func \(TenantSuspension\) [Validate](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/tenant_suspension.go#L50>)

```go
func (s TenantSuspension) Validate() error
```

Validate refuses a suspension the engine cannot apply: a mode other than hold, drain or stop, or a reason longer than MaxSuspensionReasonLength.

<a name="TriggerRule"></a>
## type [TriggerRule](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/dag.go#L49>)

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

<a name="UnitMisfitError"></a>
## type [UnitMisfitError](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L156-L163>)

UnitMisfitError is a task that does not fit the unit: it declares more than pool\_slots x unit, or its pool\_slots is above executor.unit.max\_size.

```go
type UnitMisfitError struct {
    TaskID  string
    Size    int // the task's pool_slots
    Need    int // the size its resources need; 0 when over max_size
    MaxSize int // set when the task is over executor.unit.max_size
    // contains filtered or unexported fields
}
```

<a name="UnitMisfitError.Error"></a>
### func \(\*UnitMisfitError\) [Error](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/resource_unit.go#L165>)

```go
func (e *UnitMisfitError) Error() string
```



<a name="UsageCursor"></a>
## type [UsageCursor](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/usage.go#L8-L11>)

UsageCursor is a position in the order the service API serves usage events \(ADR 0096 point 4\): the writing transaction's id, then the event id. The zero value is before every event.

```go
type UsageCursor struct {
    Xid uint64
    ID  int64
}
```

<a name="UsageCursor.Before"></a>
### func \(UsageCursor\) [Before](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/usage.go#L14>)

```go
func (c UsageCursor) Before(o UsageCursor) bool
```

Before reports whether c sorts before o.

<a name="UsageEvent"></a>
## type [UsageEvent](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/usage.go#L21-L42>)

UsageEvent is one finished task attempt, or a later revision of it, as the service API serves it. An attempt is \(TaskInstanceID, TryNumber, AttemptEpoch\); with Revision it is the key a consumer dedupes on.

```go
type UsageEvent struct {
    Cursor UsageCursor
    // Tenant, DagID and RunID are resolved when the event is served; they are
    // empty once the tenant or the run is deleted.
    Tenant         string
    DagID, RunID   string
    TaskInstanceID string
    TaskID         string
    MapIndex       int
    TryNumber      int
    AttemptEpoch   int
    Revision       int
    State          TaskState
    PreviousState  TaskState
    PoolSlots      int
    // StartedAt and EndedAt are nil for a task that never started (skipped,
    // upstream failed). EndedAt is the task's end time, or the time of the
    // change that ended the attempt when the task has none.
    StartedAt, EndedAt *time.Time
    LastFailureKind    string
    RecordedAt         time.Time
}
```

<a name="UsagePage"></a>
## type [UsagePage](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/usage.go#L45-L54>)

UsagePage is one read of the usage events after a cursor.

```go
type UsagePage struct {
    // Enabled is false when recording is off; nothing else is set then.
    Enabled bool
    // Gone reports that the retention janitor deleted events after the
    // cursor; Oldest is then the first cursor whose following events are
    // complete, and Events is empty.
    Gone   bool
    Oldest UsageCursor
    Events []UsageEvent
}
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
## type [Variable](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/variable.go#L6-L18>)

Variable is a tenant\-scoped key/value setting consumed by DAGs and managed from the Admin UI. Value is stored as\-is \(plaintext for the MVP\); the API masks values of secret\-ish keys.

```go
type Variable struct {
    Key         string
    Value       string
    Description string
    // Encrypted reports that the value is stored sealed with the secret key
    // (#507). It is read-only: writes follow the server's variable_encryption.
    Encrypted bool
    // Unreadable reports a sealed value that no configured secret key opens
    // (the key was rotated without keeping the old one, for example). Value is
    // empty then. Only listings set it; reading the variable alone is an
    // ErrValueUnreadable error.
    Unreadable bool
}
```

<a name="VariablePatch"></a>
## type [VariablePatch](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/variable.go#L26-L30>)

VariablePatch is a tri\-state write to a Variable \(\#887\). Description mirrors domain.ConnectionPatch: nil preserves the stored value \(COALESCE\), non\-nil "" clears, and a value sets. Value is subtly different because the \`value\` column is NOT NULL: the caller resolves an omitted or masked \("\*\*\*" for a sensitive key\) value to the stored value BEFORE building the patch, so Value is expected non\-nil here — a non\-nil "" still clears. Key is always written.

```go
type VariablePatch struct {
    Key         string
    Value       *string
    Description *string
}
```

<a name="WarmPolicy"></a>
## type [WarmPolicy](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/warm_placement.go#L32-L51>)

WarmPolicy is what decides whether a task attempt may run on a warm worker: whether warm pools are on, and the operator settings a warm pod is created with before any task is known. Dispatch places an attempt with it, and the register response reports each task's mode with it, so the report says what dispatch does.

```go
type WarmPolicy struct {
    // Enabled is execution.warm_pools_enabled, as the dispatcher sees it (a
    // warm placer is wired).
    Enabled bool
    // ServiceAccount is the operator's default task ServiceAccount, the one a
    // warm worker runs as for its whole life.
    ServiceAccount string
    // Unit is executor.unit (ADR 0066 §3): with it a warm pod is one unit.
    Unit *ResourceUnit
    // Limits is executor.defaults.limits_* (#537): without a unit they size
    // the warm pod.
    Limits *DefaultLimits
    // SourceModeImage is the runtime image when source mode is on, "" when it
    // is off (ADR 0067 §3).
    SourceModeImage string
    // SourceModeWarm is execution.source_mode.warm_pools: a source-mode
    // version may run on warm workers, which receive its dag.py with each
    // assignment (ADR 0095).
    SourceModeWarm bool
}
```

<a name="WarmPolicy.DedicatedReason"></a>
### func \(WarmPolicy\) [DedicatedReason](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/warm_placement.go#L79>)

```go
func (p WarmPolicy) DedicatedReason(v WarmVersion, t TaskSpec) string
```

DedicatedReason says why an attempt of task t of version v takes a dedicated pod, or "" when it may be placed on a warm worker. Warm placement degrades, it never strands: a task the warm pod could not run as its own pod would takes the dedicated path.

<a name="WarmPolicy.HasWarmTask"></a>
### func \(WarmPolicy\) [HasWarmTask](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/warm_placement.go#L91>)

```go
func (p WarmPolicy) HasWarmTask(tasks []TaskSpec) bool
```

HasWarmTask reports whether at least one of tasks may be placed on a warm worker of a version VersionReason accepts: the per\-task half of DedicatedReason. A version with no such task keeps no warm pool, since its idle workers would serve none of its tasks \(\#2065\); a version with no task has none.

<a name="WarmPolicy.VersionReason"></a>
### func \(WarmPolicy\) [VersionReason](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/warm_placement.go#L63>)

```go
func (p WarmPolicy) VersionReason(v WarmVersion) string
```

VersionReason says why no task of version v may run on a warm worker, or "" when its tasks may \(each still subject to DedicatedReason\).

<a name="WarmPoolDefaults"></a>
## type [WarmPoolDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L77-L87>)

WarmPoolDefaults are the warm pool settings of a cluster, as a deploy reads them \(\#1890\): a DAG that declares no min\_idle\_workers gets MinIdleWorkers, and every value is capped at MaxPoolSize, while warm pools are Enabled. EffectiveMinIdle resolves them for one version.

```go
type WarmPoolDefaults struct {
    // Enabled is execution.warm_pools_enabled: off, no worker is kept warm
    // whatever a DAG declares.
    Enabled bool `json:"enabled"`
    // MinIdleWorkers is execution.min_idle_workers, the idle warm workers
    // kept for a version whose DAG declares none.
    MinIdleWorkers int `json:"min_idle_workers"`
    // MaxPoolSize is execution.max_pool_size, the cap on one version's pool;
    // 0 sets none.
    MaxPoolSize int `json:"max_pool_size"`
}
```

<a name="WarmPoolDefaults.EffectiveMinIdle"></a>
### func \(\*WarmPoolDefaults\) [EffectiveMinIdle](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/deploy_defaults.go#L163>)

```go
func (w *WarmPoolDefaults) EffectiveMinIdle(dagMinIdle int) int
```

EffectiveMinIdle resolves the warm\-worker target for one dag\_version under model A2 \(ADR 0058 N1b2b\): the DAG author declares desired warmth per DAG \(dagMinIdle\), the operator caps and floors it.

- Warm pools OFF \(or w nil\) =\> always 0. This is what makes a default deploy a byte\-for\-byte no\-op: with warmth gated off no warm pod is ever targeted, so the reconciler \(when it runs at all\) reconciles every pool to zero.
- The DAG author's value wins when set \(\> 0\); when the DAG declares none \(0\) it falls back to the operator's execution.min\_idle\_workers floor.
- The resolved value is clamped to \[0, max\_pool\_size\] so an author can never provision more warmth than the operator's per\-version cap allows, and a nonsensical negative never underflows.

<a name="WarmVersion"></a>
## type [WarmVersion](<https://github.com/dexadata/dexaflow/blob/main/internal/domain/warm_placement.go#L55-L59>)

WarmVersion is what of a DAG version the warm decision reads: the image its tasks run, whether it carries its dag.py, and its staging block.

```go
type WarmVersion struct {
    Image     string
    HasSource bool
    Staging   *StagingConfig
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
