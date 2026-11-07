---
title: "internal/agent"
linkTitle: "internal/agent"
weight: 5
---

```go
import "github.com/dexadata/dexaflow/internal/agent"
```

Package agent contains the worker\-side logic that runs inside the task container: building the user process command, injecting XCom inputs, reading the return value, and retry backoff. The gRPC client lives in cmd/dexaflow\-agent.

## Index

- [Constants](<#constants>)
- [func AttemptTokenTTL\(interval time.Duration\) time.Duration](<#AttemptTokenTTL>)
- [func BuildCommand\(operator, entrypoint, operatorClass string\) \(\[\]string, error\)](<#BuildCommand>)
- [func ClassifyBootstrapFailure\(stage BootstrapStage, err error\) string](<#ClassifyBootstrapFailure>)
- [func ExchangeToken\(ctx context.Context, client agentv1.AgentServiceClient, tokens \*TokenSource\) error](<#ExchangeToken>)
- [func NewReturnValuePath\(\) \(path string, cleanup func\(\) error, err error\)](<#NewReturnValuePath>)
- [func ReadReturnValue\(path string\) \(value \[\]byte, ok bool, err error\)](<#ReadReturnValue>)
- [func ReadTokenFile\(path string\) \(string, error\)](<#ReadTokenFile>)
- [func ReportBootstrapFailure\(path string, stage BootstrapStage, err error\)](<#ReportBootstrapFailure>)
- [func ResolverFromEnv\(getenv func\(string\) string\) \(secretsource.SecretResolver, secretsource.Backend, error\)](<#ResolverFromEnv>)
- [func XComEnvVar\(name string, value \[\]byte\) string](<#XComEnvVar>)
- [type BootstrapStage](<#BootstrapStage>)
- [type CommandRunner](<#CommandRunner>)
  - [func NewExecRunner\(\) CommandRunner](<#NewExecRunner>)
  - [func NewExecRunnerRecordingGroup\(path string\) CommandRunner](<#NewExecRunnerRecordingGroup>)
- [type LogSink](<#LogSink>)
  - [func OpenLogSink\(ctx context.Context, client agentv1.AgentServiceClient\) \(LogSink, error\)](<#OpenLogSink>)
- [type NoopLogSink](<#NoopLogSink>)
  - [func \(NoopLogSink\) Close\(\) error](<#NoopLogSink.Close>)
  - [func \(NoopLogSink\) Send\(\*agentv1.LogLine\) error](<#NoopLogSink.Send>)
- [type Runner](<#Runner>)
  - [func \(r \*Runner\) Run\(ctx context.Context\) error](<#Runner.Run>)
- [type TokenSource](<#TokenSource>)
  - [func Dial\(addr, token string, allowInsecure bool, caFile string\) \(agentv1.AgentServiceClient, \*grpc.ClientConn, \*TokenSource, error\)](<#Dial>)
  - [func NewTokenSource\(token string\) \*TokenSource](<#NewTokenSource>)
  - [func \(s \*TokenSource\) Set\(token string\)](<#TokenSource.Set>)
  - [func \(s \*TokenSource\) Token\(\) string](<#TokenSource.Token>)
- [type WarmRunner](<#WarmRunner>)
  - [func \(w \*WarmRunner\) Run\(ctx context.Context, dagVersionID string\) error](<#WarmRunner.Run>)


## Constants

<a name="DefaultHeartbeatInterval"></a>DefaultHeartbeatInterval is how often the in\-pod agent pings the control plane while a task runs. Token renewal rides this signal \(ADR 0055 Fix \#4\): every live heartbeat refreshes the bearer, so the per\-attempt token TTL is derived from this interval rather than set to a flat day.

```go
const DefaultHeartbeatInterval = 15 * time.Second
```

<a name="LogResumeMetadataKey"></a>LogResumeMetadataKey is the request metadata key with which the agent asks the control plane to CONTINUE an attempt's stored log rather than start it: it is set on every StreamLogs stream the agent re\-opens after the previous one ended mid\-task \(\#912\). The control plane opens such a stream with the sink's Resume, which keeps what is already stored, and confirms it by sending the LogResumedHeaderKey response header. agentrpc mirrors both keys.

```go
const LogResumeMetadataKey = "dexaflow-log-resume"
```

<a name="LogResumedHeaderKey"></a>LogResumedHeaderKey is the response header with which the control plane confirms that it accepted a resumed log stream \(see LogResumeMetadataKey\).

```go
const LogResumedHeaderKey = "dexaflow-log-resumed"
```

<a name="AttemptTokenTTL"></a>
## func [AttemptTokenTTL](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/ttl.go#L28>)

```go
func AttemptTokenTTL(interval time.Duration) time.Duration
```

AttemptTokenTTL derives the short per\-attempt agent\-token TTL from the heartbeat interval: max\(floor, beats × interval\). The result always exceeds a single interval, so one missed beat never lapses a live credential, while the floor keeps the TTL short enough to bound an exfiltrated token. A non\-positive interval \(heartbeats disabled\) yields the floor.

<a name="BuildCommand"></a>
## func [BuildCommand](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/command.go#L33>)

```go
func BuildCommand(operator, entrypoint, operatorClass string) ([]string, error)
```

BuildCommand returns the argv to execute the user's task for the given operator. operatorClass is the dotted Airflow operator/sensor class, used only for airflow\_operator tasks \(ADR 0040\); it is ignored for the other operators.

<a name="ClassifyBootstrapFailure"></a>
## func [ClassifyBootstrapFailure](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/bootstrap.go#L59>)

```go
func ClassifyBootstrapFailure(stage BootstrapStage, err error) string
```

ClassifyBootstrapFailure maps a pre\-registration startup failure to a short, operator\-facing classification, or "" when err is nil.

It reads only the gRPC status CODE and the stage — never the error's message — so the result is always one of the constants above. That is what makes the reason safe to persist and serve: the control plane deliberately does not echo token details back to the agent, and this classifier must not reintroduce a channel that does.

<a name="ExchangeToken"></a>
## func [ExchangeToken](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/exchange.go#L22>)

```go
func ExchangeToken(ctx context.Context, client agentv1.AgentServiceClient, tokens *TokenSource) error
```

ExchangeToken performs the one\-time bootstrap token exchange \(ADR 0055 Fix \#3\): it calls the control plane's ExchangeToken RPC carrying the current bootstrap bearer \(the projected ServiceAccount token\) and swaps the returned task\-scoped agent JWT into tokens, so every subsequent RPC authenticates as the task instance. It runs ONLY under the exchange transport, before any other RPC; the default env\-var transport never calls it.

It fails the startup on any error: the agent must never proceed with a bootstrap credential the control plane rejected, nor with an empty token.

<a name="NewReturnValuePath"></a>
## func [NewReturnValuePath](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/command.go#L22>)

```go
func NewReturnValuePath() (path string, cleanup func() error, err error)
```

NewReturnValuePath returns a unique, agent\-owned path for this task's return value, plus a cleanup. The agent runs one task per process, so a per\-process temp dir keeps concurrent tasks and other users from ever sharing a single /tmp/leoflow\_return\_value.json \(which collided — permission denied across uids, clobbered across parallel tasks\). The runtime is pointed here via the LEOFLOW\_RETURN\_VALUE\_PATH env the runner injects.

<a name="ReadReturnValue"></a>
## func [ReadReturnValue](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/command.go#L107>)

```go
func ReadReturnValue(path string) (value []byte, ok bool, err error)
```

ReadReturnValue reads the optional return\-value file. ok is false \(no error\) when the file does not exist.

<a name="ReadTokenFile"></a>
## func [ReadTokenFile](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/exchange.go#L38>)

```go
func ReadTokenFile(path string) (string, error)
```

ReadTokenFile reads a projected token from path and trims surrounding whitespace \(the kubelet writes the token without a trailing newline, but trim defensively so the bearer matches exactly what the apiserver signed\).

<a name="ReportBootstrapFailure"></a>
## func [ReportBootstrapFailure](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/bootstrap.go#L96>)

```go
func ReportBootstrapFailure(path string, stage BootstrapStage, err error)
```

ReportBootstrapFailure records a classified pre\-registration failure on the container termination message, so the control plane learns WHY a pod died without its agent ever completing the handshake. Without it the reconciler sees only a failed pod and the operator is left with "no logs available".

It is best\-effort and never fails the caller: the agent is already exiting, and a lost diagnostic must not change the exit path. An empty path \(outside a pod\) is a no\-op.

<a name="ResolverFromEnv"></a>
## func [ResolverFromEnv](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/secret_resolver.go#L67>)

```go
func ResolverFromEnv(getenv func(string) string) (secretsource.SecretResolver, secretsource.Backend, error)
```

ResolverFromEnv builds the external\-secrets resolver \+ its routing Backend from the operator\-injected LEOFLOW\_SECRETS\_\* pod env \(operator\-only: the dispatch filter, \#828, keeps an author's task env from setting LEOFLOW\_ keys\). It returns a nil resolver when no backend is configured — the chain then stays vault\-only, byte\-identical to the pre\-0060 env\-export. A malformed config fails closed \(returned error\) rather than silently disabling external secrets.

<a name="XComEnvVar"></a>
## func [XComEnvVar](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/command.go#L101>)

```go
func XComEnvVar(name string, value []byte) string
```

XComEnvVar formats an XCom input as a LEOFLOW\_XCOM\_\<NAME\>=\<json\> env entry.

<a name="BootstrapStage"></a>
## type [BootstrapStage](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/bootstrap.go#L16>)

BootstrapStage names the startup step a pre\-registration failure happened in. The stage narrows the classification: the same transport error means something different while reading a token file than while exchanging one.

```go
type BootstrapStage int
```

<a name="StageToken"></a>

```go
const (
    // StageToken is reading the projected ServiceAccount token from the pod.
    StageToken BootstrapStage = iota
    // StageDial is establishing the gRPC channel to the control plane.
    StageDial
    // StageExchange is trading the bootstrap token for a task-scoped credential.
    StageExchange
)
```

<a name="CommandRunner"></a>
## type [CommandRunner](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L25-L27>)

CommandRunner executes the user task process, writing its stdout and stderr to the supplied writers and returning the process exit code.

```go
type CommandRunner interface {
    Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (exitCode int, err error)
}
```

<a name="NewExecRunner"></a>
### func [NewExecRunner](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/exec.go#L66>)

```go
func NewExecRunner() CommandRunner
```

NewExecRunner returns a CommandRunner that executes tasks as child processes.

<a name="NewExecRunnerRecordingGroup"></a>
### func [NewExecRunnerRecordingGroup](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/exec.go#L76>)

```go
func NewExecRunnerRecordingGroup(path string) CommandRunner
```

NewExecRunnerRecordingGroup is NewExecRunner that also records each task's process group at path \(a procgroup.Record: this agent's pid, the group id and the group leader's start time\) as soon as the task starts. Lite hands the path to its agent: the task leads its own group, so an agent killed outright leaves the task running, and only this record lets the server see that orphan and stop it before the attempt is placed again \(\#916\). A task whose group cannot be recorded is stopped and the run fails, so no task runs that the server could not find. An empty path records nothing.

<a name="LogSink"></a>
## type [LogSink](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L36-L39>)

LogSink receives log lines produced by the user task. Sends are best\-effort.

```go
type LogSink interface {
    Send(line *agentv1.LogLine) error
    Close() error
}
```

<a name="OpenLogSink"></a>
### func [OpenLogSink](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/logstream.go#L101>)

```go
func OpenLogSink(ctx context.Context, client agentv1.AgentServiceClient) (LogSink, error)
```

OpenLogSink starts the StreamLogs RPC and returns a sink that forwards lines to it. It is the agent's first RPC, so it uses WaitForReady: with the lazy connection of grpc.NewClient the channel may not be established yet, and without this the stream would fail fast on a cold connection \(the "opening log stream" EOF in \#36\) rather than waiting for the control plane to be reachable. ctx also bounds the streams the sink re\-opens.

<a name="NoopLogSink"></a>
## type [NoopLogSink](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L44>)

NoopLogSink discards log lines. The agent falls back to it when the control plane log stream is unavailable \(e.g. StreamLogs not yet implemented\), so a task still runs even though its logs are not shipped this run.

```go
type NoopLogSink struct{}
```

<a name="NoopLogSink.Close"></a>
### func \(NoopLogSink\) [Close](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L50>)

```go
func (NoopLogSink) Close() error
```

Close is a no\-op.

<a name="NoopLogSink.Send"></a>
### func \(NoopLogSink\) [Send](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L47>)

```go
func (NoopLogSink) Send(*agentv1.LogLine) error
```

Send discards the line.

<a name="Runner"></a>
## type [Runner](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L56-L137>)

Runner orchestrates a single task execution inside the worker container: it registers with the control plane, fetches the task spec and XCom inputs, runs the user process while streaming logs, pushes the return value, and reports the terminal state.

```go
type Runner struct {
    Client     agentv1.AgentServiceClient
    Cmd        CommandRunner
    Sink       LogSink
    Hostname   string
    Version    string
    Env        []string // base process environment (typically os.Environ())
    ReturnPath string   // file the task writes its return value to; empty disables push
    LinksPath  string   // file the runtime writes operator_extra_links to; empty disables (#375)
    PushesPath string   // file the runtime writes custom-keyed XCom pushes to; empty disables (multi-key XCom)
    // ReschedulePath is the file a reschedule-mode sensor writes its next-poke time
    // to before exiting with rescheduleExitCode; empty disables reschedule (#380).
    ReschedulePath string
    // TmpDir, when set, is exported to the task as TMPDIR so the child's temp files
    // land in a per-attempt directory the caller wipes between attempts, instead of
    // the pod's real /tmp. The warm worker points this at a subdir of its scratch
    // (reset before every attempt) so no attempt observes another attempt's temp
    // files, a token cache, a dbt profile, ~/.aws-style credentials (#728). Empty
    // leaves TMPDIR untouched: a single-shot pod is already destroyed per task, so
    // its /tmp needs no in-process reset.
    TmpDir string
    // HomeDir, when set, is exported to the task as HOME, with the XDG base dirs
    // under it, so the child's dotfiles, caches and user site-packages land in a
    // per-attempt directory the caller wipes between attempts. A warm worker on a
    // read-only root sets it (X3.2); empty leaves HOME and XDG_* as inherited.
    HomeDir string
    // TerminationLogPath is where the agent writes its durable outcome record just
    // before delivering the report, so a pod killed mid-report still leaves the
    // task's true result behind for the reconciler to recover (ADR 0052). Empty
    // disables it, Lite (subprocess, in-process report) needs no such record.
    TerminationLogPath string
    // HeartbeatInterval is how often to ping the control plane while the task
    // runs; zero disables heartbeats.
    HeartbeatInterval time.Duration
    // Token, when set, is the swappable bearer backing the gRPC per-RPC credential.
    // On a heartbeat carrying a renewed_token the loop atomically swaps it here so
    // every subsequent RPC uses the new credential (ADR 0055 Fix #4). Nil disables
    // bearer swapping (a heartbeat's renewed_token is then ignored). Typed as the
    // narrow tokenSetter seam (satisfied by *TokenSource) so the heartbeat's Set
    // can be observed in tests; production always wires a *TokenSource.
    Token tokenSetter
    // BeforeReport, if set, is invoked with the terminal state AFTER the durable
    // outcome record is written and BEFORE the report is delivered. It is a
    // fault-injection seam for the durable-outcome E2E (ADR 0052), the agent
    // binary wires it, from an env var, to exit the process, simulating a pod
    // killed mid-report with the record already on disk. Nil in production.
    BeforeReport func(agentv1.TaskState)

    // Resolver resolves a declared secret name from an external backend, pod-side
    // (ADR 0060). Nil = no external backend configured: the resolution chain is the
    // vault only, byte-identical to the pre-0060 env-export. SecretBackend is the
    // operator-configured routing (which kinds/names are externally sourced); its
    // zero value covers nothing, so a nil-or-zero pair never calls the resolver.
    Resolver      secretsource.SecretResolver
    SecretBackend secretsource.Backend
    // contains filtered or unexported fields
}
```

<a name="Runner.Run"></a>
### func \(\*Runner\) [Run](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/runner.go#L160>)

```go
func (r *Runner) Run(ctx context.Context) error
```

Run executes the task lifecycle and returns an error if the task failed. In single\-shot mode the agent registers once and serves exactly one attempt, so Run is register followed by runOneAttempt. The warm worker \(warm.go\) reuses runOneAttempt directly, registering separately and driving many attempts.

<a name="TokenSource"></a>
## type [TokenSource](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/auth.go#L20-L23>)

TokenSource holds the agent's current bearer token behind a lock so the heartbeat loop can atomically swap it \(token renewal, ADR 0055 Fix \#4\) while the gRPC per\-RPC credential reads it on every outbound call. Reads and swaps may race across goroutines, so both go through the mutex.

```go
type TokenSource struct {
    // contains filtered or unexported fields
}
```

<a name="Dial"></a>
### func [Dial](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/dial.go#L38>)

```go
func Dial(addr, token string, allowInsecure bool, caFile string) (agentv1.AgentServiceClient, *grpc.ClientConn, *TokenSource, error)
```

Dial connects to the control plane's AgentService, attaching the bearer token to every RPC. When allowInsecure is true \(local development against a cluster without TLS\) the transport is unencrypted; otherwise TLS 1.2\+ is required. When caFile is set, the server certificate is verified against that CA bundle \(a self\-signed / cluster CA\); otherwise the system roots are used. The bundle is read here, so a missing or empty file fails the dial, and read again on every later handshake, so a reconnect trusts a rotated bundle \(one that holds the old and new CA during a CA rotation\) without restarting the agent \(\#637\).

The channel pings the control plane when idle and every unary call carries a deadline \(see defaultDialTuning\), so a control plane that stops answering is noticed in seconds rather than when the kernel gives up on the socket.

It also returns the \*TokenSource backing the per\-RPC credential: the heartbeat loop swaps a renewed token into it \(ADR 0055 Fix \#4\) and the interceptor picks the new bearer up on the next call.

The channel reconnects with reconnectBackoff, so once the control plane is back it is redialed within about one heartbeat interval, however long the outage lasted \(\#899\).

<a name="NewTokenSource"></a>
### func [NewTokenSource](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/auth.go#L26>)

```go
func NewTokenSource(token string) *TokenSource
```

NewTokenSource seeds a TokenSource with the dispatch token.

<a name="TokenSource.Set"></a>
### func \(\*TokenSource\) [Set](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/auth.go#L40>)

```go
func (s *TokenSource) Set(token string)
```

Set atomically swaps the bearer used by subsequent RPCs. An empty token is ignored so a "no renewal this beat" response never blanks a working credential.

<a name="TokenSource.Token"></a>
### func \(\*TokenSource\) [Token](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/auth.go#L31>)

```go
func (s *TokenSource) Token() string
```

Token returns the current bearer.

<a name="WarmRunner"></a>
## type [WarmRunner](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/warm.go#L41-L169>)

WarmRunner is the client side of the warm\-worker transport \(ADR 0058 D4\): a long\-lived process that registers once, opens the AwaitAssignment bidi stream, and serves MANY task attempts — one at a time — each in a fresh forked child.

Two identities are kept deliberately separate:

- StreamClient carries the worker's BOOTSTRAP identity. Register and the AwaitAssignment control stream run on it and never adopt an attempt token, so the pod's membership in the pool is stable for the worker's whole life.
- WorkClient carries each attempt's PER\-ATTEMPT identity. Its per\-RPC credential reads AttemptTokens, which the loop swaps to the assignment's attempt\_token before running. Because attempts are strictly sequential, no two attempts' RPCs are ever in flight at once, so the swap is race\-free; and because the swap only touches AttemptTokens \(a different TokenSource / dial from the stream\), it never disturbs the already\-open bootstrap stream, whose authorization header was sent once at stream open.

In production StreamClient and WorkClient are two dials of the same control plane \(see cmd/dexaflow\-agent\), one bound to the bootstrap TokenSource and one to AttemptTokens. They may be the same client only in tests that don't exercise the credential.

```go
type WarmRunner struct {
    StreamClient  agentv1.AgentServiceClient
    WorkClient    agentv1.AgentServiceClient
    AttemptTokens *TokenSource

    // StreamTokens is the bootstrap stream's own TokenSource (the source
    // StreamClient's per-RPC credential reads). Under the exchange transport it is
    // seeded with the projected ServiceAccount token; the exchange step below swaps
    // the WORKER-scoped JWT into it before Register, so Register + AwaitAssignment
    // carry the worker credential. Distinct from AttemptTokens (the WorkClient's
    // per-attempt source). Unused under the env-var transport.
    StreamTokens *TokenSource

    // ExchangeBootstrap runs the projected-token → worker-JWT exchange (ADR 0058 D2)
    // on StreamClient before Register, on the INITIAL connect and on every reconnect,
    // swapping the minted worker JWT into StreamTokens. It is set under the exchange
    // transport; false under the env-var default (Register uses the seed token as-is).
    ExchangeBootstrap bool

    // NewSink opens a fresh per-attempt log sink on the WorkClient, so each
    // attempt's logs are shipped under its own attempt_token. Nil (or a returned
    // error) falls back to NoopLogSink — logs are best-effort, never fatal.
    NewSink func(ctx context.Context) (LogSink, error)

    Cmd      CommandRunner
    Hostname string
    Version  string
    Env      []string // base process environment (typically os.Environ())

    // PodName is the worker's OWN Kubernetes pod name, read from LEOFLOW_POD_NAME
    // (injected via the downward API) in main.go. It is sent up in WorkerRegister
    // so the control plane can bind a started attempt to it as the durable
    // warm_worker_id (ADR 0058 N1d-a1). Empty outside Kubernetes (e.g. tests); the
    // binding then simply degrades to per-pod liveness for this worker.
    PodName string

    // ScratchDir is the agent-owned per-attempt scratch root. It is wiped and
    // recreated before every attempt (D4 isolation) and holds the return-value,
    // extra-links, xcom-pushes, and reschedule files the runtime writes.
    ScratchDir string

    // AttemptHome is the read-only-root isolation mode (X3.2), set from
    // LEOFLOW_WARM_ATTEMPT_HOME. Each attempt then gets its own HOME and XDG dirs
    // under ScratchDir, and resetScratch also empties the writable paths a
    // read-only root leaves: SharedTmpDir (the pod's /tmp emptyDir) except for
    // ScratchDir itself, and SharedMemDir (/dev/shm, a pod-wide tmpfs). The sweep
    // runs before each ack and again as soon as an attempt ends, before SlotFree,
    // so a generated dbt profile or a planted script neither reaches the next
    // attempt nor sits on an idle worker. Off leaves HOME, /tmp and /dev/shm
    // exactly as before.
    AttemptHome  bool
    SharedTmpDir string
    SharedMemDir string

    // TerminationLogPath and HeartbeatInterval mirror the single-shot Runner's
    // fields and are threaded into every per-attempt Runner.
    TerminationLogPath string
    HeartbeatInterval  time.Duration

    // StreamHeartbeat is how often the worker re-sends its WorkerRegister on the
    // open assignment stream, idle or busy, as a liveness heartbeat. The control
    // plane allows one live registration per worker identity and judges liveness
    // by these heartbeats, so a duplicate registration of a live worker is refused
    // while a wedged stream can still be replaced. Zero uses
    // DefaultHeartbeatInterval; a negative value disables it. A control plane
    // that predates the heartbeat ignores a re-registration on an established
    // stream, so it is safe in a mixed-version rollout.
    StreamHeartbeat time.Duration

    // Self-lifecycle bounds (ADR 0058 D9/D10/D6/H3), populated from the warm-pod env
    // in main.go. A warm worker that exits is replaced by the reconciler
    // (RestartPolicy:Never + busy-aware create), so bounding its own life is how a
    // pool stays fresh and scales down. Each bound is disabled when zero/unset — a
    // defensive default; the operator config values are non-zero.
    //
    //   - MaxAttempts: drain after this many completed attempts (D9/D10).
    //   - MaxLifetime: drain once the worker is this old (D9/D10).
    //   - IdleTTL: idle-recycle after this long awaiting the next assignment (D6).
    //   - AttemptWatchdog: hard per-attempt ceiling, INDEPENDENT of the task's
    //     execution_timeout, so a task that declares no timeout and then wedges is
    //     still killed and the slot freed (H3).
    MaxAttempts     int
    MaxLifetime     time.Duration
    IdleTTL         time.Duration
    AttemptWatchdog time.Duration

    // Reconnect-toward-the-leader bounds (warm-pool Hole B). On a not-leader
    // rejection (FailedPrecondition — the leader-gate refusal, or warm-pools not
    // serving on this endpoint) the worker RECONNECTS with jittered exponential
    // backoff instead of exiting: a single pod finds the leader across the Service's
    // rotation without the pod-create-die-recreate churn a fresh pod would cause. The
    // reconnect is bounded so a genuinely misconfigured deployment eventually exits
    // (non-zero) and lets the reconciler replace it, rather than spinning forever.
    // Zero values fall back to production defaults; tests inject tiny ones.
    //
    //   - ReconnectBackoff: base backoff, doubled each consecutive reconnect.
    //   - ReconnectMaxBackoff: ceiling on a single backoff sleep.
    //   - MaxReconnects: consecutive not-leader reconnects before giving up. A
    //     stream a leader accepted starts the count over (see leaderAccepted).
    ReconnectBackoff    time.Duration
    ReconnectMaxBackoff time.Duration
    MaxReconnects       int

    // Redial re-establishes the StreamClient toward the control plane for the next
    // reconnect. A FRESH dial is what lets the worker reach the leader: a single gRPC
    // connection sticks to one Service backend, so retrying the same client would
    // re-hit the same follower forever — only a new connection re-rotates. It returns
    // the new stream client, its TokenSource (so an exchange-transport worker
    // re-exchanges into the fresh connection's bearer), and a closer for the
    // connection it opened (closed on the next reconnect / exit). Nil disables
    // re-dial — the reconnect then retries the existing StreamClient (tests inject a
    // fake that eventually serves); production wires a real re-dial via agent.Dial.
    Redial func() (agentv1.AgentServiceClient, *TokenSource, io.Closer, error)
    // contains filtered or unexported fields
}
```

<a name="WarmRunner.Run"></a>
### func \(\*WarmRunner\) [Run](<https://github.com/dexadata/dexaflow/blob/main/internal/agent/warm.go#L204>)

```go
func (w *WarmRunner) Run(ctx context.Context, dagVersionID string) error
```

Run serves the warm\-worker lifecycle, reconnecting toward the scheduler leader on a not\-leader rejection \(warm\-pool Hole B\). Each serve\(\) registers, opens the assignment stream, and serves assignments until the stream ends \(server close / drain\), ctx is canceled, or a self\-lifecycle bound trips — all clean exits \(nil\). A FailedPrecondition from serve\(\) is the not\-leader rejection \(the leader\-gate, or warm pools not serving on this endpoint\): Run re\-dials and retries with jittered exponential backoff so one pod finds the leader across the Service's rotation, bounded by MaxReconnects so a misconfigured deployment eventually exits and lets the reconciler replace it. Any other error \(a real registration / stream transport failure, or a fail\-closed scratch failure\) is returned as before. A failed TASK is a normal outcome and never ends serve\(\).

Generated by [gomarkdoc](<https://github.com/princjef/gomarkdoc>)
