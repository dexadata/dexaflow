---
title: "internal/executor"
linkTitle: "internal/executor"
weight: 3
---

```go
import "github.com/dexadata/dexaflow/internal/executor"
```

Package executor runs task instances via Kubernetes, Docker, or a subprocess.

## Index

- [Constants](<#constants>)
- [func BuildPod\(req Request\) \*corev1.Pod](<#BuildPod>)
- [func BuildWarmPod\(spec WarmPodSpec\) \*corev1.Pod](<#BuildWarmPod>)
- [func IsAgentLost\(c AgentLostCandidate, threshold time.Duration, now time.Time\) bool](<#IsAgentLost>)
- [func IsDispatchLost\(c StaleQueuedCandidate, threshold time.Duration, now time.Time\) bool](<#IsDispatchLost>)
- [func IsOrphaned\(c ReapCandidate, threshold time.Duration, now time.Time\) bool](<#IsOrphaned>)
- [func IsPodLostCandidate\(c PodLostCandidate, grace time.Duration, now time.Time\) bool](<#IsPodLostCandidate>)
- [func OutlivedCredentialCeiling\(c AgentLostCandidate, ceiling time.Duration\) bool](<#OutlivedCredentialCeiling>)
- [func ResilienceBackstopKeys\(\) \[\]string](<#ResilienceBackstopKeys>)
- [func StagingClaimName\(dagID, runID string\) string](<#StagingClaimName>)
- [func ValidateResilienceLadder\(l ResilienceLadder\) error](<#ValidateResilienceLadder>)
- [type AgentLostCandidate](<#AgentLostCandidate>)
- [type Attempt](<#Attempt>)
- [type AttemptStopper](<#AttemptStopper>)
- [type BusyWarmWorkerSource](<#BusyWarmWorkerSource>)
- [type DecisionRecorder](<#DecisionRecorder>)
- [type DispatchLostReapStore](<#DispatchLostReapStore>)
- [type Disposition](<#Disposition>)
  - [func \(d Disposition\) String\(\) string](<#Disposition.String>)
- [type Executor](<#Executor>)
- [type HeartbeatReapStore](<#HeartbeatReapStore>)
- [type InfraConfirmer](<#InfraConfirmer>)
- [type InfraOverride](<#InfraOverride>)
- [type InfraOverrideRecorder](<#InfraOverrideRecorder>)
- [type InfraOverrideReporter](<#InfraOverrideReporter>)
- [type KubernetesExecutor](<#KubernetesExecutor>)
  - [func NewKubernetesExecutor\(clientset kubernetes.Interface, namespace string\) \*KubernetesExecutor](<#NewKubernetesExecutor>)
  - [func \(e \*KubernetesExecutor\) DeleteRunPods\(ctx context.Context, runID string\) \(Teardown, error\)](<#KubernetesExecutor.DeleteRunPods>)
  - [func \(e \*KubernetesExecutor\) DeleteTaskPod\(ctx context.Context, a Attempt\) \(Teardown, error\)](<#KubernetesExecutor.DeleteTaskPod>)
  - [func \(e \*KubernetesExecutor\) Execute\(ctx context.Context, req Request\) \(Disposition, error\)](<#KubernetesExecutor.Execute>)
  - [func \(e \*KubernetesExecutor\) GCStagingClaims\(ctx context.Context, ttl time.Duration\) error](<#KubernetesExecutor.GCStagingClaims>)
  - [func \(e \*KubernetesExecutor\) SetDecisionRecorder\(r DecisionRecorder\)](<#KubernetesExecutor.SetDecisionRecorder>)
  - [func \(e \*KubernetesExecutor\) SetLogger\(l \*slog.Logger\)](<#KubernetesExecutor.SetLogger>)
  - [func \(e \*KubernetesExecutor\) SetPodMetrics\(r PodCreationRecorder\)](<#KubernetesExecutor.SetPodMetrics>)
  - [func \(e \*KubernetesExecutor\) SetStagingStore\(s StagingStore\)](<#KubernetesExecutor.SetStagingStore>)
  - [func \(e \*KubernetesExecutor\) SetTeardownRecorder\(r DecisionRecorder\)](<#KubernetesExecutor.SetTeardownRecorder>)
  - [func \(e \*KubernetesExecutor\) TaskPodPending\(ctx context.Context, a Attempt\) \(bool, error\)](<#KubernetesExecutor.TaskPodPending>)
  - [func \(e \*KubernetesExecutor\) TaskPodPresence\(ctx context.Context, a Attempt\) \(PodPresence, error\)](<#KubernetesExecutor.TaskPodPresence>)
- [type KubernetesWarmPods](<#KubernetesWarmPods>)
  - [func NewKubernetesWarmPods\(cs kubernetes.Interface, namespace string, newSpec WarmPodSpecFunc\) \*KubernetesWarmPods](<#NewKubernetesWarmPods>)
  - [func \(k \*KubernetesWarmPods\) CreateWarmPod\(ctx context.Context, t WarmTarget, anchorName, anchorUID string\) \(string, error\)](<#KubernetesWarmPods.CreateWarmPod>)
  - [func \(k \*KubernetesWarmPods\) DeleteWarmAnchor\(ctx context.Context, dagVersionID string\) error](<#KubernetesWarmPods.DeleteWarmAnchor>)
  - [func \(k \*KubernetesWarmPods\) DeleteWarmPod\(ctx context.Context, name string\) error](<#KubernetesWarmPods.DeleteWarmPod>)
  - [func \(k \*KubernetesWarmPods\) EnsureWarmAnchor\(ctx context.Context, dagVersionID string\) \(string, error\)](<#KubernetesWarmPods.EnsureWarmAnchor>)
  - [func \(k \*KubernetesWarmPods\) LastTerminationReason\(podName string\) string](<#KubernetesWarmPods.LastTerminationReason>)
  - [func \(k \*KubernetesWarmPods\) ListWarmAnchors\(ctx context.Context\) \(\[\]string, error\)](<#KubernetesWarmPods.ListWarmAnchors>)
  - [func \(k \*KubernetesWarmPods\) ListWarmPods\(ctx context.Context\) \(\[\]WarmPodInfo, error\)](<#KubernetesWarmPods.ListWarmPods>)
- [type LadderWarning](<#LadderWarning>)
  - [func ResilienceLadderWarnings\(l ResilienceLadder\) \[\]LadderWarning](<#ResilienceLadderWarnings>)
- [type OrphanStopper](<#OrphanStopper>)
- [type OutcomeReporter](<#OutcomeReporter>)
- [type PodCreationRecorder](<#PodCreationRecorder>)
- [type PodIdentity](<#PodIdentity>)
  - [func ParseAgentIdentity\(raw string\) \(PodIdentity, error\)](<#ParseAgentIdentity>)
- [type PodInformer](<#PodInformer>)
  - [func NewPodInformer\(clientset kubernetes.Interface, namespace string\) \*PodInformer](<#NewPodInformer>)
  - [func \(p \*PodInformer\) CachedPodActive\(a Attempt\) bool](<#PodInformer.CachedPodActive>)
  - [func \(p \*PodInformer\) HasSynced\(\) bool](<#PodInformer.HasSynced>)
  - [func \(p \*PodInformer\) Shutdown\(\)](<#PodInformer.Shutdown>)
  - [func \(p \*PodInformer\) SnapshotTaskPods\(\) \(\[\]\*corev1.Pod, error\)](<#PodInformer.SnapshotTaskPods>)
  - [func \(p \*PodInformer\) Start\(ctx context.Context\)](<#PodInformer.Start>)
  - [func \(p \*PodInformer\) WaitForCacheSync\(ctx context.Context\) bool](<#PodInformer.WaitForCacheSync>)
- [type PodLostCandidate](<#PodLostCandidate>)
- [type PodLostReapStore](<#PodLostReapStore>)
- [type PodManager](<#PodManager>)
- [type PodPresence](<#PodPresence>)
  - [func \(p PodPresence\) String\(\) string](<#PodPresence.String>)
- [type PodPresenceCache](<#PodPresenceCache>)
- [type PodSecurity](<#PodSecurity>)
- [type PodSnapshotter](<#PodSnapshotter>)
- [type ProcessLiveness](<#ProcessLiveness>)
- [type ProvisionalInfraFailure](<#ProvisionalInfraFailure>)
- [type ReapCandidate](<#ReapCandidate>)
- [type ReapCursor](<#ReapCursor>)
  - [func \(c ReapCursor\) IsZero\(\) bool](<#ReapCursor.IsZero>)
- [type ReapStore](<#ReapStore>)
- [type Reaper](<#Reaper>)
  - [func NewReaper\(store ReaperStore, pods PodManager, cache PodPresenceCache, warmPods WarmPodLister, rec DecisionRecorder, logger \*slog.Logger, cfg ReaperConfig, gates ReaperGates\) \*Reaper](<#NewReaper>)
  - [func \(r \*Reaper\) ReapOnce\(ctx context.Context\) error](<#Reaper.ReapOnce>)
  - [func \(r \*Reaper\) SetAttemptLifetimeCeiling\(d time.Duration\)](<#Reaper.SetAttemptLifetimeCeiling>)
  - [func \(r \*Reaper\) SetAttemptTokenTTL\(ttl time.Duration\)](<#Reaper.SetAttemptTokenTTL>)
  - [func \(r \*Reaper\) SetInformerSynced\(fn func\(\) bool\)](<#Reaper.SetInformerSynced>)
  - [func \(r \*Reaper\) SetLastSweepCompleted\(fn func\(\) time.Time\)](<#Reaper.SetLastSweepCompleted>)
  - [func \(r \*Reaper\) SetLeaderSince\(fn func\(\) time.Time\)](<#Reaper.SetLeaderSince>)
  - [func \(r \*Reaper\) SetLogSink\(s logSink\)](<#Reaper.SetLogSink>)
  - [func \(r \*Reaper\) SetProcessLiveness\(p ProcessLiveness\)](<#Reaper.SetProcessLiveness>)
- [type ReaperConfig](<#ReaperConfig>)
  - [func DefaultReaperConfig\(\) ReaperConfig](<#DefaultReaperConfig>)
- [type ReaperGates](<#ReaperGates>)
- [type ReaperStore](<#ReaperStore>)
- [type Reconciler](<#Reconciler>)
  - [func NewReconciler\(clientset kubernetes.Interface, namespace string, reporter OutcomeReporter\) \*Reconciler](<#NewReconciler>)
  - [func \(r \*Reconciler\) LastSweepCompletedAt\(\) time.Time](<#Reconciler.LastSweepCompletedAt>)
  - [func \(r \*Reconciler\) Reconcile\(ctx context.Context\) error](<#Reconciler.Reconcile>)
  - [func \(r \*Reconciler\) SetInfraConfirmer\(c InfraConfirmer\)](<#Reconciler.SetInfraConfirmer>)
  - [func \(r \*Reconciler\) SetInfraOverrideObservers\(rec InfraOverrideRecorder, sink logSink\)](<#Reconciler.SetInfraOverrideObservers>)
  - [func \(r \*Reconciler\) SetPodSnapshotter\(s PodSnapshotter\)](<#Reconciler.SetPodSnapshotter>)
  - [func \(r \*Reconciler\) SetSettledRunCollection\(c SettledRunChecker\)](<#Reconciler.SetSettledRunCollection>)
- [type Request](<#Request>)
- [type ResilienceLadder](<#ResilienceLadder>)
- [type RunRef](<#RunRef>)
- [type ServiceAccounts](<#ServiceAccounts>)
  - [func NewServiceAccounts\(clientset kubernetes.Interface, namespace string\) \*ServiceAccounts](<#NewServiceAccounts>)
  - [func \(s \*ServiceAccounts\) ImagePullSecrets\(ctx context.Context, name string\) \(count int, found bool, err error\)](<#ServiceAccounts.ImagePullSecrets>)
- [type SettledRunChecker](<#SettledRunChecker>)
- [type StagingStore](<#StagingStore>)
- [type StaleQueuedCandidate](<#StaleQueuedCandidate>)
- [type SubprocessExecutor](<#SubprocessExecutor>)
  - [func NewSubprocessExecutor\(agentPath string, logger \*slog.Logger\) \*SubprocessExecutor](<#NewSubprocessExecutor>)
  - [func \(e \*SubprocessExecutor\) AttemptProcessAlive\(\_ context.Context, runID, taskID string, tryNumber int\) \(bool, error\)](<#SubprocessExecutor.AttemptProcessAlive>)
  - [func \(e \*SubprocessExecutor\) Execute\(ctx context.Context, req Request\) \(Disposition, error\)](<#SubprocessExecutor.Execute>)
  - [func \(e \*SubprocessExecutor\) SetPIDDir\(dir string\)](<#SubprocessExecutor.SetPIDDir>)
  - [func \(e \*SubprocessExecutor\) SetWorkDir\(dir string\)](<#SubprocessExecutor.SetWorkDir>)
  - [func \(e \*SubprocessExecutor\) StopAttempt\(ctx context.Context, runID, taskID string, tryNumber int\) \(bool, error\)](<#SubprocessExecutor.StopAttempt>)
  - [func \(e \*SubprocessExecutor\) StopOrphanedTask\(ctx context.Context, runID, taskID string, tryNumber int\) \(bool, error\)](<#SubprocessExecutor.StopOrphanedTask>)
- [type Teardown](<#Teardown>)
- [type WarmBoundTI](<#WarmBoundTI>)
- [type WarmPodCache](<#WarmPodCache>)
- [type WarmPodClient](<#WarmPodClient>)
- [type WarmPodInfo](<#WarmPodInfo>)
- [type WarmPodInformer](<#WarmPodInformer>)
  - [func NewWarmPodInformer\(clientset kubernetes.Interface, namespace string\) \(\*WarmPodInformer, error\)](<#NewWarmPodInformer>)
  - [func \(w \*WarmPodInformer\) BeginCreate\(t WarmTarget\)](<#WarmPodInformer.BeginCreate>)
  - [func \(w \*WarmPodInformer\) CachedWarmPods\(\) \(\[\]WarmPodInfo, bool\)](<#WarmPodInformer.CachedWarmPods>)
  - [func \(w \*WarmPodInformer\) Changes\(\) \<\-chan struct\{\}](<#WarmPodInformer.Changes>)
  - [func \(w \*WarmPodInformer\) Covers\(names \[\]string\) bool](<#WarmPodInformer.Covers>)
  - [func \(w \*WarmPodInformer\) CreatesPending\(dagVersionID string\) bool](<#WarmPodInformer.CreatesPending>)
  - [func \(w \*WarmPodInformer\) EndCreate\(t WarmTarget, podName string\)](<#WarmPodInformer.EndCreate>)
  - [func \(w \*WarmPodInformer\) ExpectationExpired\(\) bool](<#WarmPodInformer.ExpectationExpired>)
  - [func \(w \*WarmPodInformer\) Kick\(\)](<#WarmPodInformer.Kick>)
  - [func \(w \*WarmPodInformer\) PendingCreatesByTenant\(\) map\[string\]int](<#WarmPodInformer.PendingCreatesByTenant>)
  - [func \(w \*WarmPodInformer\) RecordTerminationsTo\(k \*KubernetesWarmPods\)](<#WarmPodInformer.RecordTerminationsTo>)
  - [func \(w \*WarmPodInformer\) Start\(ctx context.Context\)](<#WarmPodInformer.Start>)
  - [func \(w \*WarmPodInformer\) WaitForCacheSync\(ctx context.Context\) bool](<#WarmPodInformer.WaitForCacheSync>)
- [type WarmPodLister](<#WarmPodLister>)
- [type WarmPodSpec](<#WarmPodSpec>)
- [type WarmPodSpecFunc](<#WarmPodSpecFunc>)
- [type WarmPoolReconciler](<#WarmPoolReconciler>)
  - [func NewWarmPoolReconciler\(targets WarmTargetSource, pods WarmPodClient, busy BusyWarmWorkerSource, maxWarmPodsPerTenant int, logger \*slog.Logger, rec DecisionRecorder\) \*WarmPoolReconciler](<#NewWarmPoolReconciler>)
  - [func \(r \*WarmPoolReconciler\) Reconcile\(ctx context.Context\) error](<#WarmPoolReconciler.Reconcile>)
  - [func \(r \*WarmPoolReconciler\) SetCache\(c WarmPodCache, parallelism int\)](<#WarmPoolReconciler.SetCache>)
- [type WarmTarget](<#WarmTarget>)
- [type WarmTargetSource](<#WarmTargetSource>)
- [type WarmTerminationLookup](<#WarmTerminationLookup>)
- [type WarmWorkerLostReapStore](<#WarmWorkerLostReapStore>)


## Constants

<a name="DefaultAgentTokenAudience"></a>Agent\-token transport \(ADR 0055 Fix \#3\). The env\-var transport keeps the plaintext token on the pod spec \(today's behavior\); the exchange transport keeps it OFF and projects a ServiceAccount token instead.

```go
const (

    // DefaultAgentTokenAudience is the projected token's audience when the request
    // does not set one — the control plane's TokenReviewer validates the projected
    // token against this exact audience on exchange, so both sides share the const.
    DefaultAgentTokenAudience = "leoflow-control-plane"

    // AgentIdentityAnnotation carries the exact (unsanitized) task-instance identity
    // the control plane resolves a reviewed pod to on exchange. Pod labels are
    // sanitized and lossy, so the resolver reads this instead. Written only under
    // the exchange transport, so the env-var default pod spec is unchanged. It is
    // exported so the pod → task-instance resolver reads the SAME contract that
    // wrote it (single-sourced, no drift).
    AgentIdentityAnnotation = "leoflow.io/agent-identity"
)
```

<a name="WarmWorkerLabel"></a>Exported warm\-worker label contract \(ADR 0058 D1/D2\). The token\-exchange resolver reads these off a reviewed pod to decide whether it is a warm worker \(and which pool/tenant it serves\), so the SAME label BuildWarmPod stamps is the one the resolver keys on — single\-sourced, no drift. They alias the package's stamping constants above.

```go
const (
    // WarmWorkerLabel marks a pod as a warm worker; WarmWorkerLabelValue is its
    // value. A reviewed pod carrying this label is resolved to a warm-worker
    // (control-channel-only) identity rather than a task instance.
    WarmWorkerLabel      = warmWorkerLabelKey
    WarmWorkerLabelValue = warmWorkerLabelVal
    // WarmDagVersionLabel names the dag_version pool a warm worker serves.
    WarmDagVersionLabel = warmDagVersionLabelKey
    // WarmTenantLabel names the tenant that owns a warm worker's dag_version.
    WarmTenantLabel = warmTenantLabelKey
)
```

<a name="EventResilienceBackstopDisabled"></a>EventResilienceBackstopDisabled is the event attribute of the boot WARN for a ladder setting that removes a resilience backstop. An alert rule matches on it rather than on config\_key, which names the knob and not the event, so a second rung warning about the same key does not make the rule over\-match \(\#941\).

```go
const EventResilienceBackstopDisabled = "resilience_backstop_disabled"
```

<a name="ReapPageSize"></a>ReapPageSize is how many candidates a paged reaper list returns per sweep. It bounds one sweep's work after a long outage, as the old fixed LIMIT did.

```go
const ReapPageSize = 100
```

<a name="SourceAnnotation"></a>SourceAnnotation carries a source\-mode task's dag.py on its pod \(ADR 0067 §3\).

```go
const SourceAnnotation = "leoflow.io/dag-source"
```

<a name="SourceMountPath"></a>SourceMountPath is where a source\-mode task's dag.py is projected, and the task container's working directory, so \`python \-m leoflow\_runtime\` imports dag from it as Lite does from its materialized work dir.

```go
const SourceMountPath = "/opt/leoflow/source"
```

<a name="StartCredentialMargin"></a>StartCredentialMargin is how long before the attempt credential minted at dispatch runs out the dispatch\-lost reaper stops waiting for a Pending pod \(\#1126\). An agent that starts inside the margin still registers and reaches its first heartbeat, which renews the credential, before it lapses; a pod still Pending at the deadline is re\-placed instead. The resilience ladder checks it against the heartbeat and maintenance intervals.

```go
const StartCredentialMargin = 2 * time.Minute
```

<a name="BuildPod"></a>
## func [BuildPod](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L162>)

```go
func BuildPod(req Request) *corev1.Pod
```

BuildPod constructs the pod spec for a task instance. It is pure \(modulo the random name suffix\) and unit\-tested independently of any cluster.

<a name="BuildWarmPod"></a>
## func [BuildWarmPod](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpod.go#L173>)

```go
func BuildWarmPod(spec WarmPodSpec) *corev1.Pod
```

BuildWarmPod constructs the pod spec for one warm worker. It reuses BuildPod's machinery — the token transport, the CA mount, the control\-plane env, and the security contexts — but for a LONG\-LIVED worker bound to a dag\_version rather than a single task attempt. The differences from a task pod are deliberate:

- Env: LEOFLOW\_WARM\_WORKER=1 \+ LEOFLOW\_DAG\_VERSION\_ID select the agent's warm loop, and there is NO task env \(no task\-instance id, no per\-attempt token, no durable\-outcome path\) — a warm worker has no task until an attempt is pushed to it in\-band.
- Labels: a stable warm\-worker label set \(warmWorkerLabelKey \+ warmDagVersionLabelKey\) so the reconciler can list, count, and reap warm pods per version. None of the task identity labels are set.
- RestartPolicy Never: a warm worker that exits \(drain, idle recycle, or a crash\) is REPLACED by the reconciler with a fresh pod that re\-registers cleanly, rather than restarted in place with stale in\-container state. This is the clean model for the reconciler\-owned lifecycle; the D9 lifetime/ attempt caps and the idle\-TTL recycle that drive drains are deferred to N1d.

It is pure \(modulo the random name suffix\) and unit\-tested independently of any cluster. It bakes NO task token in. When the spec carries a GC anchor \(D11\) it stamps an ownerReference to that anchor ConfigMap so the pod is cascade\-GC'd on external teardown; without an anchor it builds a bare pod, unchanged.

<a name="IsAgentLost"></a>
## func [IsAgentLost](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/heartbeat_reap.go#L69>)

```go
func IsAgentLost(c AgentLostCandidate, threshold time.Duration, now time.Time) bool
```

IsAgentLost reports whether the agent has been silent long enough to be declared lost. A zero LastHeartbeat \(never reported\) is treated as alive, not lost — the TI may be inline \(no agent ever exists\), or simply has not completed its first interval yet. The reaper only fires on TIs that did heartbeat at least once and then went silent; this is the "do no harm" rule of ADR 0031. Future timestamps \(clock skew\) are treated as alive.

<a name="IsDispatchLost"></a>
## func [IsDispatchLost](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/stale_queued_reap.go#L58>)

```go
func IsDispatchLost(c StaleQueuedCandidate, threshold time.Duration, now time.Time) bool
```

IsDispatchLost reports whether a queued TI has been waiting long enough to be declared dispatch\-lost. A zero QueuedAt is treated as alive — a TI without that stamp is too poorly observed to reap defensively. Future timestamps \(clock skew\) are treated as alive. Mirrors IsAgentLost's "do no harm" rule \(ADR 0031\).

<a name="IsOrphaned"></a>
## func [IsOrphaned](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reap.go#L27>)

```go
func IsOrphaned(c ReapCandidate, threshold time.Duration, now time.Time) bool
```

IsOrphaned reports whether a running run has been quiet long enough to be declared orphaned. A zero LastActivity \(no progress signal at all\) counts as orphaned: a running run with no recorded activity since at least its started\_at is, by definition, a run nothing is touching. Future timestamps \(clock skew\) are treated as fresh — the reaper is a backstop, not a clock arbiter, so it errs on the side of leaving recent\-looking runs alone.

<a name="IsPodLostCandidate"></a>
## func [IsPodLostCandidate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_lost_reap.go#L62>)

```go
func IsPodLostCandidate(c PodLostCandidate, grace time.Duration, now time.Time) bool
```

IsPodLostCandidate reports whether a running TI has been running long enough to warrant a pod\-liveness check. A zero RunningSince is treated as alive \(too poorly observed to reap — the "do no harm" rule of ADR 0031\), and a future RunningSince \(clock skew\) is treated as alive. This gate is purely about elapsed time; the actual lost\-vs\-alive decision is the pod\-liveness check.

<a name="OutlivedCredentialCeiling"></a>
## func [OutlivedCredentialCeiling](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/heartbeat_reap.go#L104>)

```go
func OutlivedCredentialCeiling(c AgentLostCandidate, ceiling time.Duration) bool
```

OutlivedCredentialCeiling reports whether the candidate attempt went silent because its credential lapsed at the ceiling \(auth.max\_attempt\_credential\_lifetime\). Past it the control plane refuses to renew the attempt's credential, so a silent agent there is the credential lapsing as designed and the attempt fails as a task failure instead of being re\-placed as an infra loss with a fresh credential \(\#1461\).

It judges when the attempt went silent, its last heartbeat measured from its running transition, never the reap time: an agent lost shortly before the ceiling is reaped after it, and its credential never lapsed, so it stays agent\_lost. A lapse has a last heartbeat later than the ceiling less credentialCeilingSlack. A non\-positive ceiling is the operator's "no ceiling" and never matches; neither does an attempt with no recorded start or heartbeat.

<a name="ResilienceBackstopKeys"></a>
## func [ResilienceBackstopKeys](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/resilience_ladder.go#L212>)

```go
func ResilienceBackstopKeys() []string
```

ResilienceBackstopKeys lists the config keys whose setting can remove a resilience backstop, so the boot posture gauge exports a series for each, 0 while the backstop is on.

<a name="StagingClaimName"></a>
## func [StagingClaimName](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/staging.go#L46>)

```go
func StagingClaimName(dagID, runID string) string
```

StagingClaimName is the deterministic PVC name for a run's staging volume. It must be stable across retries and clear\+re\-run so the same PVC is re\-attached \(ADR 0022\), and DNS\-safe.

<a name="ValidateResilienceLadder"></a>
## func [ValidateResilienceLadder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/resilience_ladder.go#L140>)

```go
func ValidateResilienceLadder(l ResilienceLadder) error
```

ValidateResilienceLadder checks the orderings the restart recovery depends on \(see ResilienceLadder\) and reports the first violated relation, naming both sides with their values. A relation between build\-time constants can only be broken by a code change, so its error says so and asks for a bug report; the one relation involving an operator knob names the config key. All build\-time rungs must be positive. It is pure — the server calls it once at boot and refuses to start on an error, turning what used to be a comment\-level convention into an enforced invariant.

<a name="AgentLostCandidate"></a>
## type [AgentLostCandidate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/heartbeat_reap.go#L32-L56>)

AgentLostCandidate is one task instance in \`running\` whose agent may have gone silent, with the timestamp of its most recent heartbeat. The reaper compares the gap from this stamp to "now" against a stall threshold; a non\-zero gap larger than the threshold means the agent is presumed gone and the TI is failed with reason "agent\_lost".

```go
type AgentLostCandidate struct {
    TaskInstanceID string
    TenantID       string
    DagRunID       string
    DagID          string
    TaskID         string
    // TryNumber is the attempt the candidate row is on, so the reaper can
    // tear down EXACTLY that attempt's pod after failing it (#474). A retry
    // bumps try_number in place and dispatches a new pod with a new
    // try-number label, so pinning it here means a newer live attempt's pod
    // can never be deleted by mistake.
    TryNumber int
    // AttemptEpoch is the epoch the row's current execution was dispatched
    // with (ADR 0051 amendment). The mark and the pod teardown are pinned to it
    // as well as to TryNumber, so a row re-placed or re-dispatched between the
    // list and the write is a different attempt and is left alone.
    AttemptEpoch int
    // StartedAt is when the attempt entered running (zero when unknown). The
    // reaper measures LastHeartbeat from it against
    // auth.max_attempt_credential_lifetime: an attempt that went silent past the
    // ceiling stopped getting its credential renewed, so its silence is the
    // credential lapsing, not a lost agent (#1461).
    StartedAt     time.Time
    LastHeartbeat time.Time
}
```

<a name="Attempt"></a>
## type [Attempt](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_manager.go#L11-L16>)

Attempt names exactly one execution of a task instance: its run, its task, the try it is on and the attempt epoch the dispatcher claimed for it \(ADR 0051 amendment\). The try alone is not enough: an infra re\-place, a reschedule poke or a repeated dispatch starts a new execution on the same try, and only the epoch tells two pods of one try apart. A pod stamped before the epoch existed carries no epoch label and is epoch 0.

```go
type Attempt struct {
    RunID        string
    TaskID       string
    TryNumber    int
    AttemptEpoch int
}
```

<a name="AttemptStopper"></a>
## type [AttemptStopper](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/process_liveness.go#L51-L57>)

AttemptStopper is implemented by a ProcessLiveness that can stop the task of an attempt whose agent is still alive \(Lite: the subprocess executor signals the task's recorded process group\). The agent\-lost reaper uses it for an attempt still running past the credential ceiling \(auth.max\_attempt\_credential\_lifetime\), the Lite counterpart of a task pod's activeDeadlineSeconds \(\#1511\): it fails the attempt as credential\_ceiling first, then stops its task. A ProcessLiveness without it \(or none, the pod path\) leaves that attempt to the pod deadline and the silent\-agent path.

```go
type AttemptStopper interface {
    // StopAttempt stops the attempt's task (SIGTERM, a grace, then SIGKILL to
    // its whole process group) and reports true once nothing of it is left.
    // false with a nil error means nothing could be verified and nothing was
    // signaled.
    StopAttempt(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}
```

<a name="BusyWarmWorkerSource"></a>
## type [BusyWarmWorkerSource](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L84-L86>)

BusyWarmWorkerSource yields the set of warm\-worker pod names currently serving a \`running\` attempt \(ADR 0058 N1d\-b\): a warm worker is BUSY iff some \`running\` task\_instance is durably bound to it \(warm\_worker\_id = the pod's own name — the binding landed in N1d\-a1/a2\). Returned as a set keyed by pod name so the reconciler classifies each live pod in O\(1\).

Implemented on the storage side and defined HERE so the reconciler depends on a narrow capability rather than importing storage. With warm pools off no TI is ever bound, so the set is always empty and every worker classifies as idle — byte\-for\-byte today's dedicated pod\-per\-task behavior.

```go
type BusyWarmWorkerSource interface {
    ListBusyWarmWorkerPods(ctx context.Context) (map[string]bool, error)
}
```

<a name="DecisionRecorder"></a>
## type [DecisionRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L14-L16>)

DecisionRecorder records the reaper's per\-tick decision metrics. It is the narrow slice of the scheduler's metrics sink the reapers need — only the one counter — so the executor depends on a capability, not on the observability package. A nil DecisionRecorder is tolerated \(tests need not stub it\).

```go
type DecisionRecorder interface {
    RecordSchedulerDecision(decisionType string)
}
```

<a name="DispatchLostReapStore"></a>
## type [DispatchLostReapStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/stale_queued_reap.go#L68-L89>)

DispatchLostReapStore is the slice of scheduler.Store the dispatch\-lost reaper needs. The full scheduler.Store embeds this interface so production wires through one type; unit tests fake just this surface.

```go
type DispatchLostReapStore interface {
    // ListStaleQueuedCandidates returns one page of the `queued` TIs that
    // entered the queue at least threshold ago, with that timestamp, ordered
    // by (queued_at, id) and strictly after the cursor (the zero cursor is the
    // start), at most ReapPageSize rows. The threshold is applied before the
    // page limit, so attempts inside it never take the slots of those past it;
    // the reaper still re-checks it in Go.
    ListStaleQueuedCandidates(ctx context.Context, threshold time.Duration, after ReapCursor) ([]StaleQueuedCandidate, error)
    // MarkTaskDispatchLost transitions one TI to `failed` with
    // error_message='dispatch_lost'. The WHERE state='queued' guard makes
    // this idempotent: a second call on a now-non-queued TI is a no-op. It is
    // pinned to the listed (tryNumber, attemptEpoch) and returns whether a row
    // was actually updated: false means the row moved on between the list and
    // this write (its agent reported RUNNING, or a dispatch claimed a new
    // epoch), so the caller must NOT treat it as reaped (no false log, no pod
    // delete).
    MarkTaskDispatchLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
    // MarkTaskStartExpired fails one queued TI as an infra failure because its
    // pod never started before the attempt credential would run out (#1126).
    // Same guards and return as MarkTaskDispatchLost.
    MarkTaskStartExpired(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}
```

<a name="Disposition"></a>
## type [Disposition](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/dispatch_classify.go#L18>)

Disposition is the typed outcome of a dispatch attempt, returned by Executor.Execute so the scheduler can act on WHY a dispatch failed without reaching into Kubernetes error types itself. Classification lives on the execution layer — the only layer that knows how a given runtime signals backpressure — and travels up the seam as this enum \(ADR 0051 Phase 4\).

```go
type Disposition int
```

<a name="Dispatched"></a>

```go
const (
    // Dispatched means the task was handed to the runtime (a pod created, an agent
    // subprocess started). It is not a terminal outcome: the task's real result
    // arrives asynchronously over gRPC. It is the zero value, so a Request that
    // never touches classification reads as a clean hand-off.
    Dispatched Disposition = iota
    // Backpressure is transient cluster backpressure from the Kubernetes apiserver
    // — a ResourceQuota 403 or an API Priority & Fairness 429 — that clears once
    // the cluster has headroom again. The scheduler backs the task off and
    // re-offers it indefinitely, never counting it against the dispatch-attempt
    // budget and never driving the task to dispatch_failed: the cluster asking
    // Dexaflow to slow down is not the user's task failing.
    Backpressure
    // Rejected is a permanent dispatch failure that will not clear on its own: an
    // invalid image, an RBAC denial, an admission-webhook rejection, a bad spec,
    // or any error the classifier does not recognize (including every error a Lite
    // subprocess executor can return). The scheduler keeps the historical
    // bounded-backoff → dispatch_failed behavior (ADR 0031 Amendment A).
    Rejected
    // Deferred means the dispatch was never attempted: the control plane's own
    // buffered dispatch queue was full (or shutting down), or the Kubernetes
    // client's own rate limiter refused the request before sending it. Nothing reached the
    // runtime, so the scheduler leaves the task scheduled for a later tick and
    // records nothing: no backoff, no dispatch-attempt increment, no
    // dispatch_failed. It is local backpressure, not a failed dispatch.
    Deferred
    // Refused is a permanent "this task may not run here" decided before the
    // runtime is called, such as a task larger than its size under an operator
    // resource unit (ADR 0066 §3). A retry cannot change the verdict, so the
    // scheduler fails the task on the first attempt, with the message as its
    // reason, and spends no dispatch retries on it. Unlike Rejected, which also
    // covers errors that clear on retry, Refused is never retried.
    Refused
    // Unavailable means the Kubernetes API could not take the request: a 503,
    // a 504, a 500 (such as a fail-closed admission webhook that is down), a
    // connection refused or reset, or the client deadline (#1606). That is a
    // control plane outage (an upgrade, an etcd leader change), not a verdict
    // on the task, so the scheduler re-offers it without spending the
    // dispatch-attempt budget. Unlike Backpressure it is bounded: the task
    // fails as dispatch_failed once the API has been unavailable to it for
    // longer than the scheduler's window (ADR 0093). The create may have
    // reached the apiserver before the response was lost; retrying is still
    // safe because the re-offer and every dispatch bump the attempt epoch,
    // which fences a pod the lost create left behind: its agent's RUNNING
    // report is refused before user code starts.
    Unavailable
)
```

<a name="Disposition.String"></a>
### func \(Disposition\) [String](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/dispatch_classify.go#L69>)

```go
func (d Disposition) String() string
```

String renders the disposition for logs and error notes.

<a name="Executor"></a>
## type [Executor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/executor.go#L173-L175>)

Executor runs or dispatches a task. For asynchronous executors \(Kubernetes/Docker/subprocess\) the return reflects dispatch only, and the agent reports the final state over gRPC. The Disposition tells the scheduler WHY a dispatch failed \(transient cluster Backpressure, an Unavailable API, a permanent Rejected, among others\), so the orchestration layer never has to inspect runtime\-specific error types itself \(ADR 0051 Phase 4\). A successful dispatch returns \(Dispatched, nil\); a failure returns the classified disposition alongside the non\-nil cause \(its text feeds the scheduler's note/log\).

```go
type Executor interface {
    Execute(ctx context.Context, req Request) (Disposition, error)
}
```

<a name="HeartbeatReapStore"></a>
## type [HeartbeatReapStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/heartbeat_reap.go#L114-L131>)

HeartbeatReapStore is the slice of scheduler.Store the TI heartbeat reaper needs. The full scheduler.Store embeds this interface so production wires through one type; unit tests fake just this surface.

```go
type HeartbeatReapStore interface {
    // ListAgentLostCandidates returns every `running` TI whose last heartbeat
    // is non-null (it has heartbeated at least once). The reaper applies the
    // threshold per candidate so the SQL stays simple and the decision is
    // purely in Go.
    ListAgentLostCandidates(ctx context.Context) ([]AgentLostCandidate, error)
    // MarkTaskAgentLost transitions one TI to `failed` with
    // error_message='agent_lost'. The WHERE state='running' guard makes this
    // idempotent. It returns whether a row was actually updated: false means a
    // late terminal report transitioned the TI between the list and this write,
    // so the caller must NOT treat it as reaped (no false log, no pod delete).
    MarkTaskAgentLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
    // MarkTaskCredentialCeiling transitions one TI to `failed` with the
    // credential_ceiling reason as a TASK failure (no infra kind), for an
    // attempt that outlived auth.max_attempt_credential_lifetime (#1461). Same
    // guards and return contract as MarkTaskAgentLost.
    MarkTaskCredentialCeiling(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}
```

<a name="InfraConfirmer"></a>
## type [InfraConfirmer](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_confirm.go#L31-L34>)

InfraConfirmer is the store surface of the confirmation pass: list the provisional marks, and confirm one, guarded on its exact attempt. The scheduler store implements it.

```go
type InfraConfirmer interface {
    ListProvisionalInfraFailures(ctx context.Context) ([]ProvisionalInfraFailure, error)
    ConfirmInfraFailure(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}
```

<a name="InfraOverride"></a>
## type [InfraOverride](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_override.go#L14-L20>)

InfraOverride describes a durable SUCCESS settled over a reaper's infra mark: the mark it overrode \(agent\_lost, pod\_lost or dispatch\_lost\) and the attempt's log location, for the system line that records it.

```go
type InfraOverride struct {
    Mark     string
    TenantID string
    DagID    string
    DagRunID string
    TaskID   string
}
```

<a name="InfraOverrideRecorder"></a>
## type [InfraOverrideRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_override.go#L32-L34>)

InfraOverrideRecorder counts overrides by the mark they overrode.

```go
type InfraOverrideRecorder interface {
    RecordInfraOverride(mark string)
}
```

<a name="InfraOverrideReporter"></a>
## type [InfraOverrideReporter](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_override.go#L26-L29>)

InfraOverrideReporter is implemented by an OutcomeReporter that can tell a SUCCESS settle that changed no row apart, and settle a durable SUCCESS over a provisional infra mark of the same attempt \(ADR 0052 amendment, part 1\). The reconciler uses it when the reporter implements it.

```go
type InfraOverrideReporter interface {
    SucceedTaskIfActive(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
    SucceedTaskOverInfraMark(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (InfraOverride, bool, error)
}
```

<a name="KubernetesExecutor"></a>
## type [KubernetesExecutor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L26-L40>)

KubernetesExecutor runs each task as an ephemeral pod \(ADR 0002\).

```go
type KubernetesExecutor struct {
    // contains filtered or unexported fields
}
```

<a name="NewKubernetesExecutor"></a>
### func [NewKubernetesExecutor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L86>)

```go
func NewKubernetesExecutor(clientset kubernetes.Interface, namespace string) *KubernetesExecutor
```

NewKubernetesExecutor builds an executor creating pods in the given namespace.

<a name="KubernetesExecutor.DeleteRunPods"></a>
### func \(\*KubernetesExecutor\) [DeleteRunPods](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_terminate.go#L131>)

```go
func (e *KubernetesExecutor) DeleteRunPods(ctx context.Context, runID string) (Teardown, error)
```

DeleteRunPods deletes every task pod belonging to a single reaped run. The orphan\-run reaper abandons a whole run \(failing all its still\-active TIs\), so every pod of that run must be torn down. The run\-id is a unique per\-run UUID, so this selector can only ever match pods of the one abandoned run — never a different run's live pod. The terminal\-phase skip applies per pod inside the run, not just to the per\-attempt delete above \(\#928\): this reaper reads no presence at all, so a run abandoned at the 5\-minute threshold would otherwise take every finished task's outcome record with it. A mixed set is safe because each settle is guarded on the pod's own try\-number, ReapRun has already flipped the run and every still\-active task instance in one transaction before this runs, and pod names carry a random suffix so a preserved pod can never collide with a redispatch. The subtlest cell is a reschedule poke pod, which the reconciler collects immediately rather than on age because a reschedule reuses the same try\-number: preserving one delays that collect by up to a cycle, which is harmless because up\_for\_reschedule is not an active state for any reaper, so a reaper only ever preserves a poke pod for an attempt it has just made terminal. Tolerates NotFound.

<a name="KubernetesExecutor.DeleteTaskPod"></a>
### func \(\*KubernetesExecutor\) [DeleteTaskPod](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_terminate.go#L56>)

```go
func (e *KubernetesExecutor) DeleteTaskPod(ctx context.Context, a Attempt) (Teardown, error)
```

DeleteTaskPod deletes the pod\(s\) for exactly one reaped attempt: the \(run, task, try, epoch\) tuple. Pinning try\-number and the attempt epoch is the invariant guard. A retry bumps try\_number in place and dispatches a new pod with a new try\-number label; every other rail that starts a new execution of the row \(an infra re\-place, a reschedule redispatch, a warm requeue\) and every dispatch bumps attempt\_epoch, and the new pod carries the new epoch label \(ADR 0051 amendment\). So a newer live attempt can never match and is never deleted \(\#901\). A pod already in a terminal phase is left for the reconciler \(\#928\). Tolerates NotFound.

<a name="KubernetesExecutor.Execute"></a>
### func \(\*KubernetesExecutor\) [Execute](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L100>)

```go
func (e *KubernetesExecutor) Execute(ctx context.Context, req Request) (Disposition, error)
```

Execute creates the task pod. The agent inside the pod reports state over gRPC. A dispatch failure is classified on this layer — where the apiserver's error types are known — into transient Backpressure \(a ResourceQuota 403 or an APF 429\), an Unavailable API \(a 5xx, a connection refused or reset, the client deadline; ADR 0093\) or a permanent Rejected, so the scheduler acts on the disposition without importing Kubernetes error types \(ADR 0051 Phase 4\). The cause is returned alongside so its text still feeds the scheduler's note/log.

<a name="KubernetesExecutor.GCStagingClaims"></a>
### func \(\*KubernetesExecutor\) [GCStagingClaims](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/staging.go#L116>)

```go
func (e *KubernetesExecutor) GCStagingClaims(ctx context.Context, ttl time.Duration) error
```

GCStagingClaims reclaims per\-run staging PVCs from the metadatabase\-tracked lifecycle \(ADR 0022\): a successful run frees its volume immediately; a failed run keeps it until ttl elapses after the run's terminal time \(clear\+re\-run safety\); an orphaned volume \(run row gone\) is reclaimed. Each deletion is recorded with its reason. In addition, unrecorded orphan staging PVCs left in Kubernetes are swept by label once ttl elapses \(\#1615\). A no\-op when no StagingStore is wired.

<a name="KubernetesExecutor.SetDecisionRecorder"></a>
### func \(\*KubernetesExecutor\) [SetDecisionRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L61>)

```go
func (e *KubernetesExecutor) SetDecisionRecorder(r DecisionRecorder)
```

SetDecisionRecorder wires the recorder that meters decisions and errors \(such as staging\_pvc\_delete\_error\).

<a name="KubernetesExecutor.SetLogger"></a>
### func \(\*KubernetesExecutor\) [SetLogger](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L64>)

```go
func (e *KubernetesExecutor) SetLogger(l *slog.Logger)
```

SetLogger wires the logger used for executor operational messages.

<a name="KubernetesExecutor.SetPodMetrics"></a>
### func \(\*KubernetesExecutor\) [SetPodMetrics](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L52>)

```go
func (e *KubernetesExecutor) SetPodMetrics(r PodCreationRecorder)
```

SetPodMetrics wires the recorder that meters task pod creates \(dexaflow\_pods\_created\_total, dexaflow\_task\_pod\_creation\_duration\_seconds\).

<a name="KubernetesExecutor.SetStagingStore"></a>
### func \(\*KubernetesExecutor\) [SetStagingStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L83>)

```go
func (e *KubernetesExecutor) SetStagingStore(s StagingStore)
```

SetStagingStore wires the metadatabase\-backed staging\-volume lifecycle store \(ADR 0022\). With no store set, provisioning is not recorded and GC is a no\-op.

<a name="KubernetesExecutor.SetTeardownRecorder"></a>
### func \(\*KubernetesExecutor\) [SetTeardownRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L57>)

```go
func (e *KubernetesExecutor) SetTeardownRecorder(r DecisionRecorder)
```

SetTeardownRecorder wires the recorder that meters reap\_teardown\_delete\_fallback: a started pod a reap could not stop in place \(the patch was refused\) and deleted instead.

<a name="KubernetesExecutor.TaskPodPending"></a>
### func \(\*KubernetesExecutor\) [TaskPodPending](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_terminate.go#L305>)

```go
func (e *KubernetesExecutor) TaskPodPending(ctx context.Context, a Attempt) (bool, error)
```

TaskPodPending reports whether exactly the \(run, task, try, epoch\) attempt has a Pending pod and no Running one, i.e. its agent has not started yet \(image pull, node provisioning, init containers\). Selector and epoch are pinned exactly as in TaskPodPresence. An error reports nothing pending, and the reaper defers on the error itself.

<a name="KubernetesExecutor.TaskPodPresence"></a>
### func \(\*KubernetesExecutor\) [TaskPodPresence](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_terminate.go#L350>)

```go
func (e *KubernetesExecutor) TaskPodPresence(ctx context.Context, a Attempt) (PodPresence, error)
```

TaskPodPresence reports what the apiserver holds for exactly the \(run, task, try, epoch\) attempt: a live pod \(Pending/Running\), a present\-but\-finished pod, or no pod at all. The dispatch\-lost and pod\-lost reapers consult this before failing a TI: a live pod means the dispatch actually landed and the node is merely slow to pull the image \(\#461\), so the reaper must DEFER.

A present\-but\-finished pod is reported apart from an absence on purpose. The pod object still carries the attempt's outcome \(the termination log the reconciler recovers a durable result from\), so it is the reconciler's to settle and no reaper may delete it; only a genuine absence means the attempt is lost with nothing left to recover. Any live pod for the attempt wins over a lingering finished sibling, since work may still be running.

Try\-number is pinned — the same invariant guard as DeleteTaskPod above. The retry rail resets up\_for\_retry \-\> none with try\_number\+1 and the planner re\-queues the TI \(storage/queries/runs.sql\), so a \`queued\`/\`running\` TI can be on try 2 while try 1's pod still lingers Pending after a failed best\-effort delete. Selecting on \(run, task\) alone would match that stale older pod and false\-defer the reap of the current attempt forever \(\#723\). Asking about the attempt the reaper is about to fail is the correct liveness question. The epoch is pinned for the same reason \(ADR 0051 amendment\): an infra re\-place keeps the try, so a lingering pod of the superseded execution must not defer the reap of its replacement.

<a name="KubernetesWarmPods"></a>
## type [KubernetesWarmPods](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L25-L34>)

KubernetesWarmPods is the production WarmPodClient: it lists warm pods by the warm\-worker label, builds new ones via the injected spec func \+ BuildWarmPod, and deletes them, all in one namespace. It owns the label selector so the executor's warm\-worker label contract stays private to this package.

```go
type KubernetesWarmPods struct {
    // contains filtered or unexported fields
}
```

<a name="NewKubernetesWarmPods"></a>
### func [NewKubernetesWarmPods](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L38>)

```go
func NewKubernetesWarmPods(cs kubernetes.Interface, namespace string, newSpec WarmPodSpecFunc) *KubernetesWarmPods
```

NewKubernetesWarmPods builds the cluster\-backed warm\-pod client. newSpec is the auth/config\-aware builder invoked per create; List and Delete do not need it.

<a name="KubernetesWarmPods.CreateWarmPod"></a>
### func \(\*KubernetesWarmPods\) [CreateWarmPod](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L130>)

```go
func (k *KubernetesWarmPods) CreateWarmPod(ctx context.Context, t WarmTarget, anchorName, anchorUID string) (string, error)
```

CreateWarmPod mints the target's warm\-pod spec, builds the pod, and creates it. anchorName/anchorUID identify the version's GC\-anchor ConfigMap \(ADR 0058 D11\); when non\-empty they are threaded onto the spec so BuildWarmPod stamps the pod's ownerReference to the anchor. The reconciler ensures the anchor and reads its UID before any create, so both are populated on the live path; a caller that passes them empty gets a bare pod, unchanged.

<a name="KubernetesWarmPods.DeleteWarmAnchor"></a>
### func \(\*KubernetesWarmPods\) [DeleteWarmAnchor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L196>)

```go
func (k *KubernetesWarmPods) DeleteWarmAnchor(ctx context.Context, dagVersionID string) error
```

DeleteWarmAnchor deletes the per\-dag\-version GC\-anchor ConfigMap \(ADR 0058 D11\), tolerating NotFound \(already gone / never created\). The reconciler calls this ONLY for a fully\-drained inactive version \(zero live pods\), so the cascade the ownerReference sets up is a no\-op — it can never kill a live warm attempt.

<a name="KubernetesWarmPods.DeleteWarmPod"></a>
### func \(\*KubernetesWarmPods\) [DeleteWarmPod](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L150>)

```go
func (k *KubernetesWarmPods) DeleteWarmPod(ctx context.Context, name string) error
```

DeleteWarmPod removes one warm worker by name.

<a name="KubernetesWarmPods.EnsureWarmAnchor"></a>
### func \(\*KubernetesWarmPods\) [EnsureWarmAnchor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L165>)

```go
func (k *KubernetesWarmPods) EnsureWarmAnchor(ctx context.Context, dagVersionID string) (string, error)
```

EnsureWarmAnchor ensures the per\-dag\-version GC\-anchor ConfigMap exists and returns its UID \(ADR 0058 D11\). The anchor owns the version's warm pods via an ownerReference, so on control\-plane loss / namespace teardown the pods are cascade\-GC'd — the orphan class the reconciler\-as\-deleter cannot cover. It is create\-then\-read and idempotent: an AlreadyExists \(a prior tick, or another leader\) is success, and the UID is read back with a GET so every pod created this tick is stamped with the SAME owner UID. The anchor carries no data \(empty ConfigMap\); the labels only make it discoverable.

<a name="KubernetesWarmPods.LastTerminationReason"></a>
### func \(\*KubernetesWarmPods\) [LastTerminationReason](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L89>)

```go
func (k *KubernetesWarmPods) LastTerminationReason(podName string) string
```

LastTerminationReason returns the warm\-worker container's terminated reason last observed for the named warm pod \(for example OOMKilled\), or "" when none was observed within the last warmTerminationMemory. It still answers after the pod is deleted, so the warm\-worker\-lost reaper can name an OOM kill of a worker the LIST no longer returns \(\#1835\).

<a name="KubernetesWarmPods.ListWarmAnchors"></a>
### func \(\*KubernetesWarmPods\) [ListWarmAnchors](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L52>)

```go
func (k *KubernetesWarmPods) ListWarmAnchors(ctx context.Context) ([]string, error)
```

ListWarmAnchors returns the dag\_version \(its label\) of every warm\-pool GC anchor in the namespace \(\#1500\). An anchor without the label reads "" and is skipped by the reconciler, never deleted on a guess.

<a name="KubernetesWarmPods.ListWarmPods"></a>
### func \(\*KubernetesWarmPods\) [ListWarmPods](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L71>)

```go
func (k *KubernetesWarmPods) ListWarmPods(ctx context.Context) ([]WarmPodInfo, error)
```

ListWarmPods returns every warm\-worker pod in the namespace, tagged with the dag\_version it serves \(from its label\). It remembers the terminated reason of each terminal one, for LastTerminationReason.

<a name="LadderWarning"></a>
## type [LadderWarning](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/resilience_ladder.go#L254-L268>)

LadderWarning is one boot WARN about a ladder setting that is valid but removes a resilience backstop: the operator\-readable sentence plus the two things a monitoring rule needs as FIELDS. Logging only a pre\-formatted string leaves the JSON record with nothing but msg, so the single alert an operator would actually want — "some instance booted with the credential ceiling disabled" — can only be written as a substring match on prose that a later reword silently breaks \(\#924\). Msg stays self\-contained \(it names the key and the value too\) so a plain\-text log needs no field expansion to be read. Every rung of the ladder is a duration, so Value is one.

```go
type LadderWarning struct {
    // Msg is the human-readable sentence: what is off and what it costs.
    Msg string
    // Event names what happened, a stable low-cardinality constant
    // (EventResilienceBackstopDisabled), while Key distinguishes the rungs.
    Event string
    // Key is the config key an operator would set to restore the backstop —
    // the attribute an alert rule matches on.
    Key string
    // Value is the offending setting as configured. slog's JSON handler renders
    // a duration as an integer nanosecond count, so an alert rule matches it
    // numerically (value <= 0 for a disabled ceiling), not as "0s" — that
    // spelling is the text handler's.
    Value time.Duration
}
```

<a name="ResilienceLadderWarnings"></a>
### func [ResilienceLadderWarnings](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/resilience_ladder.go#L231>)

```go
func ResilienceLadderWarnings(l ResilienceLadder) []LadderWarning
```

ResilienceLadderWarnings reports the ladder settings that are valid but remove a resilience backstop, so the server can surface them as boot WARNs. ValidateResilienceLadder deliberately accepts a non\-positive credential ceiling — it is the operator's documented "no ceiling" setting — but that one value disables every wall\-clock bound the ceiling carries: heartbeat renewal of an attempt's bearer becomes unbounded; a dedicated task pod whose DAG declares no execution timeout gets no ActiveDeadlineSeconds floor; and, with warm pools enabled, the per\-attempt watchdog that keeps a wedged attempt from pinning a warm slot is off too \(a warm pod has no pod\-level deadline at all, and the worker lifetime cap drains between attempts, never mid\-attempt\). A task that wedges while still heartbeating then has no bound of its own even with a healthy control plane: the orphan\-run reaper skips a run with a live task instance and agent\-lost never fires on a live agent. None of these losses is an error; all are invisible without this signal. Pure, like the validator: the server calls it once at boot after the logger exists.

<a name="OrphanStopper"></a>
## type [OrphanStopper](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/process_liveness.go#L34-L41>)

OrphanStopper is implemented by a ProcessLiveness that can stop a task left running by an agent that died \(Lite: the task's process group outlives an agent killed outright, \#916\). The reapers call it for an attempt that reads alive, before deferring it, so the infra re\-place that follows a reap never starts a second copy beside an orphan.

```go
type OrphanStopper interface {
    // StopOrphanedTask stops the attempt's task when its agent is confirmed
    // dead and the task is verifiably the one that agent started, and reports
    // true once nothing of it is left. false with a nil error means nothing was
    // stopped (the agent is alive, or the task could not be verified) and the
    // attempt must still be treated as alive.
    StopOrphanedTask(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}
```

<a name="OutcomeReporter"></a>
## type [OutcomeReporter](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L541-L550>)

OutcomeReporter records a terminal task\-instance outcome the reconciler recovered from a pod \(its durable outcome record, or its phase\). Every method is guarded by the attempt \(try\_number and attempt\_epoch, from the pod's labels\) so a stale reconciler acting on a previous attempt's pod never clobbers a live retry or re\-place \(\#1130\), and is idempotent: a settle on an already\-terminal or superseded instance is a no\-op, not an error.

```go
type OutcomeReporter interface {
    FailTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string) error
    SucceedTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) error
    RescheduleTask(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, at time.Time) error
    // FailTaskInfra records the attempt as failed for an infrastructure reason
    // (last_failure_kind 'infra', confirmed at once: the terminal pod is the
    // confirmation), so the planner re-places it on the infra budget instead of
    // spending a task retry (ADR 0051, #1595).
    FailTaskInfra(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int, reason string) error
}
```

<a name="PodCreationRecorder"></a>
## type [PodCreationRecorder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L44-L48>)

PodCreationRecorder meters task pod creates. observability.Metrics implements it.

```go
type PodCreationRecorder interface {
    // ObservePodCreation records one pod create for dagID: result is "success"
    // or "error" (ADR 0010), d the API call's latency.
    ObservePodCreation(dagID, result string, d time.Duration)
}
```

<a name="PodIdentity"></a>
## type [PodIdentity](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L429-L440>)

PodIdentity is the JSON payload of AgentIdentityAnnotation: the full task\-instance identity the control plane mints the exchanged JWT for.

```go
type PodIdentity struct {
    TaskInstanceID string `json:"ti"`
    TenantID       string `json:"tenant"`
    DagID          string `json:"dag"`
    RunID          string `json:"run"`
    TaskID         string `json:"task"`
    TryNumber      int    `json:"try"`
    // AttemptEpoch is the execution of TryNumber this pod runs (ADR 0051
    // amendment). nil on a pod created before the epoch existed, which the
    // exchange then mints a legacy token for.
    AttemptEpoch *int `json:"epoch,omitempty"`
}
```

<a name="ParseAgentIdentity"></a>
### func [ParseAgentIdentity](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/kubernetes.go#L445>)

```go
func ParseAgentIdentity(raw string) (PodIdentity, error)
```

ParseAgentIdentity decodes the AgentIdentityAnnotation payload. It is the read side of the identity contract mountAgentToken writes, used by the pod → task\-instance resolver on the exchange path.

<a name="PodInformer"></a>
## type [PodInformer](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L62-L69>)

PodInformer is a shared\-informer read\-path over task pods, scoped by namespace and the leoflow.io/run\-id label \(ADR 0002 pods\). It replaces the reapers' per\-running\-TI\-per\-second apiserver LIST storm and the reconciler's 30s LIST with one long\-lived watch feeding a local cache.

Its readings are trusted only in the safe direction \(\#461\): CachedPodActive is consulted ONLY to DEFER a reap when a pod is present and Pending/Running — a cache "absent" reading is never authoritative and callers MUST fall through to the live TaskPodPresence \(quorum\) read before any destructive action. Cache lag can therefore only ever delay a reap by a tick, never cause a false\-positive one. SnapshotTaskPods is safe for the reconciler because presence of a terminal pod is monotonic and every settle is attempt/state\-guarded \(ADR 0052\).

It is constructed only in the scheduler/all role \(ADR 0049\), never api\-only, and is nil in Lite/subprocess \(no pods\), where consumers keep their live paths.

```go
type PodInformer struct {
    // contains filtered or unexported fields
}
```

<a name="NewPodInformer"></a>
### func [NewPodInformer](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L75>)

```go
func NewPodInformer(clientset kubernetes.Interface, namespace string) *PodInformer
```

NewPodInformer builds a shared pod informer over the given cluster, scoped to namespace and to pods carrying the leoflow.io/run\-id label \(managed task pods\). Resync is 0: reads are level\-triggered on demand, so there are no logic\-bearing handlers to re\-fire. It does not start watching until Start is called.

<a name="PodInformer.CachedPodActive"></a>
### func \(\*PodInformer\) [CachedPodActive](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L151>)

```go
func (p *PodInformer) CachedPodActive(a Attempt) bool
```

CachedPodActive reports whether the cache holds a pod for exactly the \(run, task, try, epoch\) attempt that is Pending or Running: the exact predicate TaskPodPresence uses, pinned to the same attempt \(\#723, ADR 0051 amendment\). It is the safe direction of the asymmetric\-trust contract: a true return may DEFER a reap; a false return is NEVER authoritative and the caller must fall through to the live read. Before the cache has synced it returns false, so a cold cache degrades to the live path rather than misreporting absence.

<a name="PodInformer.HasSynced"></a>
### func \(\*PodInformer\) [HasSynced](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L132>)

```go
func (p *PodInformer) HasSynced() bool
```

HasSynced reports whether the initial LIST has populated the cache. It is the live form of WaitForCacheSync's one\-shot answer, for a caller that must keep asking — the reaper's leader\-settling gate — so a cache that synced late \(a watch recovered after an RBAC fix\) is seen, and one that never synced is not mistaken for an empty cluster.

<a name="PodInformer.Shutdown"></a>
### func \(\*PodInformer\) [Shutdown](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L138>)

```go
func (p *PodInformer) Shutdown()
```

Shutdown stops the watch and waits for the informer goroutines to exit. Safe to call more than once \(Start's ctx\-cancel path and an explicit caller may race\).

<a name="PodInformer.SnapshotTaskPods"></a>
### func \(\*PodInformer\) [SnapshotTaskPods](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L202>)

```go
func (p *PodInformer) SnapshotTaskPods() ([]*corev1.Pod, error)
```

SnapshotTaskPods returns the managed task pods currently in the cache — the reconciler's read replacement for its 30s LIST. It errors \(errCacheNotSynced\) before the initial sync so the reconciler retries next tick instead of acting on a cold cache that looks empty.

<a name="PodInformer.Start"></a>
### func \(\*PodInformer\) [Start](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L111>)

```go
func (p *PodInformer) Start(ctx context.Context)
```

Start begins the watch in the background and stops it when ctx is canceled, so the informer is always\-on for the process lifetime \(warming the cache before leadership\). It is idempotent\-safe to call once per informer.

<a name="PodInformer.WaitForCacheSync"></a>
### func \(\*PodInformer\) [WaitForCacheSync](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_informer.go#L123>)

```go
func (p *PodInformer) WaitForCacheSync(ctx context.Context) bool
```

WaitForCacheSync blocks until the initial LIST has populated the cache or ctx is canceled, returning whether the sync completed. A false return \(canceled or timed out\) is not fatal: CachedPodActive independently gates on HasSynced and returns false until warm, so consumers simply keep using their live read paths.

<a name="PodLostCandidate"></a>
## type [PodLostCandidate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_lost_reap.go#L19-L41>)

PodLostCandidate is one task instance in \`running\` whose backing pod may have vanished — deleted, evicted, OOM\-killed, or lost with its node — before any other reaper could catch it. RunningSince is when the TI entered \`running\`; the reaper only checks pod liveness once the TI has been running past a grace period, so a just\-dispatched TI whose pod is still materializing is never reaped on a transient "no pod yet".

```go
type PodLostCandidate struct {
    TaskInstanceID string
    DagRunID       string
    DagID          string
    TaskID         string
    // TryNumber pins the best-effort pod delete to exactly this attempt, so a
    // retry's newer pod is never touched (same invariant as the #474 teardown).
    TryNumber int
    // AttemptEpoch is the epoch the row's current execution was dispatched
    // with (ADR 0051 amendment). The mark and the pod teardown are pinned to it
    // as well as to TryNumber, so a row re-placed or re-dispatched between the
    // list and the write is a different attempt and is left alone.
    AttemptEpoch int
    RunningSince time.Time
    // Heartbeated reports whether the attempt has heartbeated at least once.
    // Pod-lost ignores it; Lite uses it to judge an agent that died before its
    // first heartbeat, which agent-lost never lists (#916).
    Heartbeated bool
    // TenantID locates the attempt's log stream for the killed marker: pod_lost
    // on Kubernetes, and the agent_lost one Lite writes when it fails a
    // never-heartbeated attempt.
    TenantID string
}
```

<a name="PodLostReapStore"></a>
## type [PodLostReapStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_lost_reap.go#L72-L88>)

PodLostReapStore is the slice of scheduler.Store the pod\-lost reaper needs. The full scheduler.Store embeds this interface so production wires through one type; unit tests fake just this surface.

```go
type PodLostReapStore interface {
    // ListRunningTasks returns one page of the `running` TIs that have been
    // running for at least grace, with the timestamp each entered running,
    // ordered by (started_at, id) and strictly after the cursor (the zero
    // cursor is the start), at most ReapPageSize rows. Warm-pool attempts are
    // excluded: they have no per-task pod, so a presence check would always
    // read them as lost, and the warm-worker-lost reaper owns them. The grace
    // filter is applied before the page limit, so attempts still inside it
    // never take the slots of those the reaper can act on.
    ListRunningTasks(ctx context.Context, grace time.Duration, after ReapCursor) ([]PodLostCandidate, error)
    // MarkTaskPodLost transitions one TI to `failed` with
    // error_message='pod_lost'. The WHERE state='running' guard makes this
    // idempotent. It returns whether a row was actually updated: false means a
    // late terminal report transitioned the TI between the list and this write,
    // so the caller must NOT treat it as reaped (no false log, no pod delete).
    MarkTaskPodLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}
```

<a name="PodManager"></a>
## type [PodManager](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_manager.go#L32-L70>)

PodManager is the slice of the Kubernetes executor the reapers use to \(1\) tear down a reaped task's pod and \(2\) check whether a queued TI's pod is actually live before declaring its dispatch lost \(\#474, \#461\).

Before \#474 the reapers only wrote DB state, so a reaped TI's pod kept running to completion — breaking at\-most\-once execution. The reapers now call these AFTER the durable DB transition so the pod is actually stopped.

It is nil in Lite/subprocess, where tasks are host processes with no pods. Every call site guards the nil: a nil PodManager means "no pods to manage", and the dispatch\-lost reaper falls back to its pure time\-threshold behavior.

The Kubernetes executor implements this; the interface lives here so the scheduler depends on a capability, not on the executor package.

```go
type PodManager interface {
    // DeleteTaskPod deletes the pod for exactly one reaped attempt: the
    // (run, task, try, epoch) tuple. Pinning try-number and the attempt epoch
    // guarantees a newer live attempt (a retry on a new try, or an infra
    // re-place on the same try with a new epoch) is never deleted (#901). Each
    // pod is deleted by name with a UID precondition, so a pod the call did not
    // list is never acted on. Tolerates a missing pod.
    //
    // A pod already in a terminal phase is SKIPPED, not deleted (#928): the
    // teardown exists to stop a running container, a terminal pod has none, and
    // its termination message is the outcome record the reconciler settles the
    // attempt from (ADR 0052). That is why a reaper may call this unconditionally
    // after its mark without reading presence first — the guard is at the delete
    // site, so it holds for every reaper including the ones (agent-lost,
    // orphan-run) that read no presence at all.
    //
    // The Teardown result counts the pods skipped that way, so each reaper can
    // record <reaper>_terminal_pod_preserved through its own recorder (#933).
    DeleteTaskPod(ctx context.Context, a Attempt) (Teardown, error)
    // DeleteRunPods deletes every task pod of one reaped run. Used by the
    // orphan-run reaper, which abandons the whole run. The run-id is unique
    // per run, so no other run's pod can match. Tolerates missing pods. The
    // terminal-phase skip above applies per pod within the run (#928).
    DeleteRunPods(ctx context.Context, runID string) (Teardown, error)
    // TaskPodPresence reports what the apiserver holds for exactly the
    // (run, task, try, epoch) attempt: a live pod, a present-but-finished pod,
    // or nothing. A reaper defers on a live pod: the dispatch landed, the node
    // is merely slow (#461). Try-number and epoch are pinned so a retried or
    // re-placed TI's liveness gate asks about the attempt it is about to fail,
    // not any older attempt whose pod may still linger (#723).
    TaskPodPresence(ctx context.Context, a Attempt) (PodPresence, error)
    // TaskPodPending reports whether exactly the (run, task, try, epoch)
    // attempt has a Pending pod and no Running one: its agent has not started
    // (image pull, node provisioning, init containers). The dispatch-lost
    // reaper asks it only for a live pod past the start deadline, to tell an
    // agent that can no longer start with a live credential from one that
    // already did (#1126).
    TaskPodPending(ctx context.Context, a Attempt) (bool, error)
}
```

<a name="PodPresence"></a>
## type [PodPresence](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_manager.go#L98>)

PodPresence is the three\-way answer to "what does the apiserver hold for this attempt's pod?".

The distinction is load\-bearing, not cosmetic. A bare bool collapsed the last two states, and the pod\-lost reaper read that collapse as authorization to reap: for a task pod that had already finished it marked the task instance pod\_lost and then DELETED the pod — destroying the termination\-log evidence the reconciler recovers the task's durable outcome from. A finished task then reads as failed, and its run with it. Presence must be three\-valued so a reaper can defer on "present but finished" while still reaping on a genuine absence, which is the state pod\-lost exists for.

```go
type PodPresence int
```

<a name="PodPresenceLive"></a>

```go
const (
    // PodPresenceLive means a pod for the attempt exists and is Pending or
    // Running: work may still be happening, so every reaper must defer. It is
    // deliberately the zero value — an unset or error-path presence then
    // authorizes nothing destructive.
    PodPresenceLive PodPresence = iota
    // PodPresenceTerminal means a pod for the attempt exists but none of them is
    // Pending or Running. Whatever happened to that attempt is recorded on the
    // pod object, so settling it belongs to the reconciler and no reaper may
    // delete it. Phase Unknown counts here as well: the pod object is still
    // there and it is not an absence. Note the backstop for a pod stuck in
    // Unknown is NOT the reconciler — classifyPod groups Unknown with
    // Pending/Running, so the reconciler neither settles nor collects it — but
    // the agent-lost reaper (heartbeats stop when a node goes unreachable) and
    // the orphan-run reaper. The phase has not been set by kubelet since 2015
    // and is deprecated upstream, so the practical exposure is nil; classifying
    // it as presence is the conservative direction. Note this is deliberately
    // WIDER than terminalForTeardown, which the pod teardown uses: presence asks
    // "is this an absence?" (Unknown is not), teardown asks "is there a container
    // to stop, and will anything else collect this?" (for Unknown, possibly yes
    // and no) — so Unknown defers a reap here and is still deleted there (#928).
    PodPresenceTerminal
    // PodPresenceAbsent means the apiserver holds no pod for the attempt at all
    // — the attempt's pod is genuinely gone (deleted, evicted, lost with its
    // node) and there is nothing left to settle from. This is the only presence
    // that authorizes a pod-lost reap.
    PodPresenceAbsent
)
```

<a name="PodPresence.String"></a>
### func \(PodPresence\) [String](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_manager.go#L130>)

```go
func (p PodPresence) String() string
```

String names the presence for logs.

<a name="PodPresenceCache"></a>
## type [PodPresenceCache](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_manager.go#L159-L166>)

PodPresenceCache is an optional read\-through cache of pod presence — backed by a shared informer \(PR\-10\) — that the pod\-lost and dispatch\-lost reapers consult ONLY to DEFER a reap, never to authorize one. Its trust is asymmetric \(\#461\):

- CachedPodActive == true =\> a pod is present and Pending/Running; the reaper may skip the live LIST and defer, because presence is monotonic\-safe against cache lag \(a pod the cache still shows as live cannot have been gone longer than the lag, and deferring one tick is harmless\).
- CachedPodActive == false =\> NOT authoritative. The reaper MUST fall through to the live TaskPodPresence \(quorum\) read before any destructive action, so a lagged/cold cache can only ever delay a reap by a tick, never cause a false\-positive one.

It exists to remove the O\(running\-TIs\)/sec apiserver LIST storm from the read path while keeping the kill decision on the live read. Nil in Lite/subprocess and before the informer warms: every candidate then uses the live path.

```go
type PodPresenceCache interface {
    // CachedPodActive reports whether the cache holds a Pending/Running pod for
    // exactly the (run, task, try, epoch) attempt. Only a true return is
    // trusted (to defer); false is a "no speedup" signal that must not drive a
    // reap. Try-number and epoch are pinned so the cache gate cannot defer on an
    // older attempt's lingering pod (#723).
    CachedPodActive(a Attempt) bool
}
```

<a name="PodSecurity"></a>
## type [PodSecurity](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/executor.go#L14-L32>)

PodSecurity holds the task\-pod hardening knobs whose defaults are behavioral rather than free. Both zero values are the safe choice, so a Request that never touches this struct gets a pod that Pod Security Admission's \`restricted\` profile admits.

```go
type PodSecurity struct {
    // RunAsNonRoot refuses to start a task container whose image resolves to
    // UID 0. It completes the `restricted` set and is on by default: the images
    // Dexaflow ships now satisfy it. runtime/Dockerfile runs as the numeric
    // non-root UID 65532 (`USER 65532:65532` — a name the kubelet cannot resolve
    // is what previously blocked this), and every examples/*/image inherits it.
    // When set, BuildPod also stamps a pod-level fsGroup (nonRootFSGroup) so the
    // per-run staging PVC (ADR 0022) stays writable by that non-root user.
    //
    // It stays a knob rather than a constant so an operator can turn it off for a
    // fleet whose task images legitimately run as root.
    RunAsNonRoot bool

    // ReadOnlyRootFilesystem mounts the container root read-only. Off by
    // default on purpose: `restricted` does not require it, and it breaks
    // ordinary Python tasks that write to /tmp, the pip cache or a matplotlib
    // config dir. Opt in for a task that is known not to write.
    ReadOnlyRootFilesystem bool
}
```

<a name="PodSnapshotter"></a>
## type [PodSnapshotter](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L558-L563>)

PodSnapshotter supplies the reconciler's task\-pod set from a local cache instead of a live LIST every tick \(PR\-10\). It is safe here without a live confirm: the signal the reconciler acts on is presence of a terminal pod, which is monotonic \(a pod that reached Failed/Succeeded stays terminal\), and every settle is attempt\- and state\-guarded \(ADR 0052\), so at worst cache lag delays a settle by a tick. A nil snapshotter \(Lite/subprocess, or a cold start\) keeps the live LIST.

```go
type PodSnapshotter interface {
    // SnapshotTaskPods returns the managed task pods currently known, or an error
    // (e.g. the cache has not synced) so the reconciler retries next tick rather
    // than treating an unsynced cache as an empty cluster.
    SnapshotTaskPods() ([]*corev1.Pod, error)
}
```

<a name="ProcessLiveness"></a>
## type [ProcessLiveness](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/process_liveness.go#L21-L27>)

ProcessLiveness answers whether the local agent process of one attempt is still alive. It is the Lite \(subprocess executor\) counterpart of pod liveness: with no pods there is nothing for the reapers to read or delete, but the agent is a host process whose liveness can be checked by PID.

The reapers consult it to defer. A Lite reaper does not stop a live agent the way a pod delete does \(it stops the orphaned task of a dead one, see OrphanStopper\), so it must never fail an attempt whose agent is still alive as an infra loss: the infra re\-place that follows keeps the try number, so a second agent would start on the same attempt while the first one could still get its RUNNING report accepted and run user code \(\#911\). The one exception is an attempt past the credential ceiling \(see AttemptStopper\), which is failed as a task failure, never re\-placed, and whose task is then stopped.

```go
type ProcessLiveness interface {
    // AttemptProcessAlive reports whether the agent process spawned for the
    // (run, task, try) attempt is alive. false with a nil error means no live
    // agent is known for the attempt (it exited, or was never spawned); a
    // non-nil error means liveness is unknown and the caller must defer.
    AttemptProcessAlive(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}
```

<a name="ProvisionalInfraFailure"></a>
## type [ProvisionalInfraFailure](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_confirm.go#L15-L21>)

ProvisionalInfraFailure is one infra mark \(agent\_lost, pod\_lost, dispatch\_lost\) the reconciler has not confirmed yet \(ADR 0052 amendment, part 2\), with the attempt it names.

```go
type ProvisionalInfraFailure struct {
    TaskInstanceID string
    DagRunID       string
    TaskID         string
    TryNumber      int
    AttemptEpoch   int
}
```

<a name="ReapCandidate"></a>
## type [ReapCandidate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reap.go#L15-L19>)

ReapCandidate is one running dag run the reaper is considering, with the timestamp of its most recent observable activity \(max of the run's started\_at and its task instances' started\_at / ended\_at\). The reaper compares the gap from this stamp to "now" against a stall threshold; a non\-zero gap larger than the threshold means the run is orphaned and should be failed.

```go
type ReapCandidate struct {
    RunID        string
    DagID        string
    LastActivity time.Time
}
```

<a name="ReapCursor"></a>
## type [ReapCursor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reap_cursor.go#L17-L20>)

ReapCursor is a keyset position in a reaper's candidate list: the time the list orders by \(started\_at for pod\-lost, queued\_at for dispatch\-lost\) and the task instance id that breaks ties. The zero cursor is the start of the list.

A list that always returned the oldest ReapPageSize rows let that many live attempts hide a lost one behind them forever \(R\-07, \#1597\): every sweep deferred the same live rows and never reached the rest. The reapers now keep a cursor across sweeps and ask for the page after it.

```go
type ReapCursor struct {
    At  time.Time
    ID  string
}
```

<a name="ReapCursor.IsZero"></a>
### func \(ReapCursor\) [IsZero](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reap_cursor.go#L23>)

```go
func (c ReapCursor) IsZero() bool
```

IsZero reports whether the cursor is the start of the list.

<a name="ReapStore"></a>
## type [ReapStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reap.go#L37-L51>)

ReapStore is the slice of scheduler.Store the reaper needs. The full scheduler.Store embeds this interface so production wires through one type; the unit tests fake just this surface.

```go
type ReapStore interface {
    // ListReapCandidates returns every dag_run currently in 'running' state
    // alongside its last-activity timestamp. The query is the authority on what
    // "running" means and how to compute the timestamp; the reaper only decides
    // whether each one has been quiet for too long.
    ListReapCandidates(ctx context.Context) ([]ReapCandidate, error)
    // ReapRun transitions a run to 'failed' with an "orphaned" note and fails
    // any still-active task instances, but only if the run is still orphaned
    // at that moment: the store re-checks the orphan predicate atomically,
    // with quietBefore as the last-activity cutoff, because the list is only a
    // snapshot. It reports false, with no error, when the run is no longer an
    // orphan and nothing was touched. It is idempotent: a second call on the
    // same run reports false.
    ReapRun(ctx context.Context, runID string, quietBefore time.Time) (bool, error)
}
```

<a name="Reaper"></a>
## type [Reaper](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L110-L139>)

Reaper is the execution\-side backstop that fails stuck runs and task instances the scheduler dispatched but that then went dark. It bundles the five independent reapers behind one ReapOnce entrypoint the leader's maintenance loop drives once per cycle, after the pod reconciler's sweep, so the caller depends on a single capability rather than wiring each reaper itself.

```go
type Reaper struct {
    // contains filtered or unexported fields
}
```

<a name="NewReaper"></a>
### func [NewReaper](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L170>)

```go
func NewReaper(store ReaperStore, pods PodManager, cache PodPresenceCache, warmPods WarmPodLister, rec DecisionRecorder, logger *slog.Logger, cfg ReaperConfig, gates ReaperGates) *Reaper
```

NewReaper constructs the reapers and wires their pod\-teardown / liveness capability \(pods\) and, for the two K8s\-aware reapers, the presence cache. The wiring mirrors exactly what the scheduler used to do inline: every reaper gets pods; only the dispatch\-lost and pod\-lost reapers get the cache. Nil pods \(Lite/subprocess\) keeps every reaper DB\-only and makes the pod\-lost reaper a no\-op; Lite gates agent\-lost and dispatch\-lost on the agent process instead \(SetProcessLiveness\).

warmPods is the live warm\-pod seam \(ADR 0058 N1d\-a2\), threaded to the two warm consumers exactly the way pods/cache are threaded: the warm\-worker\-lost reaper \(which recovers a dead worker's attempts\) and the dispatch\-lost reaper's H3 defer. Nil \(warm pools off / not wired\) makes the warm reaper a no\-op and the dispatch\-lost warm defer inert — with the flag off no TI ever carries a warm\_worker\_id either, so both warm paths are doubly inert, byte\-for\-byte today.

<a name="Reaper.ReapOnce"></a>
### func \(\*Reaper\) [ReapOnce](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L427>)

```go
func (r *Reaper) ReapOnce(ctx context.Context) error
```

ReapOnce runs the five reapers once, in the order the scheduler used to drive them: orphan\-run, then agent\-lost, then dispatch\-lost, then pod\-lost, then warm\-worker\-lost. The dispatch\-lost reaper runs AFTER the orphan\-run reaper so a clean stuck\-queued \-\> failed \-\> orphan\-run\-failed chain can complete in a single tick once the thresholds elapse.

The whole tick is skipped — and the skip metered as reap\_gate\_skip — when the destructive gate is closed on entry: a canceled context \(SIGTERM drain, the step\-down cancel fan\-out\), a scheduler in step\-down, or a lost leadership. It is likewise skipped, metered as reap\_settling\_skip, while the leader has not settled \(see settling\); once settling has lasted 2× the grace the liveness valve opens and the tick proceeds with a WARN and reap\_settling\_valve\_open. The reapers also re\-check the destructive gate before each individual write, so the early return is the cheap common case, not the only defense.

Each reaper's infra\-level list error is logged and metered but never returned: the reapers are independent backstops, so one's failure must not block the others, and a list error must not stall the caller's cycle. Per\-candidate failures are already isolated inside each reaper's run. ReapOnce therefore always returns nil today; the error return is kept for the seam so a future hard\-failure mode need not change the caller.

<a name="Reaper.SetAttemptLifetimeCeiling"></a>
### func \(\*Reaper\) [SetAttemptLifetimeCeiling](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L261>)

```go
func (r *Reaper) SetAttemptLifetimeCeiling(d time.Duration)
```

SetAttemptLifetimeCeiling wires auth.max\_attempt\_credential\_lifetime into the agent\-lost reaper: a silent attempt that has been running longer than it is failed for the credential ceiling, as a task failure under its retry policy, instead of as agent\_lost, which would re\-place it with a fresh credential and run it again from the start \(\#1461\). Zero or negative \(the default when unset\) keeps every silent attempt agent\_lost, as before.

<a name="Reaper.SetAttemptTokenTTL"></a>
### func \(\*Reaper\) [SetAttemptTokenTTL](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L248>)

```go
func (r *Reaper) SetAttemptTokenTTL(ttl time.Duration)
```

SetAttemptTokenTTL wires the TTL of the attempt credential minted at dispatch into the dispatch\-lost reaper \(\#1126\). A queued attempt whose pod is still Pending when that credential is StartCredentialMargin from running out is failed as start\_expired, an infra mark re\-placed off the retry budget with a fresh credential, instead of being left to start with a dead one. Only the Kubernetes path sets it; zero or a TTL no longer than the margin keeps the reaper deferring on every live pod, as before.

<a name="Reaper.SetInformerSynced"></a>
### func \(\*Reaper\) [SetInformerSynced](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L281>)

```go
func (r *Reaper) SetInformerSynced(fn func() bool)
```

SetInformerSynced wires the pod informer's sync predicate into the settling gate. Until the cache has synced, the reapers' presence reads fall back to live LISTs \(safe, but the fleet view a fresh leader is about to judge from is still being assembled\), so the gate waits. Nil \(no informer: Lite, or a non\-Kubernetes executor\) leaves this condition satisfied.

<a name="Reaper.SetLastSweepCompleted"></a>
### func \(\*Reaper\) [SetLastSweepCompleted](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L291>)

```go
func (r *Reaper) SetLastSweepCompleted(fn func() time.Time)
```

SetLastSweepCompleted wires the reconciler's last\-completed\-sweep record into the settling gate: the gate holds until a sweep has COMPLETED at or after leadership was acquired, because that sweep is what recovers the durable outcome of a task pod that finished during the outage; before it, "no live pod" and "no recent heartbeat" are indistinguishable from a lost task. Nil \(no reconciler: Lite\) leaves this condition satisfied.

<a name="Reaper.SetLeaderSince"></a>
### func \(\*Reaper\) [SetLeaderSince](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L272>)

```go
func (r *Reaper) SetLeaderSince(fn func() time.Time)
```

SetLeaderSince wires the accessor the settling gate measures its grace from: when this instance last acquired scheduler leadership \(zero while not leading\). Measured from leadership acquisition, not process start, so a re\-election also resets the gate. Nil \(no leadership, as in tests\) disables the gate entirely, since the other two inputs are only meaningful under a leader. Lite wires it too: its detached agents survive a restart with a stale heartbeat, exactly the signal the grace exists for \(\#916\).

<a name="Reaper.SetLogSink"></a>
### func \(\*Reaper\) [SetLogSink](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L234>)

```go
func (r *Reaper) SetLogSink(s logSink)
```

SetLogSink wires the sink the agent\-lost, pod\-lost and warm\-worker\-lost reapers use to append a final "killed: \<reason\>" marker to a reaped attempt's log stream, so a killed task's log ends with the reason instead of a silent truncation \(\#861\), and the dispatch\-lost reaper its "killed: start\_expired" marker \(\#1126\). Any logs.MarkerSink satisfies the parameter; nil \(Lite / unwired\) leaves markers off.

<a name="Reaper.SetProcessLiveness"></a>
### func \(\*Reaper\) [SetProcessLiveness](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/process_liveness.go#L67>)

```go
func (r *Reaper) SetProcessLiveness(p ProcessLiveness)
```

SetProcessLiveness wires the subprocess liveness seam into the two reapers that act on an attempt whose agent may still be running: agent\-lost and dispatch\-lost. Each defers while the attempt's agent process is alive or its liveness cannot be read. It also turns on agent\-lost's Lite\-only judgement of a TI that never heartbeated \(see runNeverHeartbeated\), and, when p is an AttemptStopper and a credential ceiling is set, the stop of an attempt still running past that ceiling \(see runPastCeiling\). Nil \(the Kubernetes path, which gates on pods\) leaves both reapers unchanged.

<a name="ReaperConfig"></a>
## type [ReaperConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L61-L73>)

ReaperConfig holds the idle thresholds and the post\-leadership settling grace the reapers apply. Zero values are legal but reap aggressively; callers pass DefaultReaperConfig unless a test or load harness deliberately overrides them.

```go
type ReaperConfig struct {
    OrphanThreshold       time.Duration
    AgentLostThreshold    time.Duration
    DispatchLostThreshold time.Duration
    // PodLostGrace is the per-task liveness floor measured from the TI's running
    // transition, below which the pod-lost reaper does not consult pod liveness.
    PodLostGrace time.Duration
    // SettlingGrace is the minimum time after this instance acquires leadership
    // before ANY reaper may act — one rung of the leader-settling gate, which
    // also requires a synced pod informer and a completed reconciler sweep. See
    // defaultSettlingGrace and Reaper.settling. Zero disables the whole gate.
    SettlingGrace time.Duration
}
```

<a name="DefaultReaperConfig"></a>
### func [DefaultReaperConfig](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L77>)

```go
func DefaultReaperConfig() ReaperConfig
```

DefaultReaperConfig returns the production thresholds — the exact values the scheduler configured before the reapers moved here.

<a name="ReaperGates"></a>
## type [ReaperGates](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L145-L154>)

ReaperGates are the two predicates of the reaper's destructive gate \(mayReap\). They are one constructor parameter so that a wiring site cannot set one and forget the other: both decide whether a mark or delete may happen, and the leadership one used to be a setter called after construction \(\#903\).

```go
type ReaperGates struct {
    // InStepDown reports whether the scheduler is in a graceful leader
    // step-down. Nil is "not stepping down".
    InStepDown func() bool
    // Leading reports whether this instance holds scheduler leadership. The
    // maintenance loop already drives ReapOnce only while leading; this is the
    // defensive re-check at the point of the write. Nil is "leading", so Lite
    // and tests without an election are unaffected.
    Leading func() bool
}
```

<a name="ReaperStore"></a>
## type [ReaperStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reaper.go#L22-L28>)

ReaperStore is the full store surface the five reapers need, composed from each reaper's own slice. The metadatabase\-backed SchedulerStore satisfies all five, so production wires through one type; a unit test fakes just the slice its reaper touches.

```go
type ReaperStore interface {
    ReapStore
    HeartbeatReapStore
    DispatchLostReapStore
    PodLostReapStore
    WarmWorkerLostReapStore
}
```

<a name="Reconciler"></a>
## type [Reconciler](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L586-L614>)

Reconciler detects task pods whose task instance was never settled by the agent \(a pod killed before or during its report\) and records the true outcome — from the pod's durable outcome record where present, else its phase — so retries and run finalization proceed instead of stranding the task. It also garbage\-collects finished pods once they age out.

Outcome recovery is best\-effort \(ADR 0052 is a School\-A optimization on the School\-B re\-drive floor\): the settle guards on the active states. If a reaper marked the attempt as an infra failure first \(agent\_lost, pod\_lost, dispatch\_lost\), a durable SUCCESS record still wins while the mark is provisional \(ADR 0052 amendment\): the reconciler falls back to SucceedTaskOverInfraMark, which settles success only for that exact attempt, before the mark is confirmed or the planner's confirmation valve opens, on a running run. Past that window the success is dropped and the task degrades to the safe retry path, a correctness\-safe re\-run, not a wrong result. A FAILED record never overrides a mark: the reap's own teardown makes the agent write one.

```go
type Reconciler struct {
    // contains filtered or unexported fields
}
```

<a name="NewReconciler"></a>
### func [NewReconciler](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L617>)

```go
func NewReconciler(clientset kubernetes.Interface, namespace string, reporter OutcomeReporter) *Reconciler
```

NewReconciler builds a Reconciler over the given cluster and outcome reporter.

<a name="Reconciler.LastSweepCompletedAt"></a>
### func \(\*Reconciler\) [LastSweepCompletedAt](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L639>)

```go
func (r *Reconciler) LastSweepCompletedAt() time.Time
```

LastSweepCompletedAt reports when the last COMPLETED sweep started: the task\-pod set was listed and every pod visited, so every terminal pod present when the list was taken had its chance to settle its task instance. The start, not the end, so the stamp never claims data fresher than the LIST \(\#917\). Zero means no sweep has completed yet. It is the record the reaper's leader\-settling gate compares against the leadership stamp — the reapers act on signals a control\-plane restart manufactures \(a task pod that exited during the outage looks lost\), and only a sweep completed under the new leader separates "finished during the outage, outcome recoverable" from "genuinely lost".

A sweep that could not list pods records nothing. A sweep whose individual settle failed on a DB error still counts: that pod is retried next sweep, and the gate's grace leaves room for the retry before any reaper may act.

<a name="Reconciler.Reconcile"></a>
### func \(\*Reconciler\) [Reconcile](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L650>)

```go
func (r *Reconciler) Reconcile(ctx context.Context) error
```

Reconcile lists managed task pods, records each terminal one's outcome against its task instance, and garbage\-collects finished pods older than the grace period. A completed sweep is stamped for LastSweepCompletedAt.

<a name="Reconciler.SetInfraConfirmer"></a>
### func \(\*Reconciler\) [SetInfraConfirmer](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_confirm.go#L40>)

```go
func (r *Reconciler) SetInfraConfirmer(c InfraConfirmer)
```

SetInfraConfirmer wires the confirmation pass \(ADR 0052 amendment, part 2\). With it set, every sweep looks at each provisional infra mark's pods and confirms the mark when no evidence can still surface. Left unset, no mark is confirmed here, so the store must then confirm marks itself \(Lite\).

<a name="Reconciler.SetInfraOverrideObservers"></a>
### func \(\*Reconciler\) [SetInfraOverrideObservers](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/infra_override.go#L38>)

```go
func (r *Reconciler) SetInfraOverrideObservers(rec InfraOverrideRecorder, sink logSink)
```

SetInfraOverrideObservers wires what makes an override visible: a counter by mark and the sink the system line is appended to. Either may be nil.

<a name="Reconciler.SetPodSnapshotter"></a>
### func \(\*Reconciler\) [SetPodSnapshotter](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/reconcile.go#L624>)

```go
func (r *Reconciler) SetPodSnapshotter(s PodSnapshotter)
```

SetPodSnapshotter wires a cache\-backed pod source so Reconcile reads its task\-pod set from the shared informer instead of a live LIST every tick \(PR\-10\). Left unset, the reconciler keeps the live LIST — today's behavior.

<a name="Reconciler.SetSettledRunCollection"></a>
### func \(\*Reconciler\) [SetSettledRunCollection](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/run_settle_collect.go#L69>)

```go
func (r *Reconciler) SetSettledRunCollection(c SettledRunChecker)
```

SetSettledRunCollection turns on collecting a settled run's finished pods in one DeleteCollection. Nil \(the default\) keeps the age\-based GC only.

<a name="Request"></a>
## type [Request](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/executor.go#L35-L163>)

Request bundles everything an executor needs to run a single task instance.

```go
type Request struct {
    TaskInstanceID string
    TenantID       string
    DagID          string
    RunID          string
    TaskID         string
    TryNumber      int
    // AttemptEpoch is the execution of TryNumber the dispatcher claimed for this
    // request (ADR 0051 amendment). It is stamped into the pod's identity
    // annotation so the exchanged token names this execution.
    AttemptEpoch int

    Image           string
    ImagePullPolicy string
    Operator        string
    Entrypoint      string
    Env             map[string]string
    Resources       domain.Resources
    Execution       domain.Execution
    // DefaultServiceAccount is the operator's default task ServiceAccount, the
    // one the dispatcher fills into Execution.ServiceAccount when the task names
    // none. The execution allowlist does not govern it, so the executor's
    // re-check of the allowlist skips exactly this account and checks any other
    // (issue #537 C2).
    DefaultServiceAccount string
    TimeoutSeconds        int
    // AttemptLifetimeCeilingSeconds is the operator's attempt credential ceiling
    // (auth.max_attempt_credential_lifetime) in whole seconds. The Kubernetes
    // executor floors the task pod's ActiveDeadlineSeconds with it when the task
    // declares no TimeoutSeconds, so no task pod is immortal: the agent's reports
    // retry for as long as the control plane is unreachable, and without a floor
    // a total control-plane outage would leave every such pod Running forever,
    // holding its requests and blocking node scale-down. Past the ceiling the pod
    // can do nothing useful — heartbeat renewal stops and its bearer lapses. A
    // user-declared TimeoutSeconds always wins, even when longer. Non-positive
    // means "no ceiling" (the same convention as the warm worker's attempt
    // watchdog for the same knob) and applies no floor. Ignored by the subprocess
    // executor, which has no pod.
    AttemptLifetimeCeilingSeconds int64

    // PodSecurity carries the two hardening choices that can change how a task
    // runs. Everything else BuildPod applies is unconditional, because dropping
    // capabilities, blocking privilege escalation and setting a seccomp profile
    // cost a normal task nothing. Zero value is the secure default.
    PodSecurity PodSecurity

    // ExecutionPolicy is the operator's allowlist of Execution fields (issue
    // #629). The dispatcher refuses a task that breaks it before building the
    // request; the Kubernetes executor checks it again before it builds the pod,
    // so no path that builds a Request can place a pod the operator did not
    // allow. Nil allows everything.
    ExecutionPolicy *domain.ExecutionPolicy
    // AuthoredExecution, when not nil, is the task's own execution settings
    // before an admission hook changed them (ADR 0100 §4). The allowlist
    // governs what a DAG author sets, so the re-check below reads these
    // instead of Execution; the dispatcher already checked the service account
    // a hook set. Nil: Execution is what the author set.
    AuthoredExecution *domain.Execution

    // Source is the dag.py text captured at compile time. The SubprocessExecutor
    // materializes it to a per-TI temp dir so `python -m leoflow_runtime
    // dag:<task>` can importlib it from there — this is how multi-DAG Lite setups
    // avoid the ModuleNotFoundError that hit Lima 2026-06-01 when the agent's
    // global workdir didn't carry the user's dag.py. The K8s executor ignores it
    // unless SourceMode is set (the container image carries the source).
    Source string
    // SourceMode tells the K8s executor to ship Source into the pod as dag.py
    // (ADR 0067 §3). The dispatcher sets it only for a version on the operator's
    // runtime image that carries a source. Ignored by the subprocess executor,
    // which always materializes Source.
    SourceMode bool

    // Agent connection details injected into the worker environment.
    ControlPlaneAddr string
    AgentToken       string

    // AgentTokenTransport selects how the agent's bearer credential reaches the
    // pod (ADR 0055 Fix #3): "" / "envvar" (the default) sets AgentToken as a
    // plaintext LEOFLOW_AGENT_TOKEN env var — today's behavior, byte-identical;
    // "exchange" keeps the plaintext token OFF the pod object and instead projects
    // a ServiceAccount token the agent exchanges for a task-scoped JWT. Ignored by
    // the subprocess executor (Lite has no pod/SA).
    AgentTokenTransport string
    // AgentTokenAudience is the audience of the projected ServiceAccount token
    // under the exchange transport (the control plane's audience). Empty falls back
    // to the default control-plane audience.
    AgentTokenAudience string
    // AgentTokenExpirationSeconds is the projected token's expiration under the
    // exchange transport. Floored to a safe minimum so a very short task's
    // bootstrap token has not already expired at exchange time.
    AgentTokenExpirationSeconds int64
    // AgentTokenSecretName / AgentTokenSecretKey select the SecretKeyRef fallback
    // for the exchange transport (a cluster that cannot project an SA token): when
    // AgentTokenSecretName is set, LEOFLOW_AGENT_TOKEN is sourced from that Secret
    // via SecretKeyRef rather than projected — still off the plaintext pod spec.
    AgentTokenSecretName string
    AgentTokenSecretKey  string

    // StagingClaim, when set, is the name of the per-DAG-run RWX PVC mounted at
    // /staging in the task pod for large intermediate data shared across the run
    // (ADR 0022). Empty means no staging volume. StagingSize/StagingStorageClass
    // are used to provision the claim on first use.
    StagingClaim        string
    StagingSize         string
    StagingStorageClass string
    // StagingAccessMode is the PVC access mode (default ReadWriteMany; single-node
    // dev uses ReadWriteOnce). Empty means ReadWriteMany.
    StagingAccessMode string

    // AgentTLSCAConfigMap, when set, is the name of a ConfigMap holding the CA
    // (key ca.crt) the agent uses to verify the control plane's gRPC TLS cert
    // (issue #58). It is mounted into the task pod and selects TLS for the agent.
    AgentTLSCAConfigMap string

    // TaskSecretName, when set, is a Kubernetes Secret mounted read-only into the
    // task pod at TaskSecretMountPath. It carries a credential a task references by
    // path (e.g. a GCP service-account key via the connection's key_path), keeping
    // the key in the cluster's secret store rather than in Dexaflow (ADR 0035).
    TaskSecretName      string
    TaskSecretMountPath string

    // SecretsBackend / SecretsBackendKwargs, when set, are the operator's external
    // secrets backend (ADR 0060): the provider class the in-pod resolver drives and
    // its raw kwargs JSON. Injected as LEOFLOW_SECRETS_BACKEND[_KWARGS] pod env, in
    // the leoflow-owned group (operator-sourced — an author's task env cannot set
    // LEOFLOW_ keys, #828). Empty = no external backend (chain stays vault-only).
    SecretsBackend       string
    SecretsBackendKwargs string
}
```

<a name="ResilienceLadder"></a>
## type [ResilienceLadder](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/resilience_ladder.go#L76-L110>)

ResilienceLadder is the set of timing knobs whose relative ORDER the control\-plane\-restart recovery depends on. Each is owned by a different package \(agent, executor, scheduler, server main, operator config\) and tuned for its own reason, so nothing but this type states that they must line up. The invariants:

```
heartbeat interval  <  agent-lost threshold  <  settling grace  <  attempt token TTL
2 × maintenance interval  <  settling grace
longest infra re-place delay  <  orphan threshold
task termination grace + 2 × maintenance interval  <  infra confirmation valve  <  orphan threshold
attempt token TTL  <  max attempt credential lifetime   (when the ceiling is enabled)
dispatch-lost threshold  <  attempt token TTL - start credential margin
2 × maintenance interval + heartbeat interval  <  start credential margin
```

Why each rung matters:

- heartbeat \< threshold: a healthy agent must beat well inside the silence window or the agent\-lost reaper fails live tasks.
- threshold \< settling grace: after a \(re\-\)election the reapers wait longer than the silence they punish, so the whole fleet can re\-heartbeat.
- settling grace \< token TTL: the grace ends while a re\-heartbeat can still authenticate and renew the bearer; otherwise the fleet is unreapable AND unable to report — every in\-flight task is lost.
- 2×maintenance \< settling grace: the leader's maintenance loop runs the reconciler's sweep and then the reapers as ONE ordered cycle, so a reap structurally follows a completed sweep — the settling gate additionally requires that a sweep completed under the new leader. This rung is what makes the grace meaningful on top of that ordering: at least two cycles START inside it, so a settle that failed transiently on the first sweep \(a DB hiccup\) gets a second sweep before the gate can open, and the liveness valve at 2×grace has seen at least four cycle starts before it declares the sweep broken. Starts, not completions: a cycle slower than the interval coalesces the ticks behind it. Each phase is bounded by the maintenance phase timeout, so a cycle that starts always ends, but the count of completions inside the grace is not structural. A single cycle is not enough: the first tick under a new leader may land anywhere in the interval, so "one interval below the grace" can mean zero completed cycles.
- infra re\-place delay \< orphan threshold: a run whose only live task is parked in its longest infra re\-place backoff has no activity to show the orphan\-run reaper; the threshold must outlast that parking or the reaper eats a run that is still recovering from the very fault being retried.
- termination grace \+ 2×maintenance \< infra confirmation valve \(ADR 0052 amendment\): a reaped pod is stopped in place, its task container exits within the termination grace, and the reconciler needs a sweep after that to read the record and confirm or override the mark. A valve that opens sooner lets the planner re\-place on the guess before the evidence could be read. A DAG that declares a longer termination grace gets the valve, not the record, for that task.
- infra confirmation valve \< orphan threshold: a run whose only live task is a provisional mark shows no activity to the orphan\-run reaper.
- token TTL \< credential lifetime: heartbeat renewal keeps an attempt's bearer alive only while the attempt is younger than the ceiling. A ceiling below the TTL means the first renewal is already refused, the bearer lapses at the TTL, and every task longer than one TTL is unreapable AND unable to report — the restart recovery is silently disabled. This is the ONLY operator\-tunable rung; every other value is a build\-time constant.
- dispatch\-lost threshold \< token TTL \- start margin \(\#1126\): a pod still Pending as its dispatch\-minted credential nears its end is re\-placed by the dispatch\-lost reaper, which only considers attempts queued past its threshold; a threshold past the start deadline would leave the pod to start with a dead credential and spend a user retry.
- 2×maintenance \+ heartbeat \< start margin: a pod that starts just after a reaper pass saw it inside the deadline is not re\-placed, so its agent must register and reach its first heartbeat, which renews the credential, before the credential lapses.

Warm pools share the same ladder: a warm worker's per\-attempt bearer is renewed by the same heartbeat under the same ceiling, and the warm\-worker\-lost reaper reuses the pod\-lost mark, so no warm\-specific rung exists.

```go
type ResilienceLadder struct {
    HeartbeatInterval  time.Duration
    AgentLostThreshold time.Duration
    // SettlingGrace is the post-leadership window during which no reaper fires
    // (ReaperConfig.SettlingGrace); the one grace every reaper shares.
    SettlingGrace   time.Duration
    AttemptTokenTTL time.Duration
    // ReconcileInterval is the period of the leader's maintenance loop: one
    // reconciler sweep followed by one reaper pass per cycle.
    ReconcileInterval time.Duration
    // OrphanThreshold is how long a running dag run may sit with no activity
    // before the orphan-run reaper fails it (ReaperConfig.OrphanThreshold).
    OrphanThreshold time.Duration
    // InfraReplaceMaxDelay is the longest the scheduler may park an infra-failed
    // task before re-placing it: the backoff before the final permitted re-place
    // plus the whole de-synchronizing jitter window. The scheduler owns and
    // computes it; it is passed in because the scheduler package depends on this
    // one, so the validator cannot read it directly.
    InfraReplaceMaxDelay time.Duration
    // MaxAttemptCredentialLifetime is the operator's auth.max_attempt_credential_lifetime:
    // the ceiling on how long heartbeat renewal keeps an attempt's bearer alive.
    // A non-positive value is the documented "no ceiling" setting; the rung that
    // depends on it is then trivially satisfied and skipped.
    MaxAttemptCredentialLifetime time.Duration
    // TaskTerminationGrace is the default termination grace of a task pod: how
    // long a pod stopped in place may take for its task container to exit.
    TaskTerminationGrace time.Duration
    // InfraConfirmMaxWait is the scheduler's infra confirmation valve
    // (scheduler.InfraConfirmMaxWait), passed in for the same reason as
    // InfraReplaceMaxDelay.
    InfraConfirmMaxWait time.Duration
    // DispatchLostThreshold is how long an attempt may sit queued before the
    // dispatch-lost reaper looks at it (ReaperConfig.DispatchLostThreshold).
    DispatchLostThreshold time.Duration
}
```

<a name="RunRef"></a>
## type [RunRef](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/run_settle_collect.go#L56-L59>)

RunRef names a run in its tenant, as the pod labels carry them.

```go
type RunRef struct {
    Tenant string
    Run    string
}
```

<a name="ServiceAccounts"></a>
## type [ServiceAccounts](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/service_accounts.go#L17-L20>)

ServiceAccounts reads the ServiceAccounts of the namespace task pods run in, for the register dry run \(\#1889, ADR 0085\): whether the account a task pod would run as exists, and how many pull secrets it carries. It reads the account only, never the Secrets it names, with the one \`get\` on serviceaccounts the chart's Role grants.

```go
type ServiceAccounts struct {
    // contains filtered or unexported fields
}
```

<a name="NewServiceAccounts"></a>
### func [NewServiceAccounts](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/service_accounts.go#L24>)

```go
func NewServiceAccounts(clientset kubernetes.Interface, namespace string) *ServiceAccounts
```

NewServiceAccounts returns a reader of the ServiceAccounts of namespace, "default" when empty, as NewKubernetesExecutor places task pods.

<a name="ServiceAccounts.ImagePullSecrets"></a>
### func \(\*ServiceAccounts\) [ImagePullSecrets](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/service_accounts.go#L34>)

```go
func (s *ServiceAccounts) ImagePullSecrets(ctx context.Context, name string) (count int, found bool, err error)
```

ImagePullSecrets returns how many imagePullSecrets the ServiceAccount name carries, and found false when the namespace has no such account. Any other failure \(a refusal, an apiserver that does not answer\) is an error.

<a name="SettledRunChecker"></a>
## type [SettledRunChecker](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/run_settle_collect.go#L63-L65>)

SettledRunChecker reports which of the given runs are settled in their tenant. Runs it leaves out are not collected.

```go
type SettledRunChecker interface {
    SettledRuns(ctx context.Context, refs []RunRef) (map[RunRef]bool, error)
}
```

<a name="StagingStore"></a>
## type [StagingStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/staging.go#L103-L107>)

StagingStore persists the per\-run staging\-volume lifecycle in the metadatabase \(ADR 0022\): provisioning records an active row, GC marks it deleted with a reason, and GC reads the active set joined with each run's state. Identified by the deterministic PVC name \(unique per namespace\).

```go
type StagingStore interface {
    RecordStagingVolume(ctx context.Context, tenantID, dagID, runID, pvcName, size string) error
    MarkStagingDeleted(ctx context.Context, pvcName, reason string) error
    ListActiveStagingVolumes(ctx context.Context) ([]domain.StagingVolumeState, error)
}
```

<a name="StaleQueuedCandidate"></a>
## type [StaleQueuedCandidate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/stale_queued_reap.go#L21-L45>)

StaleQueuedCandidate is one task instance in \`queued\` whose dispatch may have been lost — typically because the scheduler crashed mid\-tick between committing the scheduled→queued transition and actually dispatching the TI to an executor. The reaper compares the gap from QueuedAt to "now" against a dispatch\-lost threshold; a non\-zero gap larger than the threshold means the dispatch is presumed gone and the TI is failed with reason \`dispatch\_lost\`. This unblocks the orphan\-run reaper, which keeps stuck runs out of its candidate set as long as any TI looks active \(\#202\).

```go
type StaleQueuedCandidate struct {
    TaskInstanceID string
    DagRunID       string
    // TenantID names the attempt's log stream for a start_expired marker.
    TenantID string
    DagID    string
    TaskID   string
    // TryNumber is the attempt the queued row is on, so a best-effort pod
    // delete after the mark targets exactly that attempt's pod (#474).
    TryNumber int
    // AttemptEpoch is the epoch the row's current execution was dispatched
    // with (ADR 0051 amendment). The mark and the pod teardown are pinned to it
    // as well as to TryNumber, so a row re-placed or re-dispatched between the
    // list and the write is a different attempt and is left alone.
    AttemptEpoch int
    QueuedAt     time.Time
    // WarmWorkerID is the warm pod durably bound to this attempt (ADR 0058
    // N1d-a2), or "" for a dedicated task or a warm attempt not yet acked. When
    // set AND the worker is in the live warm-pod set, the dispatch-lost reaper
    // DEFERS: the warm worker holds this attempt and is merely slow to transition
    // queued->running, so failing it would double-run the task (review finding
    // H3). A warm attempt has no task pod, so the existing pod-presence gate
    // cannot protect it — this warm check is what does.
    WarmWorkerID string
}
```

<a name="SubprocessExecutor"></a>
## type [SubprocessExecutor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess.go#L50-L67>)

SubprocessExecutor runs the agent as a host subprocess with no isolation. It is for dev mode only and logs a prominent warning on construction.

```go
type SubprocessExecutor struct {
    // contains filtered or unexported fields
}
```

<a name="NewSubprocessExecutor"></a>
### func [NewSubprocessExecutor](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess.go#L73>)

```go
func NewSubprocessExecutor(agentPath string, logger *slog.Logger) *SubprocessExecutor
```

NewSubprocessExecutor builds a SubprocessExecutor running the given agent binary. It warns that user code runs unsandboxed. The per\-DAG venv root is read from LEOFLOW\_LITE\_VENVS\_ROOT at construction time so the executor can pick the right Python for each task without a follow\-up call.

<a name="SubprocessExecutor.AttemptProcessAlive"></a>
### func \(\*SubprocessExecutor\) [AttemptProcessAlive](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess_liveness.go#L183>)

```go
func (e *SubprocessExecutor) AttemptProcessAlive(_ context.Context, runID, taskID string, tryNumber int) (bool, error)
```

AttemptProcessAlive reports whether the agent spawned for the \(run, task, try\) attempt is alive, by the PID this executor recorded when it spawned it, or, when that agent is gone, whether any process of the task group the agent recorded is still running \(an agent killed outright leaves its task behind, \#916\). No record of either means nothing is known for the attempt: \(false, nil\). A record that cannot be read, or a PID whose liveness cannot be probed, is an error so the caller defers, and so is a record directory another user could have tampered with \(see checkPIDDir\). A recorded PID that the OS reuses for an unrelated process reads alive; that only defers a reap, it never authorizes one.

<a name="SubprocessExecutor.Execute"></a>
### func \(\*SubprocessExecutor\) [Execute](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess.go#L179>)

```go
func (e *SubprocessExecutor) Execute(ctx context.Context, req Request) (Disposition, error)
```

Execute launches the agent subprocess and returns once it has started, like the Kubernetes executor creating a pod. The agent reports its own task state over gRPC, so the scheduler can record the task as queued before the agent finishes; running it synchronously here would let the agent report success before the scheduler recorded queued, and the queued write would clobber it. A non\-zero exit is therefore NOT a synchronous error; only a failure to start is. The process is reaped in the background.

A subprocess dispatch failure is always Rejected: a Lite executor talks to no apiserver, so its errors are never cluster backpressure — this preserves today's "every Lite error is permanent" behavior across the typed seam \(ADR 0051 Phase 4\). Success is Dispatched \(the agent reports its terminal state over gRPC\).

<a name="SubprocessExecutor.SetPIDDir"></a>
### func \(\*SubprocessExecutor\) [SetPIDDir](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess_liveness.go#L71>)

```go
func (e *SubprocessExecutor) SetPIDDir(dir string)
```

SetPIDDir sets the directory the executor records each spawned agent's PID in, one file per \(run, task, try\) attempt. The record lives on disk, not in memory, because the agent is detached and outlives a control\-plane restart: the restarted server must still be able to tell that an attempt's agent is alive. Empty restores the default under the system temp directory.

<a name="SubprocessExecutor.SetWorkDir"></a>
### func \(\*SubprocessExecutor\) [SetWorkDir](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess.go#L106>)

```go
func (e *SubprocessExecutor) SetWorkDir(dir string)
```

SetWorkDir sets the working directory the agent runs in. In a task pod the image's WORKDIR holds the DAG code; on a dev host \`dexaflow lite\` points this at the project directory so the agent can import the user's dag.py. Empty keeps the parent process's working directory.

<a name="SubprocessExecutor.StopAttempt"></a>
### func \(\*SubprocessExecutor\) [StopAttempt](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess_liveness.go#L276>)

```go
func (e *SubprocessExecutor) StopAttempt(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
```

StopAttempt stops the task of an attempt whose agent may still be alive: the reaper calls it for an attempt it has just failed for outliving the credential ceiling \(auth.max\_attempt\_credential\_lifetime, \#1511\), the Lite counterpart of a task pod's activeDeadlineSeconds. It sends SIGTERM to the attempt's whole task process group, waits up to the grace, then sends SIGKILL, the same escalation StopOrphanedTask uses, and reports true once no process of the group is left, including when the task had already exited.

Only the task group is signaled, never the agent: its PID record carries no start time, so after a restart of this server it could name a reused PID. The agent exits on its own once its task is gone \(its terminal report finds the attempt settled and is told to stop\), and StopAttempt waits a bounded time for that, so whatever the agent logs about its task's end is written before the caller's own log marker. Like StopOrphanedTask it acts only on a group recorded in this server's record directory whose leader is still the recorded process. No record, or a leader it cannot verify, reports false and signals nothing.

<a name="SubprocessExecutor.StopOrphanedTask"></a>
### func \(\*SubprocessExecutor\) [StopOrphanedTask](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/subprocess_liveness.go#L229>)

```go
func (e *SubprocessExecutor) StopOrphanedTask(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
```

StopOrphanedTask stops the attempt's task process group when it is an orphan of a dead agent, so the attempt can be reaped and placed again without a second copy of the task running beside the first \(\#916\). It acts only on a group recorded in this server's own record directory, only when the agent that recorded it is dead \(signal 0 answers ESRCH\), and only when the group's leader is still the process that was recorded \(procgroup.Stop checks its start time\). It sends SIGTERM, then SIGKILL after the grace, and reports true once the group is gone. Any other case \(no record, agent alive or unknown, leader unverifiable, group still alive after SIGKILL\) reports false and changes nothing, so the reaper keeps deferring.

<a name="Teardown"></a>
## type [Teardown](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/pod_manager.go#L73-L78>)

Teardown reports what one reap teardown left in place.

```go
type Teardown struct {
    // Preserved counts the pods skipped because they were already in a
    // terminal phase or carried an outcome record (#928). A climbing rate is the
    // signature of a reconciler that is marking but not settling (#933).
    Preserved int
}
```

<a name="WarmBoundTI"></a>
## type [WarmBoundTI](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warm_worker_lost_reap.go#L65-L78>)

WarmBoundTI is one \`running\` task instance durably bound to a warm worker \(ADR 0058 N1d\-a2\): WarmWorkerID is the warm pod that acked and is serving this attempt. The failover reaper matches WarmWorkerID against the live warm\-pod set to find attempts a dead warm pod held.

```go
type WarmBoundTI struct {
    TaskInstanceID string
    DagRunID       string
    TaskID         string
    TryNumber      int
    // AttemptEpoch pins the failover mark to the bound execution (ADR 0051
    // amendment).
    AttemptEpoch int
    WarmWorkerID string
    // TenantID and DagID locate the attempt's log stream for the killed marker
    // (#861).
    TenantID string
    DagID    string
}
```

<a name="WarmPodCache"></a>
## type [WarmPodCache](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L240-L254>)

WarmPodCache is the reconciler's cached read of the warm fleet \(the WarmPodInformer\), plus create expectations keyed by the created pod's name: a create the cache has not observed yet holds back further creates for its version and counts toward its tenant's budget, so a lagging cache cannot push a pool past its target, MaxPoolSize or MaxWarmPodsPerTenant.

```go
type WarmPodCache interface {
    CachedWarmPods() ([]WarmPodInfo, bool)
    // BeginCreate is called before a create is issued, EndCreate after it with
    // the created pod's name, or "" when the create failed.
    BeginCreate(t WarmTarget)
    EndCreate(t WarmTarget, podName string)
    CreatesPending(dagVersionID string) bool
    PendingCreatesByTenant() map[string]int
    // ExpectationExpired reports, once, that an expectation timed out without
    // its pod being observed: the cache may be missing pods.
    ExpectationExpired() bool
    // Covers reports whether the cache holds, or saw deleted, every named pod:
    // false means it is behind the last live LIST and must not be trusted.
    Covers(podNames []string) bool
}
```

<a name="WarmPodClient"></a>
## type [WarmPodClient](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L128-L140>)

WarmPodClient is the cluster side of warm\-pool reconciliation: list the warm fleet, create a new warm worker for a target \(which mints the bootstrap token, builds the pod via BuildWarmPod, and Creates it — the auth/config\-aware half, wired in main.go\), delete one by name, and manage the per\-dag\-version GC\-anchor ConfigMap \(ADR 0058 D11\). Kept as a narrow seam so the reconciler is unit\-tested with a fake and the executor imports neither auth nor config. KubernetesWarmPods is the production implementation.

EnsureWarmAnchor creates \(idempotently\) the version's anchor ConfigMap and returns its UID; every warm pod created for the version is stamped with an ownerReference to it, so on control\-plane loss / namespace teardown the pods are cascade\-GC'd. CreateWarmPod threads that anchor name\+UID onto the pod. The reconciler ensures the anchor before any create and deletes it \(DeleteWarmAnchor\) ONLY once the version has fully drained to zero live pods — so the cascade is always a no\-op and never kills a live attempt.

```go
type WarmPodClient interface {
    ListWarmPods(ctx context.Context) ([]WarmPodInfo, error)
    // CreateWarmPod returns the created pod's name, which keys the event-driven
    // refill's create expectation.
    CreateWarmPod(ctx context.Context, t WarmTarget, anchorName, anchorUID string) (podName string, err error)
    DeleteWarmPod(ctx context.Context, name string) error
    EnsureWarmAnchor(ctx context.Context, dagVersionID string) (uid string, err error)
    DeleteWarmAnchor(ctx context.Context, dagVersionID string) error
    // ListWarmAnchors returns the dag_version of every GC anchor in the
    // namespace, so the reconciler can delete the anchor of an inactive version
    // that never had a pod (every create refused), which the drain never visits.
    ListWarmAnchors(ctx context.Context) ([]string, error)
}
```

<a name="WarmPodInfo"></a>
## type [WarmPodInfo](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L102-L111>)

WarmPodInfo identifies one existing warm\-worker pod and the dag\_version it serves \(read from its labels\). The reconciler counts these per version.

Terminal marks a warm pod that has reached a terminal phase \(Succeeded or Failed\). Warm pods are RestartPolicy:Never and the agent has no reconnect, so a crashed/drained/finished worker lingers as a terminal pod that can never serve again. The reconciler must NOT count terminal pods toward the target \(or a dead worker never gets replaced\) and always reaps them. TenantID is the tenant that owns the dag\_version this pod serves, read from the pod's leoflow.io/tenant\-id label \(M4\). The reconciler counts a tenant's live pods by it — including pods of a draining/inactive version, which are absent from the active targets — so the per\-tenant cap sees the tenant's whole warm footprint. A pre\-label pod \(rolling upgrade\) carries "" here; the reconciler attributes it via its version when resolvable and NEVER deletes it for the cap.

```go
type WarmPodInfo struct {
    Name         string
    DagVersionID string
    Terminal     bool
    TenantID     string
    // TerminationReason is the warm-worker container's terminated reason as the
    // kubelet reported it (for example OOMKilled), or "" while it runs. The
    // warm-worker-lost reaper names an OOM kill from it (#1218).
    TerminationReason string
}
```

<a name="WarmPodInformer"></a>
## type [WarmPodInformer](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L38-L69>)

WarmPodInformer is a dedicated shared\-informer read path over the warm worker fleet \(pods labeled leoflow.io/warm\-worker=true\), which the task\-pod PodInformer does not cover: it selects on the run\-id label warm pods do not carry. It replaces the warm\-pool reconciler's per\-tick LIST and drives event\-driven refill: losing a worker \(deleted, or reaching a terminal phase\) signals Changes so the reconciler replaces it at once rather than on its next tick.

Its readings are trusted only where staleness is harmless \(ADR 0058, \#461\): the reconciler uses the cache to decide what to CREATE and which idle workers to trim, and confirms with a live LIST before the one cascading delete it performs \(a drained version's GC anchor\). The failover reapers keep their own live LIST; nothing here authorizes failing an attempt.

```go
type WarmPodInformer struct {
    // contains filtered or unexported fields
}
```

<a name="NewWarmPodInformer"></a>
### func [NewWarmPodInformer](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L80>)

```go
func NewWarmPodInformer(clientset kubernetes.Interface, namespace string) (*WarmPodInformer, error)
```

NewWarmPodInformer builds the warm\-pod informer for namespace. It does not watch until Start is called.

<a name="WarmPodInformer.BeginCreate"></a>
### func \(\*WarmPodInformer\) [BeginCreate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L198>)

```go
func (w *WarmPodInformer) BeginCreate(t WarmTarget)
```

BeginCreate records a create about to be issued for t, before the call so a watch event that beats the caller is still matched.

<a name="WarmPodInformer.CachedWarmPods"></a>
### func \(\*WarmPodInformer\) [CachedWarmPods](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L181>)

```go
func (w *WarmPodInformer) CachedWarmPods() ([]WarmPodInfo, bool)
```

CachedWarmPods returns the cached warm fleet, mapped as ListWarmPods maps it, and whether the cache has synced. Before the first sync it returns false, so a cold cache is never read as an empty fleet.

<a name="WarmPodInformer.Changes"></a>
### func \(\*WarmPodInformer\) [Changes](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L168>)

```go
func (w *WarmPodInformer) Changes() <-chan struct{}
```

Changes delivers a signal each time the fleet lost a worker \(or Kick was called\). Signals coalesce: one pending signal stands for any number of events.

<a name="WarmPodInformer.Covers"></a>
### func \(\*WarmPodInformer\) [Covers](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L306>)

```go
func (w *WarmPodInformer) Covers(names []string) bool
```

Covers reports whether the cache has caught up with the named pods \(the ones the reconciler's last live LIST returned\): each is cached, or the informer saw it deleted. A name it never saw means the cache is behind the cluster \(a hung watch\), so the reconciler reads live instead of creating off a stale count.

<a name="WarmPodInformer.CreatesPending"></a>
### func \(\*WarmPodInformer\) [CreatesPending](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L232>)

```go
func (w *WarmPodInformer) CreatesPending(dagVersionID string) bool
```

CreatesPending reports whether dagVersionID has a create in flight or one the cache has not observed yet \(younger than warmCreateExpectationTTL\).

<a name="WarmPodInformer.EndCreate"></a>
### func \(\*WarmPodInformer\) [EndCreate](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L208>)

```go
func (w *WarmPodInformer) EndCreate(t WarmTarget, podName string)
```

EndCreate records that the create BeginCreate announced returned, with the created pod's name, or "" when it failed \(no expectation is kept\). A pod the watch already reported is matched here and leaves no expectation behind.

<a name="WarmPodInformer.ExpectationExpired"></a>
### func \(\*WarmPodInformer\) [ExpectationExpired](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L266>)

```go
func (w *WarmPodInformer) ExpectationExpired() bool
```

ExpectationExpired reports whether an expectation timed out unobserved since the last call, and clears the latch.

<a name="WarmPodInformer.Kick"></a>
### func \(\*WarmPodInformer\) [Kick](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L171>)

```go
func (w *WarmPodInformer) Kick()
```

Kick requests a reconcile without blocking; the pool's claim hook uses it too.

<a name="WarmPodInformer.PendingCreatesByTenant"></a>
### func \(\*WarmPodInformer\) [PendingCreatesByTenant](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L250>)

```go
func (w *WarmPodInformer) PendingCreatesByTenant() map[string]int
```

PendingCreatesByTenant counts, per tenant, the creates in flight or not yet observed, across every dag\_version.

<a name="WarmPodInformer.RecordTerminationsTo"></a>
### func \(\*WarmPodInformer\) [RecordTerminationsTo](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L143>)

```go
func (w *WarmPodInformer) RecordTerminationsTo(k *KubernetesWarmPods)
```

RecordTerminationsTo makes the informer record, on k, the warm\-worker container's terminated reason of every warm pod it sees turn terminal or get deleted, so k.LastTerminationReason answers for a worker deleted before any LIST saw it terminal \(\#1835\). The delete event carries the pod's final state. Call it before Start.

<a name="WarmPodInformer.Start"></a>
### func \(\*WarmPodInformer\) [Start](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L150>)

```go
func (w *WarmPodInformer) Start(ctx context.Context)
```

Start begins the watch and stops it when ctx is canceled.

<a name="WarmPodInformer.WaitForCacheSync"></a>
### func \(\*WarmPodInformer\) [WaitForCacheSync](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_informer.go#L162>)

```go
func (w *WarmPodInformer) WaitForCacheSync(ctx context.Context) bool
```

WaitForCacheSync blocks until the initial LIST has populated the cache or ctx ends, and reports whether it synced. Not syncing is survivable: the reconciler reads the fleet live until CachedWarmPods reports synced.

<a name="WarmPodLister"></a>
## type [WarmPodLister](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warm_worker_lost_reap.go#L19-L21>)

WarmPodLister is the narrow read\-only seam onto the live warm fleet the failover paths need: just the warm\-pod LIST, no create/delete. Both the warm\-worker\-lost reaper and the dispatch\-lost reaper's H3 defer depend on this capability rather than the full WarmPodClient, so a unit test fakes only the LIST. KubernetesWarmPods \(via WarmPodClient\) already satisfies it — production reuses that one type, it is not duplicated.

```go
type WarmPodLister interface {
    ListWarmPods(ctx context.Context) ([]WarmPodInfo, error)
}
```

<a name="WarmPodSpec"></a>
## type [WarmPodSpec](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpod.go#L62-L149>)

WarmPodSpec is everything BuildWarmPod needs to build one long\-lived warm\-worker pod: which dag\_version pool it serves, the image \(the DAG's image — a warm worker runs the agent in warm mode and forks a child per attempt\), the control\-plane connection, and how its BOOTSTRAP credential reaches it \(the same transport a task pod uses\). It carries NO task instance: the per\-attempt token and task identity arrive in\-band over AwaitAssignment, never on this spec.

```go
type WarmPodSpec struct {
    DagVersionID    string
    Image           string
    ImagePullPolicy string
    Namespace       string

    // TenantID is the tenant that owns this dag_version. It is stamped onto the pod
    // as the leoflow.io/tenant-id label so the reconciler can attribute the pod to
    // its tenant for the per-tenant aggregate warm-pod cap (M4), even after the
    // version stops being active (the label outlives the active target).
    TenantID string

    // ControlPlaneAddr / AgentTLSCAConfigMap mirror the task-pod connection knobs:
    // where the agent dials and (when set) the CA ConfigMap it verifies the server
    // cert against. Reused verbatim so a warm worker connects exactly as a task pod.
    ControlPlaneAddr    string
    AgentTLSCAConfigMap string

    // ServiceAccount is the ServiceAccount the warm pod runs as — the operator's
    // default task ServiceAccount (executor.task_service_account). A warm worker is
    // pre-created and generic, so it can only carry the operator-wide default, not a
    // per-DAG execution.service_account; empty leaves it on the namespace default SA.
    // Without this a task placed on a warm worker runs as the default SA and keyless
    // secret resolution breaks (#2, warm-pool path).
    ServiceAccount string

    // Bootstrap-credential transport (ADR 0055 Fix #3), mirroring Request's token
    // fields: BootstrapToken rides plaintext on the env-var default; the exchange
    // transport keeps it off the pod and projects a ServiceAccount token instead.
    // The credential authorizes only Register + AwaitAssignment (no secret access —
    // secrets resolve per-attempt against the in-band attempt token), so a warm
    // worker never stamps a task-instance identity annotation.
    BootstrapToken              string
    AgentTokenTransport         string
    AgentTokenAudience          string
    AgentTokenExpirationSeconds int64
    AgentTokenSecretName        string
    AgentTokenSecretKey         string

    // Self-lifecycle caps the warm agent enforces on ITSELF (ADR 0058 D9/D10/D6/H3),
    // carried in-band as env so a worker can drain, idle-recycle, and hard-bound a
    // wedged attempt without any control-plane round trip. Each is zero when the
    // operator disables that bound; the agent treats zero/unset as "no bound".
    //
    //   - MaxAttemptsPerWorker: drain after this many completed attempts (D9/D10).
    //   - MaxWorkerLifetimeSeconds: drain past this wall-clock age (D9/D10).
    //   - WorkerIdleTTLSeconds: idle-recycle after this long awaiting work (D6).
    //   - AttemptWatchdogSeconds: hard per-attempt ceiling, independent of the task's
    //     execution_timeout, so a no-timeout wedge cannot pin the slot (H3). main.go
    //     sets it to auth.max_attempt_credential_lifetime — an attempt can never
    //     validly outlive its credential ceiling.
    MaxAttemptsPerWorker     int
    MaxWorkerLifetimeSeconds int64
    WorkerIdleTTLSeconds     int64
    AttemptWatchdogSeconds   int64

    // PodSecurity carries the same container/pod hardening choices as a task pod.
    PodSecurity PodSecurity

    // Resources sizes the warm container. With an operator resource unit it is
    // one unit (ADR 0066 §3), and only tasks of that size are placed on the
    // worker. Nil leaves the container unsized, as before.
    Resources *domain.Resources

    // ReadOnlyRootFilesystem is the warm isolation mode (X3.2,
    // execution.warm_read_only_root_filesystem). It forces a read-only root on the
    // warm container whatever PodSecurity says, mounts the writable /tmp emptyDir,
    // and tells the agent (warmAttemptHomeEnv) to give each attempt its own HOME
    // and XDG dirs inside the scratch it wipes, and to sweep /tmp and /dev/shm
    // around every attempt. On a writable root a file one attempt plants on the image would be
    // executed by the next attempt on this worker.
    ReadOnlyRootFilesystem bool

    // AnchorName / AnchorUID identify the per-dag-version GC-anchor ConfigMap this
    // warm pod is owned by (ADR 0058 D11). When BOTH are set, BuildWarmPod stamps an
    // ownerReference to the anchor, so on control-plane loss / namespace teardown the
    // pod is cascade-GC'd with the anchor — the one orphan class the reconciler (as
    // deleter) cannot cover. When either is empty (off-cluster/pre-anchor builds and
    // tests) the pod is built bare, exactly as before D11.
    AnchorName string
    AnchorUID  types.UID

    // Labels / Annotations are operator-declared metadata overlaid onto the pod;
    // keys under domain.ReservedMetadataPrefix are dropped and Dexaflow's own
    // warm-worker labels always win a collision (see mergeMetadata).
    Labels      map[string]string
    Annotations map[string]string
}
```

<a name="WarmPodSpecFunc"></a>
## type [WarmPodSpecFunc](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool_k8s.go#L19>)

WarmPodSpecFunc mints the per\-pod bootstrap credential and fills the transport / connection / security fields for a warm worker of the target, returning a spec ready for BuildWarmPod. It is the auth\- and config\-aware half of warm\-pod creation, injected from main.go so the executor package imports neither auth nor config. It is called once per warm worker the reconciler needs to create.

```go
type WarmPodSpecFunc func(t WarmTarget) (WarmPodSpec, error)
```

<a name="WarmPoolReconciler"></a>
## type [WarmPoolReconciler](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L176-L228>)

WarmPoolReconciler maintains the IDLE warm\-worker buffer per active dag\_version \(ADR 0058 N1b2b \+ N1d\-b, model A2\). Each tick it reads the active targets, the existing warm fleet, and the busy set \(pods serving a running attempt\), then per version partitions the live pods into BUSY and IDLE and:

- CREATEs enough workers to restore EffectiveMinIdle IDLE workers, capped by MaxPoolSize \(the total ceiling\) — so the pool grows past min\_idle under load and never exceeds the cap;
- DELETEs only EXCESS IDLE workers \(idle over the target\), never a busy one, so a scale\-down or a drain never kills an in\-flight attempt \(review M1\). Each delete re\-reads the busy set first, and every delete of the tick runs before any create, so a worker that takes an attempt during the tick is kept;
- reaps terminal pods unconditionally \(H1\);
- drains a no\-longer\-active version \(target 0\) down to its idle workers \+ terminal pods, LEAVING busy workers to finish \(they are deleted a later tick once idle\).

It is leader\-gated \(run on a gated ticker, like the pod reconciler\) so at replicaCount\>1 only the leader mutates the fleet. Idempotent \(it reconciles to the idle target, so re\-running converges\), panic\-safe and per\-version isolated \(one bad dag\_version never blocks the others\), and O\(active versions\) per tick. It is only constructed and started when warm pools are enabled; with them off the reconciler never runs, every warm pool stays empty, and dispatch is byte\-for\-byte today's dedicated pod\-per\-task.

The busy set is a hard safety input: without it every worker looks idle and a busy worker could be deleted, so a nil busy source or a busy\-list error aborts the tick with zero mutations \(do\-no\-harm\).

GC anchor \(D11\): before creating any pod for a version the reconciler ensures a per\-dag\-version anchor ConfigMap and stamps every warm pod with an ownerReference to it, so on control\-plane loss / namespace teardown the fleet is cascade\-GC'd. The anchor is create\-only during a version's active life and deleted only once an inactive version has fully drained to zero pods \(the footgun guard in Reconcile\), so the cascade never kills a live attempt.

```go
type WarmPoolReconciler struct {
    // contains filtered or unexported fields
}
```

<a name="NewWarmPoolReconciler"></a>
### func [NewWarmPoolReconciler](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L324>)

```go
func NewWarmPoolReconciler(targets WarmTargetSource, pods WarmPodClient, busy BusyWarmWorkerSource, maxWarmPodsPerTenant int, logger *slog.Logger, rec DecisionRecorder) *WarmPoolReconciler
```

NewWarmPoolReconciler builds a reconciler over the given target source, pod client, and busy\-worker source. busy is REQUIRED: it classifies each live pod as busy or idle so scale\-down never kills an in\-flight attempt \(ADR 0058 N1d\-b M1\); without it every worker would look idle and a busy worker could be deleted, so a nil busy source \(or a busy\-list error at tick time\) makes the tick do nothing — do\-no\-harm. maxWarmPodsPerTenant is the per\-tenant aggregate cap \(M4\); \<= 0 disables tenant accounting \(pre\-M4 per\-version behavior\). logger and rec \(metrics\) are optional — a nil logger falls back to the default and a nil rec skips metering.

<a name="WarmPoolReconciler.Reconcile"></a>
### func \(\*WarmPoolReconciler\) [Reconcile](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L336>)

```go
func (r *WarmPoolReconciler) Reconcile(ctx context.Context) error
```

Reconcile brings the warm fleet in line with the active targets for one tick. It returns an error only when it could not read the world \(the target source or the pod list failed\) so the ticker logs it and retries next tick without acting on a bad view; per\-version and per\-pod failures are logged/metered and isolated, and never abort the sweep.

<a name="WarmPoolReconciler.SetCache"></a>
### func \(\*WarmPoolReconciler\) [SetCache](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L259>)

```go
func (r *WarmPoolReconciler) SetCache(c WarmPodCache, parallelism int)
```

SetCache switches the reconciler to the cached fleet view and parallel creates bounded by parallelism \(values below 1 mean 1\). The cascading anchor delete is still confirmed with a live LIST.

<a name="WarmTarget"></a>
## type [WarmTarget](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L46-L59>)

WarmTarget is one active dag\_version the warm\-pool reconciler keeps warm workers ready for \(model A2, ADR 0058 N1d\-b\). Image is the DAG's image the warm worker runs the agent from.

EffectiveMinIdle is the number of IDLE workers to keep ready for this version — the warm buffer, already resolved through the clamp/fallback/flag gate \(see config.ExecutionSection.EffectiveMinIdle\). It is NOT a total pool size: busy workers do not count against it, so under load the pool grows past min\_idle to keep the idle buffer available.

MaxPoolSize is the TOTAL ceiling of live workers \(idle \+ busy\) the version may hold at once \(config.ExecutionSection.MaxPoolSize\). The reconciler never creates past it, so the pool breathes between EffectiveMinIdle\-idle \+ busy and this cap. The N1a boot validation guarantees max\_pool\_size \>= 1 when warm pools are on and EffectiveMinIdle is clamped to it upstream, so MaxPoolSize \>= EffectiveMinIdle in practice; the reconciler still handles MaxPoolSize \< EffectiveMinIdle defensively by taking the effective ceiling as max\(EffectiveMinIdle, MaxPoolSize\) — the idle buffer must always be creatable. TenantID is the tenant that owns this dag\_version, threaded through so the per\-tenant aggregate warm\-pod cap \(M4\) can sum a tenant's promised idle floors and ration its shared budget across versions. It comes from the active run's tenant\_id \(or the DAG's, for a prewarm target\); the reconciler groups targets and live pods by it.

Prewarm marks a target held only because the version is its DAG's current version \(\#1707, ADR 0082\): no active run needs it yet, and its pool is started so the first run finds warm workers. Under a per\-tenant cap its floor shares what the cap leaves and never raises the budget \(boundPrewarm\); otherwise the reconciler treats it like any other target and only logs and meters when a prewarm pool starts.

DagID is the DAG's id, which orders a tenant's prewarm targets when the per\-tenant cap cannot cover all of their floors, so the same DAGs keep their pools from one reconcile to the next.

```go
type WarmTarget struct {
    DagVersionID     string
    DagID            string
    Image            string
    EffectiveMinIdle int
    MaxPoolSize      int
    TenantID         string
    Prewarm          bool
    // contains filtered or unexported fields
}
```

<a name="WarmTargetSource"></a>
## type [WarmTargetSource](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warmpool.go#L70-L72>)

WarmTargetSource yields the dag\_versions to keep warm and their effective warm target: the current version of each unpaused DAG that declares min\_idle\_workers, and the versions of active runs that are still current \(ADR 0082\). A version absent from it drains. It is implemented on the storage/scheduler side \(it reads active runs, the DAGs' current versions and their cached specs and applies the operator's clamp/fallback\), and is defined HERE so the executor's reconciler depends on a narrow capability rather than importing the scheduler or storage package \(which would be a dependency cycle: storage already imports executor\).

```go
type WarmTargetSource interface {
    ActiveWarmTargets(ctx context.Context) ([]WarmTarget, error)
}
```

<a name="WarmTerminationLookup"></a>
## type [WarmTerminationLookup](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warm_worker_lost_reap.go#L27-L29>)

WarmTerminationLookup is the optional seam a WarmPodLister offers to name the cause of a worker the LIST no longer returns: the warm\-worker container's terminated reason it last observed for that pod, or "" \(\#1835\). KubernetesWarmPods implements it.

```go
type WarmTerminationLookup interface {
    LastTerminationReason(podName string) string
}
```

<a name="WarmWorkerLostReapStore"></a>
## type [WarmWorkerLostReapStore](<https://github.com/dexadata/dexaflow/blob/main/internal/executor/warm_worker_lost_reap.go#L84-L97>)

WarmWorkerLostReapStore is the slice of the store the warm\-worker\-lost reaper needs: list the warm\-bound running TIs, and reuse the pod\-lost mark to route a lost attempt to infra \(bumps infra\_attempts, NOT try\_number\). The full scheduler store satisfies it; a unit test fakes just this surface.

```go
type WarmWorkerLostReapStore interface {
    ListWarmBoundRunningTIs(ctx context.Context) ([]WarmBoundTI, error)
    // MarkTaskPodLost is reused verbatim from the pod-lost reaper: a warm worker
    // vanishing is the same infra failure as a task pod vanishing, so it routes
    // through the same infra path with the same idempotency guard (WHERE
    // state='running'). applied=false means a late settle raced the mark — a
    // benign skip, never a false reap.
    MarkTaskPodLost(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
    // MarkTaskPodLostOOMKilled is MarkTaskPodLost, the same infra mark with the
    // same guards, for an attempt whose warm worker's container the kubelet
    // reported OOMKilled (#1218): only the failure message differs, naming the
    // OOM kill. The attribution stays infra (ADR 0051, ADR 0058 D7).
    MarkTaskPodLostOOMKilled(ctx context.Context, taskInstanceID string, tryNumber, attemptEpoch int) (bool, error)
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
