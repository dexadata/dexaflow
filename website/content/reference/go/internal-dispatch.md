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

- [Variables](<#variables>)
- [type BufferConfig](<#BufferConfig>)
- [type BufferedDispatcher](<#BufferedDispatcher>)
  - [func NewBuffered\(inner Inner, sink FailureSink, logger \*slog.Logger, metrics MetricsRecorder, cfg BufferConfig\) \*BufferedDispatcher](<#NewBuffered>)
  - [func \(b \*BufferedDispatcher\) Close\(\) error](<#BufferedDispatcher.Close>)
  - [func \(b \*BufferedDispatcher\) Dispatch\(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec\) \(executor.Disposition, error\)](<#BufferedDispatcher.Dispatch>)
- [type Dispatcher](<#Dispatcher>)
  - [func NewDispatcher\(exec executor.Executor, resolver Resolver, issuer TokenIssuer, controlAddr string, tokenTTL time.Duration\) \*Dispatcher](<#NewDispatcher>)
  - [func \(d \*Dispatcher\) Dispatch\(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec\) \(executor.Disposition, error\)](<#Dispatcher.Dispatch>)
  - [func \(d \*Dispatcher\) SetAgentTLSCAConfigMap\(name string\)](<#Dispatcher.SetAgentTLSCAConfigMap>)
  - [func \(d \*Dispatcher\) SetAgentTokenTransport\(transport, audience string, expirationSeconds int64\)](<#Dispatcher.SetAgentTokenTransport>)
  - [func \(d \*Dispatcher\) SetAttemptLifetimeCeiling\(ceiling time.Duration\)](<#Dispatcher.SetAttemptLifetimeCeiling>)
  - [func \(d \*Dispatcher\) SetDefaultTaskServiceAccount\(name string\)](<#Dispatcher.SetDefaultTaskServiceAccount>)
  - [func \(d \*Dispatcher\) SetPlatformDefaults\(p PlatformDefaults\)](<#Dispatcher.SetPlatformDefaults>)
  - [func \(d \*Dispatcher\) SetSecretsBackend\(class, kwargs string\)](<#Dispatcher.SetSecretsBackend>)
  - [func \(d \*Dispatcher\) SetTaskSecret\(name, mountPath string\)](<#Dispatcher.SetTaskSecret>)
  - [func \(d \*Dispatcher\) SetWarmPlacer\(p WarmPlacer\)](<#Dispatcher.SetWarmPlacer>)
- [type FailureSink](<#FailureSink>)
- [type Inner](<#Inner>)
- [type MetricsRecorder](<#MetricsRecorder>)
- [type PlatformDefaults](<#PlatformDefaults>)
- [type Resolved](<#Resolved>)
- [type Resolver](<#Resolver>)
- [type TokenIssuer](<#TokenIssuer>)
- [type WarmPlacer](<#WarmPlacer>)


## Variables

<a name="ErrAtCapacity"></a>ErrAtCapacity is returned by BufferedDispatcher.Dispatch when the buffered queue cannot accept another request. The scheduler treats it exactly like a transient inner\-dispatcher error: log \+ metric \+ leave the TI scheduled so the next tick re\-tries. It is the backpressure signal that bounds tick latency under load \(ADR 0031: tick rate decoupled from executor latency\).

```go
var ErrAtCapacity = errors.New("dispatch buffer at capacity; will retry next tick")
```

<a name="ErrDrainTimeout"></a>ErrDrainTimeout is returned by Close when workers did not finish within the drain timeout. Shutdown continues: blocking forever is worse, because the runtime then kills the process and no drain happens at all \(\#463\).

```go
var ErrDrainTimeout = errors.New("drain timed out")
```

<a name="BufferConfig"></a>
## type [BufferConfig](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L56-L64>)

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
}
```

<a name="BufferedDispatcher"></a>
## type [BufferedDispatcher](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L84-L98>)

BufferedDispatcher fronts a synchronous Inner dispatcher with a bounded worker pool, so the scheduler tick is never blocked by a slow remote API call. ADR 0031: two\-phase scheduler — planning sync, dispatch async.

```go
type BufferedDispatcher struct {
    // contains filtered or unexported fields
}
```

<a name="NewBuffered"></a>
### func [NewBuffered](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L103>)

```go
func NewBuffered(inner Inner, sink FailureSink, logger *slog.Logger, metrics MetricsRecorder, cfg BufferConfig) *BufferedDispatcher
```

NewBuffered constructs a BufferedDispatcher. BufferSize=0 returns a passthrough that is byte\-for\-byte equivalent to using the inner dispatcher directly \(Lite path\). BufferSize\>0 spawns the worker pool.

<a name="BufferedDispatcher.Close"></a>
### func \(\*BufferedDispatcher\) [Close](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L174>)

```go
func (b *BufferedDispatcher) Close() error
```

Close stops accepting new dispatches, drains the in\-flight queue \(each buffered request is still processed by the inner dispatcher — or failed via the sink — so no TI is left stuck \`queued\`\), and waits for every worker to finish. Under mu so a concurrent Dispatch never sends on the closed channel. Idempotent.

The wait is bounded by cfg.DrainTimeout \(\#463\). A worker sits in the inner dispatcher with a detached context, so a remote API that accepts the connection and never answers would otherwise block shutdown forever — the process then dies by SIGKILL and loses the drain \#133 added. On expiry Close returns an error naming how many workers were abandoned; the caller logs it and proceeds with shutdown rather than hanging. The abandoned goroutines die with the process.

<a name="BufferedDispatcher.Dispatch"></a>
### func \(\*BufferedDispatcher\) [Dispatch](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L133>)

```go
func (b *BufferedDispatcher) Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
```

Dispatch hands a task off to the inner dispatcher. In passthrough mode the inner call happens inline. In buffered mode the request is enqueued non\- blockingly: success returns \(Dispatched, nil\) immediately \(the scheduler then records the TI as \`queued\`\); a full or closed channel returns \(Rejected, ErrAtCapacity\). Rejected preserves today's behavior exactly: ErrAtCapacity is a plain error, which the old scheduler classified as permanent — the bounded path that leaves the TI scheduled for the next tick.

<a name="Dispatcher"></a>
## type [Dispatcher](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L114-L151>)

Dispatcher builds executor requests for queued pod\-path tasks and runs them.

```go
type Dispatcher struct {
    // contains filtered or unexported fields
}
```

<a name="NewDispatcher"></a>
### func [NewDispatcher](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L202>)

```go
func NewDispatcher(exec executor.Executor, resolver Resolver, issuer TokenIssuer, controlAddr string, tokenTTL time.Duration) *Dispatcher
```

NewDispatcher builds a Dispatcher that launches tasks via exec, resolves their context with resolver, mints tokens with issuer \(valid for tokenTTL\), and tells the agent to reach the control plane at controlAddr.

<a name="Dispatcher.Dispatch"></a>
### func \(\*Dispatcher\) [Dispatch](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L245>)

```go
func (d *Dispatcher) Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
```

Dispatch resolves the task, mints its agent token, and executes it. The executor classifies its own dispatch outcome and returns it as an executor.Disposition \(ADR 0051 Phase 4\). A dispatcher\-INTERNAL failure that happens BEFORE Execute \(task resolve, token mint\) is permanent and so returns executor.Rejected — those bare errors classified as permanent before this change, so Rejected preserves the behavior exactly.

<a name="Dispatcher.SetAgentTLSCAConfigMap"></a>
### func \(\*Dispatcher\) [SetAgentTLSCAConfigMap](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L215>)

```go
func (d *Dispatcher) SetAgentTLSCAConfigMap(name string)
```

SetAgentTLSCAConfigMap configures the CA ConfigMap mounted into task pods so agents verify the control plane's gRPC TLS cert \(issue \#58\). Empty = the agent stays on the insecure channel \(dev\).

<a name="Dispatcher.SetAgentTokenTransport"></a>
### func \(\*Dispatcher\) [SetAgentTokenTransport](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L235>)

```go
func (d *Dispatcher) SetAgentTokenTransport(transport, audience string, expirationSeconds int64)
```

SetAgentTokenTransport selects how the agent's bearer credential reaches the task pod \(ADR 0055 Fix \#3\). transport is "" / "envvar" \(the plaintext env\-var default\) or "exchange" \(project a ServiceAccount token the agent exchanges for a task\-scoped JWT\). audience and expirationSeconds configure the projected token and are read only under the exchange transport. Ignored by the subprocess \(Lite\) executor, which has no pod.

<a name="Dispatcher.SetAttemptLifetimeCeiling"></a>
### func \(\*Dispatcher\) [SetAttemptLifetimeCeiling](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L158>)

```go
func (d *Dispatcher) SetAttemptLifetimeCeiling(ceiling time.Duration)
```

SetAttemptLifetimeCeiling wires the operator's attempt credential ceiling \(auth.max\_attempt\_credential\_lifetime\) into every dispatched request, where the Kubernetes executor floors the pod's ActiveDeadlineSeconds with it when the task declares no execution timeout. A non\-positive value is "no ceiling" and applies no floor; the subprocess executor ignores it.

<a name="Dispatcher.SetDefaultTaskServiceAccount"></a>
### func \(\*Dispatcher\) [SetDefaultTaskServiceAccount](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L197>)

```go
func (d *Dispatcher) SetDefaultTaskServiceAccount(name string)
```

SetDefaultTaskServiceAccount sets the ServiceAccount task pods run as when a DAG's task does not specify execution.service\_account. Empty leaves pods on the namespace default SA \(today's behavior\). Wiring the chart's task ServiceAccount here closes the trap where creating the SA silently had no effect until every DAG also set execution.service\_account.

<a name="Dispatcher.SetPlatformDefaults"></a>
### func \(\*Dispatcher\) [SetPlatformDefaults](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L227>)

```go
func (d *Dispatcher) SetPlatformDefaults(p PlatformDefaults)
```

SetPlatformDefaults configures the per\-cluster task defaults applied at dispatch to fill gaps the DAG artifact left empty \(ADR 0023, layer L0\).

<a name="Dispatcher.SetSecretsBackend"></a>
### func \(\*Dispatcher\) [SetSecretsBackend](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L176>)

```go
func (d *Dispatcher) SetSecretsBackend(class, kwargs string)
```

SetSecretsBackend configures the operator's external secrets backend \(ADR 0060\): the provider class the in\-pod resolver drives and its raw kwargs JSON, delivered to the pod as operator\-owned LEOFLOW\_SECRETS\_\* env. Empty leaves external secrets off \(the chain stays vault\-only\).

<a name="Dispatcher.SetTaskSecret"></a>
### func \(\*Dispatcher\) [SetTaskSecret](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L221>)

```go
func (d *Dispatcher) SetTaskSecret(name, mountPath string)
```

SetTaskSecret configures a Kubernetes Secret mounted read\-only into every task pod at mountPath, so tasks can read a credential \(e.g. a GCP service\-account key referenced by a connection's key\_path\) from the cluster's secret store rather than from Dexaflow \(ADR 0035\). Empty name = nothing mounted.

<a name="Dispatcher.SetWarmPlacer"></a>
### func \(\*Dispatcher\) [SetWarmPlacer](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L210>)

```go
func (d *Dispatcher) SetWarmPlacer(p WarmPlacer)
```

SetWarmPlacer wires the warm\-worker placement seam \(ADR 0058 N1b1\-place\). With a placer set, Dispatch tries to place an admitted attempt on a free warm worker of its dag\_version and only falls back to a dedicated pod on a warm miss. Leave it unset \(nil\) — the default — to keep dedicated pod\-per\-task, today's behavior.

<a name="FailureSink"></a>
## type [FailureSink](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L39-L41>)

FailureSink lets a worker report that an asynchronously\-dispatched task failed inside the inner dispatcher, so the scheduler can fail the TI with a clear reason. Without this callback a \`queued\` TI whose dispatch failed would sit forever \(no reaper targets \`queued\`; ADR 0031 \#128 only targets \`running\`\).

```go
type FailureSink interface {
    MarkTaskDispatchFailed(ctx context.Context, runID, taskID, reason string) error
}
```

<a name="Inner"></a>
## type [Inner](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L30-L32>)

Inner is the underlying synchronous dispatcher BufferedDispatcher wraps — matches scheduler.Dispatcher exactly so production wires through one type.

```go
type Inner interface {
    Dispatch(ctx context.Context, runID, dagID, dagVersionID string, task domain.TaskSpec) (executor.Disposition, error)
}
```

<a name="MetricsRecorder"></a>
## type [MetricsRecorder](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/buffered.go#L44-L49>)

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
## type [PlatformDefaults](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L93-L111>)

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
    // PodSecurity carries the task-pod hardening choices. It lives here, not in
    // the DAG spec, on purpose: whether untrusted task code may run as root is a
    // cluster-operator decision. Exposing it per-DAG would let an author elevate
    // their own task, which is the same self-service escalation as picking an
    // arbitrary service_account. It is populated from the cluster's configured
    // defaults (runAsNonRoot on), which an operator can override.
    PodSecurity executor.PodSecurity
}
```

<a name="Resolved"></a>
## type [Resolved](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L63-L77>)

Resolved is the execution context the dispatcher needs to launch a task.

```go
type Resolved struct {
    TaskInstanceID  string
    TenantID        string
    Image           string
    ImagePullPolicy string
    TryNumber       int
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
## type [Resolver](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L80-L82>)

Resolver loads a task instance's execution context from storage.

```go
type Resolver interface {
    ResolveTask(ctx context.Context, runID, taskID string) (Resolved, error)
}
```

<a name="TokenIssuer"></a>
## type [TokenIssuer](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L85-L87>)

TokenIssuer mints a per\-task\-instance agent token.

```go
type TokenIssuer interface {
    IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}
```

<a name="WarmPlacer"></a>
## type [WarmPlacer](<https://github.com/dexadata/leoflow/blob/main/internal/dispatch/dispatch.go#L58-L60>)

WarmPlacer hands a per\-attempt WorkAssignment to a free warm worker of a dag\_version, returning false when none is free \(ADR 0058 N1b1\-place\). It is a narrow structural view of the agentrpc worker registry: the executor package must not import agentrpc, so the seam lives here and main.go passes the registry, which satisfies it. A nil WarmPlacer on the Dispatcher means warm pools are off — every task takes the dedicated pod path, today's behavior.

```go
type WarmPlacer interface {
    Assign(dagVersionID string, a *agentv1.WorkAssignment) bool
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
