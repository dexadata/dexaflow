---
title: "internal/agentrpc"
linkTitle: "internal/agentrpc"
weight: 6
---

```go
import "github.com/neochaotic/leoflow/internal/agentrpc"
```

Package agentrpc implements the control\-plane side of the agent gRPC protocol: it authenticates each in\-pod agent by its per\-task\-instance token, serves the task specification, and records the state transitions the agent reports.

## Index

- [Constants](<#constants>)
- [Variables](<#variables>)
- [func RecoveryStreamInterceptor\(logger \*slog.Logger\) grpc.StreamServerInterceptor](<#RecoveryStreamInterceptor>)
- [func RecoveryUnaryInterceptor\(logger \*slog.Logger\) grpc.UnaryServerInterceptor](<#RecoveryUnaryInterceptor>)
- [type AgentTokenMinter](<#AgentTokenMinter>)
- [type AgentTokenRenewer](<#AgentTokenRenewer>)
- [type Authenticator](<#Authenticator>)
- [type InflightHandlers](<#InflightHandlers>)
  - [func NewInflightHandlers\(\) \*InflightHandlers](<#NewInflightHandlers>)
  - [func \(c \*InflightHandlers\) Count\(\) int](<#InflightHandlers.Count>)
  - [func \(c \*InflightHandlers\) StreamInterceptor\(\) grpc.StreamServerInterceptor](<#InflightHandlers.StreamInterceptor>)
  - [func \(c \*InflightHandlers\) UnaryInterceptor\(\) grpc.UnaryServerInterceptor](<#InflightHandlers.UnaryInterceptor>)
- [type LogPublisher](<#LogPublisher>)
- [type LogSink](<#LogSink>)
- [type PodTaskResolver](<#PodTaskResolver>)
- [type ReclaimEvent](<#ReclaimEvent>)
- [type ReclaimReason](<#ReclaimReason>)
- [type ReviewedPod](<#ReviewedPod>)
- [type SecretLivenessAuditor](<#SecretLivenessAuditor>)
- [type SecretScopeAuditor](<#SecretScopeAuditor>)
- [type SecretsStore](<#SecretsStore>)
- [type Server](<#Server>)
  - [func NewServer\(authn Authenticator, store Store, xcomSvc XComService\) \*Server](<#NewServer>)
  - [func \(s \*Server\) AwaitAssignment\(stream agentv1.AgentService\_AwaitAssignmentServer\) error](<#Server.AwaitAssignment>)
  - [func \(s \*Server\) EnableWarmPools\(onReclaim func\(ReclaimEvent\)\)](<#Server.EnableWarmPools>)
  - [func \(s \*Server\) ExchangeToken\(ctx context.Context, \_ \*agentv1.ExchangeTokenRequest\) \(\*agentv1.ExchangeTokenResponse, error\)](<#Server.ExchangeToken>)
  - [func \(s \*Server\) FetchXCom\(ctx context.Context, req \*agentv1.FetchXComRequest\) \(\*agentv1.FetchXComResponse, error\)](<#Server.FetchXCom>)
  - [func \(s \*Server\) GetConnections\(ctx context.Context, \_ \*agentv1.GetConnectionsRequest\) \(\*agentv1.GetConnectionsResponse, error\)](<#Server.GetConnections>)
  - [func \(s \*Server\) GetTaskSpec\(ctx context.Context, \_ \*agentv1.GetTaskSpecRequest\) \(\*agentv1.TaskSpec, error\)](<#Server.GetTaskSpec>)
  - [func \(s \*Server\) GetVariables\(ctx context.Context, \_ \*agentv1.GetVariablesRequest\) \(\*agentv1.GetVariablesResponse, error\)](<#Server.GetVariables>)
  - [func \(s \*Server\) Heartbeat\(ctx context.Context, \_ \*agentv1.HeartbeatRequest\) \(\*agentv1.HeartbeatResponse, error\)](<#Server.Heartbeat>)
  - [func \(s \*Server\) PushXCom\(ctx context.Context, req \*agentv1.PushXComRequest\) \(\*agentv1.PushXComResponse, error\)](<#Server.PushXCom>)
  - [func \(s \*Server\) Register\(ctx context.Context, \_ \*agentv1.RegisterRequest\) \(\*agentv1.RegisterResponse, error\)](<#Server.Register>)
  - [func \(s \*Server\) ReportState\(ctx context.Context, req \*agentv1.ReportStateRequest\) \(\*agentv1.ReportStateResponse, error\)](<#Server.ReportState>)
  - [func \(s \*Server\) SetLeaderCheck\(fn func\(\) bool\)](<#Server.SetLeaderCheck>)
  - [func \(s \*Server\) SetLivenessGate\(checker TaskLivenessChecker, mode string\)](<#Server.SetLivenessGate>)
  - [func \(s \*Server\) SetLogPublisher\(p LogPublisher\)](<#Server.SetLogPublisher>)
  - [func \(s \*Server\) SetLogSink\(sink LogSink\)](<#Server.SetLogSink>)
  - [func \(s \*Server\) SetSecretLivenessAuditor\(a SecretLivenessAuditor\)](<#Server.SetSecretLivenessAuditor>)
  - [func \(s \*Server\) SetSecretScopeAuditor\(a SecretScopeAuditor\)](<#Server.SetSecretScopeAuditor>)
  - [func \(s \*Server\) SetSecretScoping\(policy string\)](<#Server.SetSecretScoping>)
  - [func \(s \*Server\) SetSecrets\(store SecretsStore, allowInsecure bool\)](<#Server.SetSecrets>)
  - [func \(s \*Server\) SetShutdown\(ctx context.Context\)](<#Server.SetShutdown>)
  - [func \(s \*Server\) SetTokenExchange\(reviewer TokenReviewer, resolver PodTaskResolver, minter AgentTokenMinter, ttl time.Duration, allowInsecure bool\)](<#Server.SetTokenExchange>)
  - [func \(s \*Server\) SetTokenRenewal\(renewer AgentTokenRenewer, renewalTTL, maxAttemptLifetime time.Duration\)](<#Server.SetTokenRenewal>)
  - [func \(s \*Server\) SetWarmPools\(reg \*WorkerRegistry\)](<#Server.SetWarmPools>)
  - [func \(s \*Server\) StreamLogs\(stream agentv1.AgentService\_StreamLogsServer\) \(err error\)](<#Server.StreamLogs>)
- [type Store](<#Store>)
- [type TaskLivenessChecker](<#TaskLivenessChecker>)
- [type TaskSpec](<#TaskSpec>)
- [type TokenReviewer](<#TokenReviewer>)
- [type WarmBinding](<#WarmBinding>)
- [type WorkerRegistry](<#WorkerRegistry>)
  - [func NewWorkerRegistry\(onReclaim func\(ReclaimEvent\)\) \*WorkerRegistry](<#NewWorkerRegistry>)
  - [func \(r \*WorkerRegistry\) Ack\(assignmentID string, started bool\) \(\*WarmBinding, bool\)](<#WorkerRegistry.Ack>)
  - [func \(r \*WorkerRegistry\) Assign\(dagVersion string, a \*agentv1.WorkAssignment\) bool](<#WorkerRegistry.Assign>)
  - [func \(r \*WorkerRegistry\) Deregister\(w \*registeredWorker\)](<#WorkerRegistry.Deregister>)
  - [func \(r \*WorkerRegistry\) MarkFree\(identity string\)](<#WorkerRegistry.MarkFree>)
  - [func \(r \*WorkerRegistry\) Register\(identity, dagVersion, podName string, send chan \*agentv1.WorkAssignment\) \*registeredWorker](<#WorkerRegistry.Register>)
- [type XComService](<#XComService>)


## Constants

<a name="ScopingPermissive"></a>Secret scoping policy \(ADR 0055 D9\), operator\-set, NEVER author\-settable.

```go
const (
    // ScopingPermissive delivers the whole tenant vault when a DAG declares
    // nothing (today's behavior) and warns — but still delivers the whole vault —
    // when a DAG declares a narrower set. Subsetting is reserved for enforce, so no
    // already-declaring pipeline loses access. It is the default for this shipment.
    ScopingPermissive = "permissive"
    // ScopingEnforce delivers ONLY the declared subset (empty declaration →
    // nothing), resolved server-side and filtered in the query.
    ScopingEnforce = "enforce"
    // ScopingOff disables scoping entirely: the whole tenant vault, no warn.
    ScopingOff = "off"
)
```

<a name="LivenessObserve"></a>Secret\-path liveness gate modes \(ADR 0055 E2\). The gate consults the read\-only task\-instance liveness predicate before serving secrets so a token whose task instance is no longer live stops resolving them.

```go
const (
    // LivenessObserve logs a structured warn + records a would-have-denied audit
    // event when the caller's TI is not live, but STILL delivers. It is the
    // default: no behavior change, the observe half of the warn→enforce arc.
    LivenessObserve = "observe"
    // LivenessEnforce denies with codes.PermissionDenied when the caller's TI is
    // not live. It is the operator's later flip, after an observe period.
    LivenessEnforce = "enforce"
)
```

## Variables

<a name="ErrStaleReport"></a>ErrStaleReport is returned by Store.ReportState when the report did not apply because the task instance had already moved on — a reaper settled it, or a retry advanced past the attempt the reporting agent was dispatched for. It is not a failure of the RPC: the agent did nothing wrong and must not retry, so the handler acknowledges and logs. Declared here rather than in the storage package because storage implements this interface, not the reverse.

```go
var ErrStaleReport = errors.New("task state report did not apply: the task instance already moved on")
```

<a name="RecoveryStreamInterceptor"></a>
## func [RecoveryStreamInterceptor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/recovery.go#L34>)

```go
func RecoveryStreamInterceptor(logger *slog.Logger) grpc.StreamServerInterceptor
```

RecoveryStreamInterceptor is RecoveryUnaryInterceptor for streaming handlers \(e.g. log streaming\), so a panic mid\-stream returns Internal instead of crashing the control plane.

<a name="RecoveryUnaryInterceptor"></a>
## func [RecoveryUnaryInterceptor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/recovery.go#L18>)

```go
func RecoveryUnaryInterceptor(logger *slog.Logger) grpc.UnaryServerInterceptor
```

RecoveryUnaryInterceptor recovers panics in unary RPC handlers so a single malformed or unexpected request from an agent cannot crash the control plane. The panic is logged with its stack and translated to a gRPC Internal error; the server keeps serving. It should be the outermost interceptor so it also covers any later interceptor.

<a name="AgentTokenMinter"></a>
## type [AgentTokenMinter](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/exchange.go#L58-L60>)

AgentTokenMinter issues a task\-scoped agent JWT for a resolved identity. It is satisfied by \*auth.JWTAuthenticator \(IssueAgentToken\) — the same minter used at dispatch and heartbeat renewal, so the exchanged token is indistinguishable from a dispatched one on every downstream path.

```go
type AgentTokenMinter interface {
    IssueAgentToken(id auth.AgentIdentity, ttl time.Duration) (string, error)
}
```

<a name="AgentTokenRenewer"></a>
## type [AgentTokenRenewer](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L94-L96>)

AgentTokenRenewer re\-mints a live attempt's agent token with a fresh short TTL, preserving the identity and the attempt's first\-dispatch origin. It is consulted only on a liveness\-proven heartbeat \(ADR 0055 Fix \#4\). ok is false when the attempt has outlived its max\-lifetime ceiling — the signal to let the credential lapse rather than refresh it. Implemented by \*auth.JWTAuthenticator.

```go
type AgentTokenRenewer interface {
    RenewAgentToken(token string, ttl, maxLifetime time.Duration) (string, bool, error)
}
```

<a name="Authenticator"></a>
## type [Authenticator](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L85-L87>)

Authenticator verifies an agent bearer token into a task instance identity.

```go
type Authenticator interface {
    AuthenticateAgent(token string) (*auth.AgentIdentity, error)
}
```

<a name="InflightHandlers"></a>
## type [InflightHandlers](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/inflight.go#L21-L23>)

InflightHandlers counts the agent RPC handlers currently executing. gRPC exposes no such number, and the bounded graceful stop needs it: past its bound the process force\-closes the transports and waits only briefly for the handlers to return, while a log writer's final Put is bounded far longer, so the process can legitimately exit with handlers still running. Abandoning one is safe — a log object is written by a single atomic Put, so the stored object stays at its previous flush rather than being truncated — but silent, and this count is what makes it visible.

The zero value is not usable; build one with NewInflightHandlers. It is safe for concurrent use.

```go
type InflightHandlers struct {
    // contains filtered or unexported fields
}
```

<a name="NewInflightHandlers"></a>
### func [NewInflightHandlers](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/inflight.go#L27>)

```go
func NewInflightHandlers() *InflightHandlers
```

NewInflightHandlers returns a zeroed counter to install on a gRPC server via UnaryInterceptor and StreamInterceptor and to read with Count.

<a name="InflightHandlers.Count"></a>
### func \(\*InflightHandlers\) [Count](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/inflight.go#L30>)

```go
func (c *InflightHandlers) Count() int
```

Count reports how many handlers are executing right now.

<a name="InflightHandlers.StreamInterceptor"></a>
### func \(\*InflightHandlers\) [StreamInterceptor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/inflight.go#L49>)

```go
func (c *InflightHandlers) StreamInterceptor() grpc.StreamServerInterceptor
```

StreamInterceptor counts a streaming handler for the duration of its stream. These are the handlers the shutdown log is about: StreamLogs lives as long as its task and an idle warm worker's AwaitAssignment lives indefinitely, so they are what is still open when the bounded stop gives up.

<a name="InflightHandlers.UnaryInterceptor"></a>
### func \(\*InflightHandlers\) [UnaryInterceptor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/inflight.go#L33>)

```go
func (c *InflightHandlers) UnaryInterceptor() grpc.UnaryServerInterceptor
```

UnaryInterceptor counts a unary handler for the duration of its call.

<a name="LogPublisher"></a>
## type [LogPublisher](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L140-L142>)

LogPublisher fans a log line out for live tailing \(optional\).

```go
type LogPublisher interface {
    Publish(ctx context.Context, ref logs.Ref, line string) error
}
```

<a name="LogSink"></a>
## type [LogSink](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L135-L137>)

LogSink opens a writer for a task attempt's streamed logs.

```go
type LogSink interface {
    Open(ref logs.Ref) (logs.LogWriter, error)
}
```

<a name="PodTaskResolver"></a>
## type [PodTaskResolver](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/exchange.go#L49-L52>)

PodTaskResolver maps a reviewed pod to the agent identity it runs, so the minted JWT is scoped correctly. It is an interface so it is mocked in unit tests; the concrete resolver reads the pod the apiserver validated.

ResolveAgent is the branch point the exchange uses \(ADR 0058 D1\): a pod carrying the warm\-worker LABEL resolves to a warm\-worker identity \(Scope == ScopeWarmWorker, naming its dag\_version pool \+ worker id, no task claims\); any other pod resolves to the task\-instance identity the control plane stamped on it at dispatch — exactly what ResolveTaskInstance returns. ResolveTaskInstance is kept for the pure task path and callers that only ever expect a task instance.

```go
type PodTaskResolver interface {
    ResolveTaskInstance(ctx context.Context, pod ReviewedPod) (auth.AgentIdentity, error)
    ResolveAgent(ctx context.Context, pod ReviewedPod) (auth.AgentIdentity, error)
}
```

<a name="ReclaimEvent"></a>
## type [ReclaimEvent](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L31-L38>)

ReclaimEvent is emitted when a handed\-out assignment must be re\-placed. The placement layer consumes it to re\-dispatch the attempt on the infra budget. It carries the attempt identity \(RunID, TaskID, TryNumber\) — populated from the leaseState at every emit site — so the observer can re\-place the exact attempt \(ADR 0058 N1d\-c, H2\) without re\-deriving it. It deliberately carries no attempt\_token.

```go
type ReclaimEvent struct {
    AssignmentID string
    DagVersionID string
    Reason       ReclaimReason
    RunID        string
    TaskID       string
    TryNumber    int
}
```

<a name="ReclaimReason"></a>
## type [ReclaimReason](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L11>)

ReclaimReason names why an assignment was reclaimed \(ADR 0058 N1b, H1\).

```go
type ReclaimReason int
```

<a name="ReclaimLeaseExpired"></a>

```go
const (
    // ReclaimLeaseExpired means the worker did not ack the assignment as started
    // before its lease elapsed.
    ReclaimLeaseExpired ReclaimReason = iota
    // ReclaimRefused means the worker acked with started=false (it cannot or will
    // not run it).
    ReclaimRefused
    // ReclaimWorkerGone means the worker's stream ended while it held an unacked
    // assignment (a disconnected worker can never ack it).
    ReclaimWorkerGone
)
```

<a name="ReviewedPod"></a>
## type [ReviewedPod](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/exchange.go#L21-L26>)

ReviewedPod is the pod a validated projected ServiceAccount token identifies. The concrete TokenReviewer fills it from the Kubernetes TokenReview response — the bound\-token status carries the pod name/uid the token was issued for. The field names the control plane resolves a task instance from are apiserver\-version\-dependent \(ADR 0055 D7\): the primary key is PodName in the task namespace; PodUID guards against a name\-reused stale pod.

```go
type ReviewedPod struct {
    Namespace      string
    PodName        string
    PodUID         string
    ServiceAccount string
}
```

<a name="SecretLivenessAuditor"></a>
## type [SecretLivenessAuditor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L85-L89>)

SecretLivenessAuditor records a structured audit event when the secret\-path liveness gate fires: a would\-have\-denied in observe mode, or a denial in enforce mode \(ADR 0055\). It carries identity \+ kind \+ mode only, never secret names or values. Optional and best\-effort: a nil auditor or a write error only skips the row; it never changes the gate's decision.

```go
type SecretLivenessAuditor interface {
    // tenantID is the tenant UUID the agent token carries (AgentIdentity.TenantID),
    // not the tenant name.
    RecordSecretLivenessDenial(ctx context.Context, tenantID, dagID, runID, taskID string, tryNumber int, kind, mode string) error
}
```

<a name="SecretScopeAuditor"></a>
## type [SecretScopeAuditor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L51-L55>)

SecretScopeAuditor records a structured audit event when a task receives the full tenant secret set despite declaring only a subset of it — the visibility half of the warn\-before\-enforce arc \(ADR 0045 §Settled \#3, ADR 0055\). It carries counts only, never secret names or values. It is optional and best\-effort: a nil auditor or a write error only skips the audit row; it never changes what is delivered.

```go
type SecretScopeAuditor interface {
    // tenantID is the tenant UUID the agent token carries (AgentIdentity.TenantID),
    // not the tenant name.
    RecordSecretScopeWarning(ctx context.Context, tenantID, dagID, runID, taskID, kind string, declared, total int) error
}
```

<a name="SecretsStore"></a>
## type [SecretsStore](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L24-L29>)

SecretsStore returns a tenant's Variables and Connections for delivery to a task pod \(ADR 0021\). Connection URIs carry decrypted credentials, so this is only ever served over the authenticated agent channel — never to the UI/API.

The unscoped methods return the whole tenant vault \(permissive / off\). The scoped methods return ONLY the named subset, filtered IN THE QUERY \(ADR 0055 D1: never post\-filter the decrypted whole vault in the handler\); they back secret\_scoping: enforce, where a task receives only what it declared. An empty name set returns nothing — enforce's load\-bearing \[\] case.

```go
type SecretsStore interface {
    SecretVariables(ctx context.Context, tenant string) (map[string]string, error)
    SecretConnectionURIs(ctx context.Context, tenant string) (map[string]string, error)
    SecretVariablesScoped(ctx context.Context, tenant string, names []string) (map[string]string, error)
    SecretConnectionURIsScoped(ctx context.Context, tenant string, names []string) (map[string]string, error)
}
```

<a name="Server"></a>
## type [Server](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L145-L195>)

Server implements agentv1.AgentServiceServer over a Store and Authenticator.

```go
type Server struct {
    agentv1.UnimplementedAgentServiceServer
    // contains filtered or unexported fields
}
```

<a name="NewServer"></a>
### func [NewServer](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L199>)

```go
func NewServer(authn Authenticator, store Store, xcomSvc XComService) *Server
```

NewServer builds an AgentService server backed by the given authenticator, store, and XCom service.

<a name="Server.AwaitAssignment"></a>
### func \(\*Server\) [AwaitAssignment](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/await_assignment.go#L58>)

```go
func (s *Server) AwaitAssignment(stream agentv1.AgentService_AwaitAssignmentServer) error
```

AwaitAssignment is the warm\-worker assignment transport \(ADR 0058 N1b\): a long\-lived bidi stream over which the control plane pushes per\-attempt WorkAssignments down and the worker sends its registration, acks, and slot\-free signals up.

Inert\-when\-off: if warm pools are not wired the handler refuses immediately with FailedPrecondition — the flag\-gated dormant state.

Identity: the registry key is the worker's AUTHENTICATED identity from the stream's bearer token \(via identify\), NOT the dag\_version\_id in the register payload — a worker cannot claim an arbitrary identity through the message. The payload's dag\_version\_id only names which pool the worker serves and must be non\-empty.

After registration two flows run concurrently: the receive loop drains WorkerMessages \(acks feed the H1 lease machine, slot\-free frees the worker\) while the main select pumps assignments from the worker's outbound channel down the stream. The handler exits — deregistering the worker \(defer\) — on context cancellation, the control plane's shutdown signal \(SetShutdown\), a stream Send error, or the receive loop ending \(clean EOF or a transport error\).

<a name="Server.EnableWarmPools"></a>
### func \(\*Server\) [EnableWarmPools](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/await_assignment.go#L33>)

```go
func (s *Server) EnableWarmPools(onReclaim func(ReclaimEvent))
```

EnableWarmPools turns on the warm\-worker assignment transport with a production registry \(ADR 0058 N1b\). onReclaim \(may be nil\) observes reclaim events for the future placement layer to consume. Call only when execution.warm\_pools\_enabled is set — the default leaves the handler inert.

<a name="Server.ExchangeToken"></a>
### func \(\*Server\) [ExchangeToken](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/exchange.go#L87>)

```go
func (s *Server) ExchangeToken(ctx context.Context, _ *agentv1.ExchangeTokenRequest) (*agentv1.ExchangeTokenResponse, error)
```

ExchangeToken validates the agent's projected ServiceAccount token \(bootstrap bearer\), resolves the pod to its task instance, and returns a freshly minted task\-scoped agent JWT \(ADR 0055 Fix \#3\). It is called ONCE at agent startup under the exchange transport; the default env\-var transport never calls it.

It fails closed at every step: Unimplemented when the exchange is not wired, PermissionDenied on an insecure channel, Unauthenticated on a missing or rejected projected token, and Internal when the reviewed pod cannot be resolved to an attempt \(never mint an unscoped or misattributed token\). The minted token and the presented projected token are never logged.

<a name="Server.FetchXCom"></a>
### func \(\*Server\) [FetchXCom](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L439>)

```go
func (s *Server) FetchXCom(ctx context.Context, req *agentv1.FetchXComRequest) (*agentv1.FetchXComResponse, error)
```

FetchXCom returns an upstream task's value, but only from a task the caller declared as an XCom input within the same run \(and, by construction, the same tenant\), enforcing cross\-tenant and cross\-run isolation.

<a name="Server.GetConnections"></a>
### func \(\*Server\) [GetConnections](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L258>)

```go
func (s *Server) GetConnections(ctx context.Context, _ *agentv1.GetConnectionsRequest) (*agentv1.GetConnectionsResponse, error)
```

GetConnections returns the calling task's tenant Connections as Airflow URIs for the agent to export as AIRFLOW\_CONN\_\<CONN\_ID\>.

<a name="Server.GetTaskSpec"></a>
### func \(\*Server\) [GetTaskSpec](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L248>)

```go
func (s *Server) GetTaskSpec(ctx context.Context, _ *agentv1.GetTaskSpecRequest) (*agentv1.TaskSpec, error)
```

GetTaskSpec returns the execution spec for the calling task instance.

<a name="Server.GetVariables"></a>
### func \(\*Server\) [GetVariables](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L220>)

```go
func (s *Server) GetVariables(ctx context.Context, _ *agentv1.GetVariablesRequest) (*agentv1.GetVariablesResponse, error)
```

GetVariables returns the calling task's tenant Variables for the agent to export as AIRFLOW\_VAR\_\<KEY\>.

<a name="Server.Heartbeat"></a>
### func \(\*Server\) [Heartbeat](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L339>)

```go
func (s *Server) Heartbeat(ctx context.Context, _ *agentv1.HeartbeatRequest) (*agentv1.HeartbeatResponse, error)
```

Heartbeat stamps the per\-TI liveness signal \(\#128\) and returns the server clock so the agent can detect skew. A storage error stamping the heartbeat is logged but does not fail the RPC — failing the call would risk the agent terminating itself unnecessarily on a transient DB blip. The scheduler reaper would, in the worst case, fail the TI as agent\_lost on the next tick; correct under "do no harm" \(ADR 0031\).

<a name="Server.PushXCom"></a>
### func \(\*Server\) [PushXCom](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L411>)

```go
func (s *Server) PushXCom(ctx context.Context, req *agentv1.PushXComRequest) (*agentv1.PushXComResponse, error)
```

PushXCom stores a value the task produced, keyed by the caller's identity. Size/schema violations are returned as a rejection, not a transport error, so the agent can fail the task with a clear reason.

<a name="Server.Register"></a>
### func \(\*Server\) [Register](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L236>)

```go
func (s *Server) Register(ctx context.Context, _ *agentv1.RegisterRequest) (*agentv1.RegisterResponse, error)
```

Register acknowledges an agent's startup and returns the server clock.

<a name="Server.ReportState"></a>
### func \(\*Server\) [ReportState](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L289>)

```go
func (s *Server) ReportState(ctx context.Context, req *agentv1.ReportStateRequest) (*agentv1.ReportStateResponse, error)
```

ReportState records a state transition the agent observed for its task.

<a name="Server.SetLeaderCheck"></a>
### func \(\*Server\) [SetLeaderCheck](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/await_assignment.go#L27>)

```go
func (s *Server) SetLeaderCheck(fn func() bool)
```

SetLeaderCheck gates AwaitAssignment to the scheduler leader \(warm\-pool Hole B\). Each scheduler replica wires its OWN leadership predicate, so a follower refuses the stream \(FailedPrecondition\) and only the leader — whose leader\-only placer consults the same in\-memory registry the worker registers into — serves it. A nil predicate \(the default\) leaves the handler unchecked, so a single\-node or unwired deployment serves exactly as before.

<a name="Server.SetLivenessGate"></a>
### func \(\*Server\) [SetLivenessGate](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L107>)

```go
func (s *Server) SetLivenessGate(checker TaskLivenessChecker, mode string)
```

SetLivenessGate attaches the read\-only task\-instance liveness predicate the secret path consults, in the given mode \("observe" | "enforce", ADR 0055 E2\). An unrecognized mode falls back to observe — the safe, non\-denying default. A nil checker leaves the gate off \(delivery unchanged\), so the gate is opt\-in.

<a name="Server.SetLogPublisher"></a>
### func \(\*Server\) [SetLogPublisher](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L233>)

```go
func (s *Server) SetLogPublisher(p LogPublisher)
```

SetLogPublisher attaches the live\-tail publisher \(optional\). When set, StreamLogs publishes each line for the UI's live tail.

<a name="Server.SetLogSink"></a>
### func \(\*Server\) [SetLogSink](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L229>)

```go
func (s *Server) SetLogSink(sink LogSink)
```

SetLogSink attaches the log sink that StreamLogs writes to. Without it, StreamLogs reports Unimplemented.

<a name="Server.SetSecretLivenessAuditor"></a>
### func \(\*Server\) [SetSecretLivenessAuditor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L119>)

```go
func (s *Server) SetSecretLivenessAuditor(a SecretLivenessAuditor)
```

SetSecretLivenessAuditor attaches the sink for secret\-path liveness events \(optional\). Without it, a would\-have\-denied / denial still produces the WARN log but no audit row.

<a name="Server.SetSecretScopeAuditor"></a>
### func \(\*Server\) [SetSecretScopeAuditor](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L101>)

```go
func (s *Server) SetSecretScopeAuditor(a SecretScopeAuditor)
```

SetSecretScopeAuditor attaches the sink for secret\-scope warning events \(optional\). Without it, a narrowing declaration still produces the WARN log but no audit row.

<a name="Server.SetSecretScoping"></a>
### func \(\*Server\) [SetSecretScoping](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L125>)

```go
func (s *Server) SetSecretScoping(policy string)
```

SetSecretScoping sets the operator scope\-by\-declaration policy \(ADR 0055 D9\): "enforce" | "permissive" | "off". An unrecognized value falls back to permissive — the safe, non\-denying default — so a misconfiguration never silently denies. The policy is operator\-scoped, never author\-settable.

<a name="Server.SetSecrets"></a>
### func \(\*Server\) [SetSecrets](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L94>)

```go
func (s *Server) SetSecrets(store SecretsStore, allowInsecure bool)
```

SetSecrets attaches the secrets store. allowInsecure permits serving secrets over a non\-TLS channel — for local/dev only; production must use TLS \(the handlers fail closed otherwise\). See ADR 0021 / issue \#58.

<a name="Server.SetShutdown"></a>
### func \(\*Server\) [SetShutdown](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L225>)

```go
func (s *Server) SetShutdown(ctx context.Context)
```

SetShutdown wires the control plane's shutdown context: once ctx ends, every long\-lived stream returns Unavailable, so the gRPC graceful stop that follows completes instead of waiting for the tasks themselves to finish. An open StreamLogs first closes \(flushes\) its log writer; the agent treats the closed stream as best\-effort log delivery and keeps running its task. A StreamLogs that arrives AFTER the signal is refused before its writer is opened — the listener keeps accepting for the rest of the shutdown, and an opened\-then\- abandoned writer Puts an empty object over an attempt that is logging elsewhere. An open AwaitAssignment ends too — an idle warm worker holds one open indefinitely, so leaving it out would make the forced stop the normal shutdown path.

<a name="Server.SetTokenExchange"></a>
### func \(\*Server\) [SetTokenExchange](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/exchange.go#L69>)

```go
func (s *Server) SetTokenExchange(reviewer TokenReviewer, resolver PodTaskResolver, minter AgentTokenMinter, ttl time.Duration, allowInsecure bool)
```

SetTokenExchange wires the projected\-SA\-token exchange \(ADR 0055 Fix \#3\): the TokenReview client, the pod→task\-instance resolver, the JWT minter, and the TTL of the minted task\-scoped token. allowInsecure permits running the exchange over a non\-TLS channel \(dev only\); production must use TLS \(ExchangeToken fails closed otherwise, like the secret path\). A nil reviewer leaves the exchange OFF — ExchangeToken then reports Unimplemented — which is the default \(env\-var\) transport, so a deployment that does not opt in is byte\-identical to today.

<a name="Server.SetTokenRenewal"></a>
### func \(\*Server\) [SetTokenRenewal](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L210>)

```go
func (s *Server) SetTokenRenewal(renewer AgentTokenRenewer, renewalTTL, maxAttemptLifetime time.Duration)
```

SetTokenRenewal wires per\-attempt token renewal \(ADR 0055 Fix \#4\): on a liveness\-proven heartbeat the server re\-mints the caller's bearer with a fresh renewalTTL and returns it on HeartbeatResponse.renewed\_token, so a long task keeps a working credential while the short TTL bounds a stolen/finished one. maxAttemptLifetime is the hard ceiling on an attempt's total credential age since dispatch \(0 disables it\). A nil renewer or non\-positive renewalTTL leaves renewal off — the heartbeat returns no token \(unchanged behavior\).

<a name="Server.SetWarmPools"></a>
### func \(\*Server\) [SetWarmPools](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/await_assignment.go#L19>)

```go
func (s *Server) SetWarmPools(reg *WorkerRegistry)
```

SetWarmPools wires a prebuilt warm\-worker assignment registry \(ADR 0058 N1b\). A nil registry \(the default\) leaves AwaitAssignment inert — it returns FailedPrecondition — so with execution.warm\_pools\_enabled off the transport is completely dormant and no running path changes. Used by tests to inject a registry with a deterministic lease; callers wire it via EnableWarmPools.

<a name="Server.StreamLogs"></a>
### func \(\*Server\) [StreamLogs](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L479>)

```go
func (s *Server) StreamLogs(stream agentv1.AgentService_StreamLogsServer) (err error)
```

StreamLogs receives the task's log lines and writes them through the sink, flushing on stream end so the logs survive the pod. The stream also ends when the control plane shuts down \(SetShutdown\), so the flush runs before exit — and a stream that ARRIVES after that signal is refused before any writer is opened, so it leaves no empty object behind.

<a name="Store"></a>
## type [Store](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L107-L126>)

Store is the server's view of persistent task state.

```go
type Store interface {
    // TaskSpec returns the execution spec for the identified task instance.
    TaskSpec(ctx context.Context, id auth.AgentIdentity) (TaskSpec, error)
    // ReportState records a state transition reported by the agent.
    ReportState(ctx context.Context, id auth.AgentIdentity, state domain.TaskState, exitCode int, errMsg string) error
    // Reschedule parks an active TI in up_for_reschedule with its next-poke time so
    // the scheduler re-dispatches it later without consuming retry budget (#380).
    Reschedule(ctx context.Context, id auth.AgentIdentity, at time.Time) error
    // RecordHeartbeat stamps last_heartbeat_at on the identified TI so the
    // scheduler's heartbeat reaper (#128) can tell live tasks from agent-lost
    // ones. The state guard inside the SQL skips already-terminal rows.
    RecordHeartbeat(ctx context.Context, id auth.AgentIdentity) error
    // BindWarmAttempt records the durable warm-attempt binding (ADR 0058 N1d-a1):
    // the warm worker pod (workerPod) that acked this attempt (runID, taskID,
    // tryNumber) as started. The write is guarded on active state, so a settled
    // attempt is never bound (a benign no-op, not an error). Called only on a warm
    // ack — with warm pools off no assignment is ever acked, so it is never called
    // and warm_worker_id stays NULL.
    BindWarmAttempt(ctx context.Context, runID, taskID string, tryNumber int, workerPod string) error
}
```

<a name="TaskLivenessChecker"></a>
## type [TaskLivenessChecker](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/secrets.go#L76-L78>)

TaskLivenessChecker reports whether a task\-instance attempt is still live — present and in an active \(non\-terminal\) state for the given \(run, task, try\). It is the read\-only revocation signal the secret path consults \(ADR 0055 D3\): a terminal, superseded, or reaped attempt is not live, so its token stops resolving secrets. The predicate derives ONLY from \(run, task, try\) \+ active state — never run recency — so a clear\-and\-rerun of an old run stays live.

```go
type TaskLivenessChecker interface {
    IsTaskInstanceLive(ctx context.Context, runID, taskID string, tryNumber int) (bool, error)
}
```

<a name="TaskSpec"></a>
## type [TaskSpec](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L27-L82>)

TaskSpec is the execution specification the agent needs to run a task.

```go
type TaskSpec struct {
    Operator         string
    Entrypoint       string
    DagVersion       string
    Environment      map[string]string
    XComInputMapping map[string][]string
    XComSchema       map[string]any
    TimeoutSeconds   int
    // CallArgsJSON carries TaskFlow literal call args captured by the parser
    // (#115). The agent injects this verbatim as LEOFLOW_CALL_ARGS_JSON; the
    // runtime decodes it. Empty when the task has no literals. The name
    // keeps Airflow's DAG-run `params` term free for a future feature (#148).
    CallArgsJSON string
    // OperatorClass is the dotted Airflow operator/sensor class for an
    // airflow_operator task (ADR 0040); empty for native operators. The agent
    // passes it to BuildCommand to dispatch the runtime's --operator mode.
    OperatorClass string
    // OperatorArgsJSON carries the operator's constructor kwargs (JSON). The agent
    // injects it as LEOFLOW_OPERATOR_ARGS; the runtime decodes it. Empty when the
    // operator takes no args.
    OperatorArgsJSON string
    // LogicalDate is the DagRun's logical date in RFC3339; the agent derives the
    // runtime's LEOFLOW_TS/LEOFLOW_DS from it (ADR 0040). Empty leaves them unset.
    LogicalDate string
    // DependsOn lists the task's upstream task_ids. The agent fetches each one's
    // return_value so a captured operator's ti.xcom_pull(<id>) resolves it (ADR 0040).
    DependsOn []string
    // DataIntervalStart/End are the DagRun's data interval in RFC3339; the agent
    // stamps the runtime's data_interval_start/end context from them (ADR 0040).
    DataIntervalStart string
    DataIntervalEnd   string
    // ParamsJSON is the DagRun's params/conf, JSON-encoded (#148); the agent stamps
    // LEOFLOW_PARAMS so the runtime exposes context['params'] / {{ params.X }}.
    ParamsJSON string
    // FirstRescheduleAt is when a reschedule-mode sensor first entered reschedule,
    // RFC3339 (#380). The agent stamps LEOFLOW_FIRST_RESCHEDULE_AT so the sensor's
    // get_first_reschedule_date returns it and cumulative timeout works. Empty on
    // the first attempt (not yet rescheduled).
    FirstRescheduleAt string
    // MaxTries is the task's total attempt budget (retries + 1). The agent stamps
    // LEOFLOW_MAX_TRIES so the runtime fires on_failure_callback only on the
    // terminal attempt (#424). Zero is treated as 1 (no retries).
    MaxTries int
    // OnFailureCallback marks that the task declares an Airflow on_failure_callback
    // (#424). The agent stamps LEOFLOW_ON_FAILURE_CALLBACK=1 so the runtime runs it
    // in-process on the task's final failure.
    OnFailureCallback bool
    // DeclaredVariables and DeclaredConnections are the secret names this task
    // declared (ADR 0045, ADR 0055): the task's own set when it narrows, otherwise
    // the DAG's. They are carried on the resolved spec so a later increment can
    // scope secret delivery server-side to the declared set. Today this is data
    // only: the secret RPCs still return the whole tenant vault, so a declaration
    // changes nothing about what the agent receives.
    DeclaredVariables   []string
    DeclaredConnections []string
}
```

<a name="TokenReviewer"></a>
## type [TokenReviewer](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/exchange.go#L35-L37>)

TokenReviewer validates a projected ServiceAccount token against the control plane's audience via the Kubernetes TokenReview API and returns the pod it was issued for. It is the ONE apiserver call in the exchange, made once per pod at bootstrap \(never on the secret hot path\). It is an interface so it is MOCKED in unit tests — the concrete client needs a real apiserver and is exercised only by the owed real\-cluster e2e. An error \(bad signature, expired, wrong audience, or authenticated=false\) means the token is not a valid bootstrap credential.

```go
type TokenReviewer interface {
    ReviewProjectedToken(ctx context.Context, token string) (ReviewedPod, error)
}
```

<a name="WarmBinding"></a>
## type [WarmBinding](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L46-L51>)

WarmBinding is the durable binding an ack \(started=true\) establishes: the warm worker pod \(PodName\) now serving a specific attempt \(RunID, TaskID, TryNumber\). The handler persists it \(BindWarmAttempt\) so a later failover reaper can match bound attempts against the live warm\-pod set \(ADR 0058 N1d\-a1\). PodName is the worker's own downward\-API pod name it sent in WorkerRegister — the reaper's join key against ListWarmPods — NOT the registry's authenticated identity.

```go
type WarmBinding struct {
    RunID     string
    TaskID    string
    TryNumber int
    PodName   string
}
```

<a name="WorkerRegistry"></a>
## type [WorkerRegistry](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L89-L99>)

WorkerRegistry is the concurrency\-safe home of the warm\-worker fleet and the H1 ack/lease machine \(ADR 0058 N1b\).

Data structures and complexity:

- workers: identity \-\> entry. Register/Deregister/MarkFree are O\(1\).
- free: dag\_version \-\> \(identity \-\> entry\). Assign grabs one free worker of a dag\_version in O\(1\) \(a single map\-range that breaks on the first element\), then removes it in O\(1\).
- leases: assignment\_id \-\> in\-flight lease. Ack and lease\-expiry are O\(1\).

```go
type WorkerRegistry struct {
    // contains filtered or unexported fields
}
```

<a name="NewWorkerRegistry"></a>
### func [NewWorkerRegistry](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L103>)

```go
func NewWorkerRegistry(onReclaim func(ReclaimEvent)) *WorkerRegistry
```

NewWorkerRegistry builds a registry whose reclaim events are delivered to onReclaim \(may be nil\).

<a name="WorkerRegistry.Ack"></a>
### func \(\*WorkerRegistry\) [Ack](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L213>)

```go
func (r *WorkerRegistry) Ack(assignmentID string, started bool) (*WarmBinding, bool)
```

Ack settles the lease for assignmentID. started=true marks the worker busy, cancels the lease \(no reclaim\), and returns the WarmBinding to persist — the acked attempt's \(run, task, try\) plus the worker's pod name — with ok=true. started=false reclaims the assignment and returns ok=false. An ack for an unknown assignment \(already expired or already settled\) also returns ok=false. Only ok=true carries a non\-nil binding the handler should persist.

<a name="WorkerRegistry.Assign"></a>
### func \(\*WorkerRegistry\) [Assign](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L175>)

```go
func (r *WorkerRegistry) Assign(dagVersion string, a *agentv1.WorkAssignment) bool
```

Assign hands a WorkAssignment to some free worker of dagVersion by pushing it onto that worker's outbound channel and starting its lease. It returns false when no free worker of that dag\_version exists \(nothing was handed out and no lease was started\).

<a name="WorkerRegistry.Deregister"></a>
### func \(\*WorkerRegistry\) [Deregister](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L139>)

```go
func (r *WorkerRegistry) Deregister(w *registeredWorker)
```

Deregister removes a worker's entry, but only if the registry still points at exactly this entry — a reconnect under the same identity installs a new entry, and the stale stream's later Deregister must not evict the live one. Any in\-flight leases the worker still held are reclaimed: a gone worker can never ack them.

<a name="WorkerRegistry.MarkFree"></a>
### func \(\*WorkerRegistry\) [MarkFree](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L248>)

```go
func (r *WorkerRegistry) MarkFree(identity string)
```

MarkFree records a worker's SlotFree signal: it clears busy and returns the worker to the free set so it can take new work. Unknown identities are ignored.

<a name="WorkerRegistry.Register"></a>
### func \(\*WorkerRegistry\) [Register](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/worker_registry.go#L120>)

```go
func (r *WorkerRegistry) Register(identity, dagVersion, podName string, send chan *agentv1.WorkAssignment) *registeredWorker
```

Register records a warm worker under its authenticated identity, ready to take work for dagVersion. podName is the worker's own pod name, carried so a started ack can bind the attempt to it. Idempotent: a reconnect with the same identity replaces the prior entry \(never adds a second\), and the fresh entry starts free. It returns the entry so the caller can Deregister exactly the entry it created.

<a name="XComService"></a>
## type [XComService](<https://github.com/dexadata/leoflow/blob/main/internal/agentrpc/server.go#L129-L132>)

XComService stores and retrieves XCom values for the agent.

```go
type XComService interface {
    Push(ctx context.Context, key xcom.Key, value []byte, contentType string, schema map[string]any) error
    Fetch(ctx context.Context, key xcom.Key) (xcom.Entry, error)
}
```

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
