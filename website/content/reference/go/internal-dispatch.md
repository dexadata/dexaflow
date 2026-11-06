---
title: "internal/dispatch"
linkTitle: "internal/dispatch"
weight: 4
---

```go
import "github.com/dexadata/dexaflow/internal/dispatch"
```

Package dispatch launches pod\-path task instances: it resolves a task's execution context, mints the agent's identity token, and routes the request to the executor. It implements scheduler.Dispatcher.

## Index

- [Constants](<#constants>)
- [Variables](<#variables>)
- [type AdmissionOptions](<#AdmissionOptions>)
- [type AdmissionRecorder](<#AdmissionRecorder>)
- [type BufferConfig](<#BufferConfig>)
- [type BufferedDispatcher](<#BufferedDispatcher>)
  - [func NewBuffered\(inner Inner, sink FailureSink, logger \*slog.Logger, metrics MetricsRecorder, cfg BufferConfig\) \*BufferedDispatcher](<#NewBuffered>)
  - [func \(b \*BufferedDispatcher\) Close\(\) error](<#BufferedDispatcher.Close>)
  - [func \(b \*BufferedDispatcher\) Dispatch\(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec\) \(executor.Disposition, error\)](<#BufferedDispatcher.Dispatch>)
- [type Dispatcher](<#Dispatcher>)
  - [func NewDispatcher\(exec executor.Executor, resolver Resolver, issuer TokenIssuer, controlAddr string, tokenTTL time.Duration\) \*Dispatcher](<#NewDispatcher>)
  - [func \(d \*Dispatcher\) Dispatch\(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec\) \(executor.Disposition, error\)](<#Dispatcher.Dispatch>)
  - [func \(d \*Dispatcher\) SetAdmission\(hooks \[\]admission.Hook, opts AdmissionOptions\)](<#Dispatcher.SetAdmission>)
  - [func \(d \*Dispatcher\) SetAgentTLSCAConfigMap\(name string\)](<#Dispatcher.SetAgentTLSCAConfigMap>)
  - [func \(d \*Dispatcher\) SetAgentTokenTransport\(transport, audience string, expirationSeconds int64\)](<#Dispatcher.SetAgentTokenTransport>)
  - [func \(d \*Dispatcher\) SetAttemptLifetimeCeiling\(ceiling time.Duration\)](<#Dispatcher.SetAttemptLifetimeCeiling>)
  - [func \(d \*Dispatcher\) SetDefaultTaskServiceAccount\(name string\)](<#Dispatcher.SetDefaultTaskServiceAccount>)
  - [func \(d \*Dispatcher\) SetPlatformDefaults\(p PlatformDefaults\)](<#Dispatcher.SetPlatformDefaults>)
  - [func \(d \*Dispatcher\) SetSecretsBackend\(class, kwargs string\)](<#Dispatcher.SetSecretsBackend>)
  - [func \(d \*Dispatcher\) SetSourceModeImage\(image string\)](<#Dispatcher.SetSourceModeImage>)
  - [func \(d \*Dispatcher\) SetSourceModeWarm\(on bool\)](<#Dispatcher.SetSourceModeWarm>)
  - [func \(d \*Dispatcher\) SetTaskSecret\(name, mountPath string\)](<#Dispatcher.SetTaskSecret>)
  - [func \(d \*Dispatcher\) SetUnitMisfitRecorder\(r UnitMisfitRecorder\)](<#Dispatcher.SetUnitMisfitRecorder>)
  - [func \(d \*Dispatcher\) SetWarmPlacer\(p WarmPlacer\)](<#Dispatcher.SetWarmPlacer>)
  - [func \(d \*Dispatcher\) WarmPolicy\(\) domain.WarmPolicy](<#Dispatcher.WarmPolicy>)
- [type FailureSink](<#FailureSink>)
- [type Inner](<#Inner>)
- [type MetricsRecorder](<#MetricsRecorder>)
- [type PlatformDefaults](<#PlatformDefaults>)
- [type Resolved](<#Resolved>)
- [type Resolver](<#Resolver>)
- [type RetrySink](<#RetrySink>)
- [type TokenIssuer](<#TokenIssuer>)
- [type UnitMisfitRecorder](<#UnitMisfitRecorder>)
- [type WarmPlacer](<#WarmPlacer>)


## Constants

<a name="AdmissionAdmitted"></a>Admission outcomes, the label of the admission metric. They are disjoint: breaker\_open is an attempt held because a hook was skipped, fail\_open one admitted unchecked because a hook failed or was skipped.

```go
const (
    AdmissionAdmitted    = "admitted"
    AdmissionChanged     = "changed"
    AdmissionHeld        = "held"
    AdmissionRefused     = "refused"
    AdmissionError       = "error"
    AdmissionFailOpen    = "fail_open"
    AdmissionBreakerOpen = "breaker_open"
)
```

<a name="DefaultAdmissionTimeout"></a>Admission defaults \(ADR 0100 §5\).

```go
const (
    // DefaultAdmissionTimeout bounds one hook call. Dispatch is synchronous by
    // default and runs on the scheduler tick, so it is short.
    DefaultAdmissionTimeout = 500 * time.Millisecond
)
```

## Variables

<a name="ErrAtCapacity"></a>ErrAtCapacity is returned by BufferedDispatcher.Dispatch when the buffered queue cannot accept another request. It travels with executor.Deferred: the scheduler leaves the TI scheduled so a later tick re\-tries, without counting it as a failed dispatch. It is the backpressure signal that bounds tick latency under load \(ADR 0031: tick rate decoupled from executor latency\).

```go
var ErrAtCapacity = errors.New("dispatch buffer at capacity; will retry next tick")
```

<a name="ErrBufferWaitExceeded"></a>ErrBufferWaitExceeded is the cause a worker hands to a RetrySink, with executor.Backpressure, for a request that waited in the buffer longer than BufferConfig.MaxWait. The task is re\-offered instead of dispatched \(\#1607\).

```go
var ErrBufferWaitExceeded = errors.New("dispatch request waited in the buffer past its bound; re-offering")
```

<a name="ErrDrainTimeout"></a>ErrDrainTimeout is returned by Close when workers did not finish within the drain timeout. Shutdown continues: blocking forever is worse, because the runtime then kills the process and no drain happens at all \(\#463\).

```go
var ErrDrainTimeout = errors.New("drain timed out")
```

<a name="AdmissionOptions"></a>
## type [AdmissionOptions](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/admission.go#L60-L69>)

AdmissionOptions configure the admission hooks of a Dispatcher.

```go
type AdmissionOptions struct {
    // Timeout bounds each hook call; zero uses DefaultAdmissionTimeout.
    Timeout time.Duration
    // FailOpen admits an attempt unchanged when a hook errors, times out,
    // panics or is skipped by its breaker, instead of holding it. A hook's
    // Hold is never failed open.
    FailOpen bool
    // Recorder counts outcomes; nil does not.
    Recorder AdmissionRecorder
}
```

<a name="AdmissionRecorder"></a>
## type [AdmissionRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/admission.go#L55-L57>)

AdmissionRecorder counts each admission outcome and how long the hooks took. observability.Metrics satisfies it.

```go
type AdmissionRecorder interface {
    RecordAdmission(outcome string, took time.Duration)
}
```

<a name="BufferConfig"></a>
## type [BufferConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L72-L90>)

BufferConfig sizes the BufferedDispatcher's worker pool. BufferSize=0 means "passthrough sync" \(Lite mode, zero overhead\): no goroutines spawned, no channel, the inner dispatcher is called inline. Any BufferSize\>0 spawns max\(Workers, 1\) worker goroutines and a buffered channel of BufferSize slots.

```go
type BufferConfig struct {
    BufferSize int
    Workers    int
    // DrainTimeout bounds how long Close waits for workers to finish. Zero
    // selects defaultDrainTimeout. Without a bound, one worker stuck in a
    // remote call that never answers blocks shutdown until the runtime kills
    // the process, which loses the drain entirely (#463).
    DrainTimeout time.Duration
    // MaxWait bounds how long a request may wait in the buffer before a worker
    // takes it. The scheduler records the task queued, stamping queued_at, as
    // soon as the buffer accepts it, so time spent in the buffer counts toward
    // the dispatch-lost threshold; a request older than MaxWait when a worker
    // takes it is not dispatched. When the sink is a RetrySink it is handed back
    // as executor.Backpressure, which re-offers the task with no dispatch
    // attempt charged and a fresh queued_at on its next queued episode (#1607).
    // Zero selects half the default dispatch-lost threshold; a negative value
    // disables the bound.
    MaxWait time.Duration
}
```

<a name="BufferedDispatcher"></a>
## type [BufferedDispatcher](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L114-L130>)

BufferedDispatcher fronts a synchronous Inner dispatcher with a bounded worker pool, so the scheduler tick is never blocked by a slow remote API call. ADR 0031: two\-phase scheduler — planning sync, dispatch async.

```go
type BufferedDispatcher struct {
    // contains filtered or unexported fields
}
```

<a name="NewBuffered"></a>
### func [NewBuffered](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L135>)

```go
func NewBuffered(inner Inner, sink FailureSink, logger *slog.Logger, metrics MetricsRecorder, cfg BufferConfig) *BufferedDispatcher
```

NewBuffered constructs a BufferedDispatcher. BufferSize=0 returns a passthrough that is byte\-for\-byte equivalent to using the inner dispatcher directly \(Lite path\). BufferSize\>0 spawns the worker pool.

<a name="BufferedDispatcher.Close"></a>
### func \(\*BufferedDispatcher\) [Close](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L220>)

```go
func (b *BufferedDispatcher) Close() error
```

Close stops accepting new dispatches, drains the in\-flight queue \(each buffered request is still processed by the inner dispatcher, or failed or re\-offered via the sink, so no TI is left stuck \`queued\`\), and waits for every worker to finish. Under mu so a concurrent Dispatch never sends on the closed channel. Idempotent.

The wait is bounded by cfg.DrainTimeout \(\#463\). A worker sits in the inner dispatcher with a detached context, so a remote API that accepts the connection and never answers would otherwise block shutdown forever — the process then dies by SIGKILL and loses the drain \#133 added. On expiry Close returns an error naming how many workers were abandoned; the caller logs it and proceeds with shutdown rather than hanging. The abandoned goroutines die with the process.

<a name="BufferedDispatcher.Dispatch"></a>
### func \(\*BufferedDispatcher\) [Dispatch](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L171>)

```go
func (b *BufferedDispatcher) Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
```

Dispatch hands a task off to the inner dispatcher. In passthrough mode the inner call happens inline. In buffered mode the request is enqueued non\- blockingly: success returns \(Dispatched, nil\) immediately \(the scheduler then records the TI as \`queued\`\); a full or closed channel returns \(Deferred, ErrAtCapacity\). Deferred tells the scheduler nothing was attempted, so the TI stays scheduled for a later tick without touching its dispatch\-attempt budget \(review item S2: a full buffer is backpressure, not a dispatch failure\).

<a name="Dispatcher"></a>
## type [Dispatcher](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L145-L196>)

Dispatcher builds executor requests for queued pod\-path tasks and runs them.

```go
type Dispatcher struct {
    // contains filtered or unexported fields
}
```

<a name="NewDispatcher"></a>
### func [NewDispatcher](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L245>)

```go
func NewDispatcher(exec executor.Executor, resolver Resolver, issuer TokenIssuer, controlAddr string, tokenTTL time.Duration) *Dispatcher
```

NewDispatcher builds a Dispatcher that launches tasks via exec, resolves their context with resolver, mints tokens with issuer \(valid for tokenTTL\), and tells the agent to reach the control plane at controlAddr.

<a name="Dispatcher.Dispatch"></a>
### func \(\*Dispatcher\) [Dispatch](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L298>)

```go
func (d *Dispatcher) Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
```

Dispatch resolves the task, mints its agent token, and executes it. The executor classifies its own dispatch outcome and returns it as an executor.Disposition \(ADR 0051 Phase 4\). A dispatcher\-INTERNAL failure that happens BEFORE Execute \(task resolve, token mint\) is permanent and so returns executor.Rejected — those bare errors classified as permanent before this change, so Rejected preserves the behavior exactly.

<a name="Dispatcher.SetAdmission"></a>
### func \(\*Dispatcher\) [SetAdmission](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/admission.go#L94>)

```go
func (d *Dispatcher) SetAdmission(hooks []admission.Hook, opts AdmissionOptions)
```

SetAdmission installs the admission hooks, called in order for every attempt after the built\-in refusals and before the agent token is minted \(ADR 0100\). No hooks leave dispatch exactly as it was.

<a name="Dispatcher.SetAgentTLSCAConfigMap"></a>
### func \(\*Dispatcher\) [SetAgentTLSCAConfigMap](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L268>)

```go
func (d *Dispatcher) SetAgentTLSCAConfigMap(name string)
```

SetAgentTLSCAConfigMap configures the CA ConfigMap mounted into task pods so agents verify the control plane's gRPC TLS cert \(issue \#58\). Empty = the agent stays on the insecure channel \(dev\).

<a name="Dispatcher.SetAgentTokenTransport"></a>
### func \(\*Dispatcher\) [SetAgentTokenTransport](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L288>)

```go
func (d *Dispatcher) SetAgentTokenTransport(transport, audience string, expirationSeconds int64)
```

SetAgentTokenTransport selects how the agent's bearer credential reaches the task pod \(ADR 0055 Fix \#3\). transport is "" / "envvar" \(the plaintext env\-var default\) or "exchange" \(project a ServiceAccount token the agent exchanges for a task\-scoped JWT\). audience and expirationSeconds configure the projected token and are read only under the exchange transport. Ignored by the subprocess \(Lite\) executor, which has no pod.

<a name="Dispatcher.SetAttemptLifetimeCeiling"></a>
### func \(\*Dispatcher\) [SetAttemptLifetimeCeiling](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L213>)

```go
func (d *Dispatcher) SetAttemptLifetimeCeiling(ceiling time.Duration)
```

SetAttemptLifetimeCeiling wires the operator's attempt credential ceiling \(auth.max\_attempt\_credential\_lifetime\) into every dispatched request, where the Kubernetes executor floors the pod's ActiveDeadlineSeconds with it when the task declares no execution timeout. A non\-positive value is "no ceiling" and applies no floor; the subprocess executor ignores it.

<a name="Dispatcher.SetDefaultTaskServiceAccount"></a>
### func \(\*Dispatcher\) [SetDefaultTaskServiceAccount](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L240>)

```go
func (d *Dispatcher) SetDefaultTaskServiceAccount(name string)
```

SetDefaultTaskServiceAccount sets the ServiceAccount task pods run as when a DAG's task does not specify execution.service\_account. Empty leaves pods on the namespace default SA \(today's behavior\). Wiring the chart's task ServiceAccount here closes the trap where creating the SA silently had no effect until every DAG also set execution.service\_account.

<a name="Dispatcher.SetPlatformDefaults"></a>
### func \(\*Dispatcher\) [SetPlatformDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L280>)

```go
func (d *Dispatcher) SetPlatformDefaults(p PlatformDefaults)
```

SetPlatformDefaults configures the per\-cluster task defaults applied at dispatch to fill gaps the DAG artifact left empty \(ADR 0023, layer L0\).

<a name="Dispatcher.SetSecretsBackend"></a>
### func \(\*Dispatcher\) [SetSecretsBackend](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L231>)

```go
func (d *Dispatcher) SetSecretsBackend(class, kwargs string)
```

SetSecretsBackend configures the operator's external secrets backend \(ADR 0060\): the provider class the in\-pod resolver drives and its raw kwargs JSON, delivered to the pod as operator\-owned LEOFLOW\_SECRETS\_\* env. Empty leaves external secrets off \(the chain stays vault\-only\).

<a name="Dispatcher.SetSourceModeImage"></a>
### func \(\*Dispatcher\) [SetSourceModeImage](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L258>)

```go
func (d *Dispatcher) SetSourceModeImage(image string)
```

SetSourceModeImage turns Pro source mode on for versions on image \(ADR 0067 §3\); "" leaves it off, today's behavior. Lite does not set it: the subprocess executor always runs from the source.

<a name="Dispatcher.SetSourceModeWarm"></a>
### func \(\*Dispatcher\) [SetSourceModeWarm](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L263>)

```go
func (d *Dispatcher) SetSourceModeWarm(on bool)
```

SetSourceModeWarm lets source\-mode attempts run on warm workers, which receive the version's dag.py with each assignment \(ADR 0095\). Off, the default, keeps every source\-mode attempt on a dedicated pod.

<a name="Dispatcher.SetTaskSecret"></a>
### func \(\*Dispatcher\) [SetTaskSecret](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L274>)

```go
func (d *Dispatcher) SetTaskSecret(name, mountPath string)
```

SetTaskSecret configures a Kubernetes Secret mounted read\-only into every task pod at mountPath, so tasks can read a credential \(e.g. a GCP service\-account key referenced by a connection's key\_path\) from the cluster's secret store rather than from Dexaflow \(ADR 0035\). Empty name = nothing mounted.

<a name="Dispatcher.SetUnitMisfitRecorder"></a>
### func \(\*Dispatcher\) [SetUnitMisfitRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L206>)

```go
func (d *Dispatcher) SetUnitMisfitRecorder(r UnitMisfitRecorder)
```

SetUnitMisfitRecorder wires the counter for tolerated unit misfits.

<a name="Dispatcher.SetWarmPlacer"></a>
### func \(\*Dispatcher\) [SetWarmPlacer](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L253>)

```go
func (d *Dispatcher) SetWarmPlacer(p WarmPlacer)
```

SetWarmPlacer wires the warm\-worker placement seam \(ADR 0058 N1b1\-place\). With a placer set, Dispatch tries to place an admitted attempt on a free warm worker of its dag\_version and only falls back to a dedicated pod on a warm miss. Leave it unset \(nil\) — the default — to keep dedicated pod\-per\-task, today's behavior.

<a name="Dispatcher.WarmPolicy"></a>
### func \(\*Dispatcher\) [WarmPolicy](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L488>)

```go
func (d *Dispatcher) WarmPolicy() domain.WarmPolicy
```

WarmPolicy is the warm placement decision this dispatcher applies \(ADR 0058\): warm pools are on when a placer is wired, and a warm pod has the default task ServiceAccount, the resource unit or default limits, and no source. The register response reports each task's execution mode from the same policy, built from the same configuration \(\#1707\).

<a name="FailureSink"></a>
## type [FailureSink](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L44-L46>)

FailureSink lets a worker report that an asynchronously\-dispatched task failed inside the inner dispatcher, so the scheduler can fail the TI with a clear reason. Without this callback a \`queued\` TI whose dispatch failed would sit forever \(no reaper targets \`queued\`; ADR 0031 \#128 only targets \`running\`\).

```go
type FailureSink interface {
    MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
}
```

<a name="Inner"></a>
## type [Inner](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L35-L37>)

Inner is the underlying synchronous dispatcher BufferedDispatcher wraps — matches scheduler.Dispatcher exactly so production wires through one type.

```go
type Inner interface {
    Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
}
```

<a name="MetricsRecorder"></a>
## type [MetricsRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L60-L65>)

MetricsRecorder records dispatch\-pool observability signals.

```go
type MetricsRecorder interface {
    RecordDispatchQueueDepth(depth int)
    RecordDispatchAtCapacity()
    RecordDispatchLatencySeconds(seconds float64)
    RecordDispatchInnerError()
}
```

<a name="PlatformDefaults"></a>
## type [PlatformDefaults](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L108-L142>)

PlatformDefaults are per\-cluster task defaults applied at dispatch to fill gaps the DAG artifact left empty \(ADR 0023, layer L0\). They are the lowest precedence \(task override \> DAG default \> platform default\) and never replace a value baked into dag.json, so the artifact stays portable across clusters.

```go
type PlatformDefaults struct {
    // StagingSize/StagingStorageClass default the per-run staging volume when the
    // DAG enabled staging but did not pin them (e.g. the cluster's RWX class).
    StagingSize         string
    StagingStorageClass string
    // StagingAccessMode is the PVC access mode for the staging volume (default
    // ReadWriteMany; single-node dev uses ReadWriteOnce).
    StagingAccessMode string
    // Resources defaults a task's requests/limits when neither the task override
    // nor the DAG set any.
    Resources *domain.Resources
    // Unit is the operator resource unit (executor.unit, ADR 0066). When set it
    // replaces Resources: every task is sized from its pool_slots, and a task
    // that declares more than pool_slots x unit is refused (or, under
    // enforce: warn, run and counted). Nil: no unit.
    Unit *domain.ResourceUnit
    // Limits fills an empty CPU or memory limit on every task pod, after the task
    // and L0 resources above are chosen (executor.defaults.limits_*, #537). Nil:
    // no default limit. Not applied with a unit, which always sets both limits.
    // Without a unit the same limits also size the warm pods (see WarmPolicy).
    Limits *domain.DefaultLimits
    // PodSecurity carries the task-pod hardening choices. It lives here, not in
    // the DAG spec, on purpose: whether untrusted task code may run as root is a
    // cluster-operator decision. Exposing it per-DAG would let an author elevate
    // their own task, which is the same self-service escalation as picking an
    // arbitrary service_account. It is populated from the cluster's configured
    // defaults (runAsNonRoot on), which an operator can override.
    PodSecurity executor.PodSecurity
    // ExecutionPolicy is the operator's allowlist of task execution fields
    // (executor.defaults.execution_allowlist, issue #629). Registration refuses a
    // DAG that breaks it; dispatch refuses a task of a DAG registered before the
    // list changed. Like PodSecurity it is cluster policy, never a DAG field.
    // Nil allows everything.
    ExecutionPolicy *domain.ExecutionPolicy
}
```

<a name="Resolved"></a>
## type [Resolved](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L72-L92>)

Resolved is the execution context the dispatcher needs to launch a task.

```go
type Resolved struct {
    TaskInstanceID  string
    TenantID        string
    Image           string
    ImagePullPolicy string
    TryNumber       int
    // AttemptEpoch identifies this execution attempt of the row (ADR 0051
    // amendment). The resolver claims a fresh value on every dispatch, so two
    // dispatches of one try never share it, even with no reset rail between
    // them. The agent token (A2) and the pod's attempt-epoch label (A4) are
    // minted from it.
    AttemptEpoch int
    // Staging carries the DAG's opt-in staging-volume config (ADR 0022); nil or
    // disabled means no per-run volume.
    Staging *domain.StagingConfig
    // Source is the dag.py text captured at compile time, threaded to the
    // SubprocessExecutor so Lite tasks can importlib their DAG without relying
    // on a globally-correct workdir. Empty for Pro (the container image carries
    // the source) and ignored by the KubernetesExecutor.
    Source string
}
```

<a name="Resolver"></a>
## type [Resolver](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L95-L97>)

Resolver loads a task instance's execution context from storage.

```go
type Resolver interface {
    ResolveTask(ctx context.Context, runID, taskID string) (Resolved, error)
}
```

<a name="RetrySink"></a>
## type [RetrySink](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/buffered.go#L54-L57>)

RetrySink is a FailureSink that can also re\-offer a task whose dispatch failed in a worker, given the disposition the inner dispatcher classified the error as. When the sink implements it, a worker failure is handled the way the scheduler handles a synchronous one \(scheduler.AsyncDispatchFailures\): backpressure and transient errors put the task back to scheduled with a backoff instead of failing it. A plain FailureSink keeps failing the task.

```go
type RetrySink interface {
    FailureSink
    HandleDispatchFailure(ctx context.Context, runID, taskID string, disp executor.Disposition, cause error) error
}
```

<a name="TokenIssuer"></a>
## type [TokenIssuer](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L100-L102>)

TokenIssuer mints a per\-task\-instance agent token.

```go
type TokenIssuer interface {
    IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}
```

<a name="UnitMisfitRecorder"></a>
## type [UnitMisfitRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L201-L203>)

UnitMisfitRecorder counts a task that does not fit the resource unit but is let through under executor.unit.enforce=warn, by stage \("register" or "dispatch"\). observability.Metrics satisfies it.

```go
type UnitMisfitRecorder interface {
    RecordUnitMisfit(stage string)
}
```

<a name="WarmPlacer"></a>
## type [WarmPlacer](<https://github.com/dexadata/dexaflow/blob/main/internal/dispatch/dispatch.go#L67-L69>)

WarmPlacer hands a per\-attempt WorkAssignment to a free warm worker of a dag\_version, returning false when none is free \(ADR 0058 N1b1\-place\). It is a narrow structural view of the agentrpc worker registry: the executor package must not import agentrpc, so the seam lives here and main.go passes the registry, which satisfies it. A nil WarmPlacer on the Dispatcher means warm pools are off — every task takes the dedicated pod path, today's behavior.

```go
type WarmPlacer interface {
    Assign(dagVersionID string, a *agentv1.WorkAssignment) bool
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
