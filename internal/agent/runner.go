package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	mathrand "math/rand/v2"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// CommandRunner executes the user task process, writing its stdout and stderr to
// the supplied writers and returning the process exit code.
type CommandRunner interface {
	Run(ctx context.Context, argv, env []string, stdout, stderr io.Writer) (exitCode int, err error)
}

// rescheduleExitCode is the process exit code a reschedule-mode sensor uses to
// signal "not ready, re-dispatch me later" (it also writes the next-poke time to
// ReschedulePath). It mirrors RESCHEDULE_EXIT_CODE in the Python runtime; 75 is
// sysexits' EX_TEMPFAIL, distinct from the 0/non-zero success/failure mapping.
const rescheduleExitCode = 75

// LogSink receives log lines produced by the user task. Sends are best-effort.
type LogSink interface {
	Send(line *agentv1.LogLine) error
	Close() error
}

// NoopLogSink discards log lines. The agent falls back to it when the control
// plane log stream is unavailable (e.g. StreamLogs not yet implemented), so a
// task still runs even though its logs are not shipped this run.
type NoopLogSink struct{}

// Send discards the line.
func (NoopLogSink) Send(*agentv1.LogLine) error { return nil }

// Close is a no-op.
func (NoopLogSink) Close() error { return nil }

// Runner orchestrates a single task execution inside the worker container: it
// registers with the control plane, fetches the task spec and XCom inputs, runs
// the user process while streaming logs, pushes the return value, and reports the
// terminal state.
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
	// files — a token cache, a dbt profile, ~/.aws-style credentials (#728). Empty
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
	// disables it — Lite (subprocess, in-process report) needs no such record.
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
	// fault-injection seam for the durable-outcome E2E (ADR 0052) — the agent
	// binary wires it, from an env var, to exit the process, simulating a pod
	// killed mid-report with the record already on disk. Nil in production.
	BeforeReport func(agentv1.TaskState)
	// afterFunc returns a channel that fires after the given delay; it exists so
	// tests can make the report-retry backoff instant. Nil uses time.After.
	afterFunc func(time.Duration) <-chan time.Time
	// attemptEpoch is the execution of the try this attempt is, from its task
	// spec, stamped on the outcome record (ADR 0052 amendment). It is reset for
	// every attempt a warm worker runs; 0 until the spec is read.
	attemptEpoch int64
	// Resolver resolves a declared secret name from an external backend, pod-side
	// (ADR 0060). Nil = no external backend configured: the resolution chain is the
	// vault only, byte-identical to the pre-0060 env-export. SecretBackend is the
	// operator-configured routing (which kinds/names are externally sourced); its
	// zero value covers nothing, so a nil-or-zero pair never calls the resolver.
	Resolver      secretsource.SecretResolver
	SecretBackend secretsource.Backend
}

// after waits for d, using the injected afterFunc when set (tests) and time.After
// otherwise.
func (r *Runner) after(d time.Duration) <-chan time.Time {
	if r.afterFunc != nil {
		return r.afterFunc(d)
	}
	return time.After(d)
}

// Run executes the task lifecycle and returns an error if the task failed. In
// single-shot mode the agent registers once and serves exactly one attempt, so
// Run is register followed by runOneAttempt. The warm worker (warm.go) reuses
// runOneAttempt directly, registering separately and driving many attempts.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.register(ctx); err != nil {
		return err
	}
	return r.runOneAttempt(ctx)
}

// runOneAttempt fetches the task spec, builds the command and its environment, and
// runs the user process to its terminal ReportState. It is the whole per-attempt
// body: a single-shot agent calls it once (via Run); a warm worker calls it once
// per WorkAssignment, each time in a fresh forked child with a freshly-built env.
// It does NOT register — registration is a per-worker concern the caller owns.
func (r *Runner) runOneAttempt(ctx context.Context) error {
	r.attemptEpoch = 0
	spec, err := r.Client.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
	if err != nil {
		return fmt.Errorf("fetching task spec: %w", err)
	}
	r.attemptEpoch = spec.GetAttemptEpoch()
	argv, err := BuildCommand(spec.GetOperator(), spec.GetEntrypoint(), spec.GetOperatorClass())
	if err != nil {
		return err
	}
	env, err := r.buildEnv(ctx, spec)
	if err != nil {
		// A buildEnv failure — notably a hard external-secret resolver error (ADR
		// 0060 B6) — means the task cannot run. Report FAILED with the reason so the
		// TI settles as failed with a visible cause, instead of being stranded until
		// a reaper marks it agent_lost.
		//
		// The message and the classification are deliberately different values. The
		// message is the raw error, which belongs in the report and the logs; the
		// DURABLE record only ever carries what classifyEnvFailure recognizes, and
		// "" for everything else. Handing err.Error() to both would put a wrapped
		// gRPC error — control-plane endpoint, TLS handshake text — on a durable
		// field readable by anyone with pod read access in the task namespace
		// (#930).
		return r.failWithReason(ctx, 1, err.Error(), classifyEnvFailure(err))
	}
	return r.execute(ctx, argv, env, time.Duration(spec.GetExecutionTimeoutSeconds())*time.Second)
}

func (r *Runner) register(ctx context.Context) error {
	if _, err := r.Client.Register(ctx, &agentv1.RegisterRequest{
		AgentVersion: r.Version,
		Hostname:     r.Hostname,
	}); err != nil {
		return fmt.Errorf("registering agent: %w", err)
	}
	return nil
}

func (r *Runner) buildEnv(ctx context.Context, spec *agentv1.TaskSpec) ([]string, error) {
	fetcher := r.prefetchXCom(ctx, spec)
	var xcom []string
	for param, upstreams := range spec.GetXcomInputMapping() {
		taskIDs := upstreams.GetTaskIds()
		switch len(taskIDs) {
		case 0:
			// Empty upstream list — parser invariant prevents this, but guard anyway.
			continue
		case 1:
			// Single upstream: deliver the raw return_value JSON as-is, so a task
			// declaring `def f(x: dict)` receives the upstream's dict (not a
			// 1-element list wrapping it). Matches Airflow's TaskFlow semantics.
			resp, err := fetcher.FetchXCom(ctx, &agentv1.FetchXComRequest{
				UpstreamTaskId: taskIDs[0],
				Key:            "return_value",
			})
			if status.Code(err) == codes.NotFound {
				slog.Debug("declared xcom input is absent; leaving it unset", "param", param, "upstream", taskIDs[0])
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("fetching xcom %q from %q: %w", param, taskIDs[0], err)
			}
			xcom = append(xcom, XComEnvVar(param, resp.GetValue()))
		default:
			// Fan-in: each upstream's return_value becomes one element of a JSON
			// array, in declaration order. An absent upstream contributes `null`
			// so the function still receives len(upstreams) elements.
			collected, err := fetchFanInValues(ctx, fetcher, param, taskIDs)
			if err != nil {
				return nil, err
			}
			xcom = append(xcom, XComEnvVar(param, collected))
		}
	}
	env := mergeEnv(r.Env, spec.GetEnvironment(), xcom)
	// Force-unbuffered Python and explicit UTF-8 for every spawned subprocess
	// — including any python re-execed by the user's task. The `-u` flag on
	// argv (see BuildCommand) only covers the top-level interpreter; this env
	// var also flows to children. Without it, a user's print() bytes may sit
	// in Python's block buffer when the process is killed (SIGKILL/OOM/evict)
	// and never reach the agent's pipe.
	env = append(env, "PYTHONUNBUFFERED=1", "PYTHONIOENCODING=UTF-8")
	env = append(env, runContextEnv(spec)...)
	byTaskEnv, err := xcomByTaskEnv(ctx, fetcher, spec)
	if err != nil {
		return nil, err
	}
	env = append(env, byTaskEnv...)
	if callArgs := spec.GetCallArgsJson(); callArgs != "" {
		// TaskFlow literal call args (#115). The runtime decodes this and merges
		// values into the user function's kwargs. XCom upstreams take precedence
		// at runtime for any same-name parameter. The env var name keeps
		// Airflow's DAG-run `params` term free for a future feature (#148).
		env = append(env, "LEOFLOW_CALL_ARGS_JSON="+callArgs)
	}
	if opArgs := spec.GetOperatorArgsJson(); opArgs != "" {
		// Operator constructor kwargs (ADR 0040). The runtime's --operator mode
		// decodes this and instantiates the captured provider operator with it.
		env = append(env, "LEOFLOW_OPERATOR_ARGS="+opArgs)
	}
	secretEnv, err := r.secretsEnv(ctx, spec)
	if err != nil {
		return nil, err
	}
	env = append(env, secretEnv...)
	pathEnv, err := r.outputPathEnv()
	if err != nil {
		return nil, err
	}
	return append(env, pathEnv...), nil
}

// outputPathEnv renders the env vars that point the runtime at the agent-owned
// per-task output files, plus the TMPDIR redirect. Each is set only when its path
// field is configured (single-shot leaves some unset). TMPDIR is created and comes
// last so it overrides any TMPDIR inherited from the pod environment.
func (r *Runner) outputPathEnv() ([]string, error) {
	var env []string
	if r.ReturnPath != "" {
		// Tell the runtime to write the return value to the agent's per-task path,
		// not the shared global default — so concurrent tasks and other users never
		// collide on /tmp/leoflow_return_value.json.
		env = append(env, "LEOFLOW_RETURN_VALUE_PATH="+r.ReturnPath)
	}
	if r.LinksPath != "" {
		// The runtime writes the operator's computed extra-links here (#375).
		env = append(env, "LEOFLOW_EXTRA_LINKS_PATH="+r.LinksPath)
	}
	if r.PushesPath != "" {
		// The runtime writes the operator's custom-keyed XCom pushes here (multi-key
		// XCom). Off the LEOFLOW_XCOM_ prefix so _merge_operator_xcom does not consume it.
		env = append(env, "LEOFLOW_PUSHES_PATH="+r.PushesPath)
	}
	if r.ReschedulePath != "" {
		// A reschedule-mode sensor writes its next-poke time here and exits with
		// rescheduleExitCode; the agent then reports up_for_reschedule (#380).
		env = append(env, "LEOFLOW_RESCHEDULE_PATH="+r.ReschedulePath)
	}
	if r.TmpDir != "" {
		// Redirect TMPDIR into the per-attempt scratch dir (wiped by resetScratch
		// between attempts) so anything the child writes to $TMPDIR — a token cache,
		// a dbt profile, ~/.aws-style credentials — is gone before the next attempt
		// runs on this warm worker (#728). Appended last so it overrides any TMPDIR
		// inherited from the pod's environment; tools treat TMPDIR as ephemeral, so
		// redirecting it is safe.
		if err := os.MkdirAll(r.TmpDir, 0o700); err != nil {
			return nil, fmt.Errorf("creating per-attempt TMPDIR %q: %w", r.TmpDir, err)
		}
		env = append(env, "TMPDIR="+r.TmpDir)
	}
	if r.HomeDir != "" {
		homeEnv, err := attemptHomeEnv(r.HomeDir)
		if err != nil {
			return nil, err
		}
		env = append(env, homeEnv...)
	}
	return env, nil
}

// attemptHomeEnv creates a per-attempt HOME and its XDG base dirs and returns the
// env that points the child at them. Appended after the task's own env, so it
// overrides a HOME or XDG_* the image or the DAG set: on a read-only root those
// would either be unwritable or, under /tmp, shared with the next attempt.
func attemptHomeEnv(home string) ([]string, error) {
	dirs := [][2]string{
		{"HOME", home},
		{"XDG_CONFIG_HOME", filepath.Join(home, ".config")},
		{"XDG_CACHE_HOME", filepath.Join(home, ".cache")},
		{"XDG_DATA_HOME", filepath.Join(home, ".local", "share")},
		{"XDG_STATE_HOME", filepath.Join(home, ".local", "state")},
	}
	env := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if err := os.MkdirAll(d[1], 0o700); err != nil {
			return nil, fmt.Errorf("creating per-attempt %s %q: %w", d[0], d[1], err)
		}
		env = append(env, d[0]+"="+d[1])
	}
	return env, nil
}

// upstreamXComEnv is the env var carrying the upstream task_id -> return_value map the
// runtime's ti.xcom_pull reads. It deliberately does NOT start with "LEOFLOW_XCOM_":
// the runtime's _merge_operator_xcom consumes every LEOFLOW_XCOM_<PARAM> var as a
// param-bound operator kwarg and would otherwise inject this whole map as a bogus
// by_task= kwarg (a collision the live GCP chain test caught).
const upstreamXComEnv = "LEOFLOW_UPSTREAM_XCOM"

// xcomByTaskEnv fetches each upstream's return_value and renders the upstreamXComEnv
// map the runtime's ti.xcom_pull reads, so a captured operator can pull a chained
// upstream's output like in Airflow (ADR 0040). Only captured operators use
// ti.xcom_pull — a python @task gets its inputs via the param-keyed xcom_input_mapping
// — so the map is built for airflow_operator tasks only, avoiding wasted fetches. An
// upstream with no return_value is omitted (pulls as None). nil when nothing to deliver.
func xcomByTaskEnv(ctx context.Context, fetcher xcomFetcher, spec *agentv1.TaskSpec) ([]string, error) {
	if spec.GetOperator() != "airflow_operator" {
		return nil, nil
	}
	byTask := map[string]json.RawMessage{}
	for _, taskID := range spec.GetDependsOn() {
		resp, err := fetcher.FetchXCom(ctx, &agentv1.FetchXComRequest{
			UpstreamTaskId: taskID,
			Key:            "return_value",
		})
		if status.Code(err) == codes.NotFound {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fetching xcom for upstream %q: %w", taskID, err)
		}
		byTask[taskID] = resp.GetValue()
	}
	if len(byTask) == 0 {
		return nil, nil
	}
	encoded, err := json.Marshal(byTask)
	if err != nil {
		return nil, fmt.Errorf("encoding xcom-by-task map: %w", err)
	}
	return []string{upstreamXComEnv + "=" + string(encoded)}, nil
}

// runContextEnv renders the DagRun/task identity the runtime's standalone operator
// context reads (_StandaloneTaskInstance / _operator_context, ADR 0040): without it
// the context is blank in every executor — ti.task_id="", run_id="", try_number
// defaulting to 1, and run_operator falling back to the operator class name as the
// task_id — a silent-wrong gap for operators that read them. The server carries
// these on the TaskSpec. ts/ds are derived from the single logical_date (ts is the
// RFC3339 value, ds its UTC date). The macros params/data_interval need fields not
// yet on TaskSpec and are deliberately left unset rather than fabricated.
func runContextEnv(spec *agentv1.TaskSpec) []string {
	var env []string
	if v := spec.GetTaskId(); v != "" {
		env = append(env, "LEOFLOW_TASK_ID="+v)
	}
	if v := spec.GetRunId(); v != "" {
		env = append(env, "LEOFLOW_RUN_ID="+v)
	}
	if v := spec.GetDagId(); v != "" {
		env = append(env, "LEOFLOW_DAG_ID="+v)
	}
	if n := spec.GetTryNumber(); n > 0 {
		env = append(env, fmt.Sprintf("LEOFLOW_TRY_NUMBER=%d", n))
	}
	if n := spec.GetMaxTries(); n > 0 {
		env = append(env, fmt.Sprintf("LEOFLOW_MAX_TRIES=%d", n))
	}
	// Signal the runtime to run the task's on_failure_callback on its final failure
	// (#424); the runtime re-imports dag.py for the callable only when this is set.
	if spec.GetOnFailureCallback() {
		env = append(env, "LEOFLOW_ON_FAILURE_CALLBACK=1")
	}
	if ld := spec.GetLogicalDate(); ld != "" {
		env = append(env, "LEOFLOW_TS="+ld)
		if t, perr := time.Parse(time.RFC3339, ld); perr == nil {
			env = append(env, "LEOFLOW_DS="+t.UTC().Format("2006-01-02"))
		}
	}
	if v := spec.GetDataIntervalStart(); v != "" {
		env = append(env, "LEOFLOW_DATA_INTERVAL_START="+v)
	}
	if v := spec.GetDataIntervalEnd(); v != "" {
		env = append(env, "LEOFLOW_DATA_INTERVAL_END="+v)
	}
	if v := spec.GetParamsJson(); v != "" {
		env = append(env, "LEOFLOW_PARAMS="+v)
	}
	if v := spec.GetFirstRescheduleAt(); v != "" {
		// Delivered to a re-dispatched reschedule-mode sensor so its
		// get_first_reschedule_date returns the real first time and `timeout` is
		// honored cumulatively across pokes (#380). Empty on the first attempt.
		env = append(env, "LEOFLOW_FIRST_RESCHEDULE_AT="+v)
	}
	return env
}

// secretsEnv fetches the tenant's Variables/Connections and renders them as
// AIRFLOW_VAR_<KEY> / AIRFLOW_CONN_<ID> so Airflow's native env secrets backend
// (and plain os.environ) resolve them (ADR 0021). Best-effort: a fetch failure
// (e.g. an insecure channel refusing secrets) logs and is skipped, so tasks that
// do not use Variables/Connections still run.
// secretsEnv builds the AIRFLOW_VAR_*/AIRFLOW_CONN_* env for the task. The
// resolution chain (ADR 0060) is: declared name → external backend → leoflow
// vault → env. The vault RPCs stay the declaration-scoped, liveness-gated source
// they always were; when an external backend is configured, a declared name it
// covers is resolved pod-side and OVERRIDES the vault entry for that name.
//
// Fail-closed / liveness (ADR 0060 B2/B6): a hard resolver error (not a clean
// miss) fails the task. If a vault RPC returns PermissionDenied — the
// liveness-enforce denial (ADR 0055) — external resolution is skipped entirely: a
// non-live task instance resolves nothing, from the vault or externally. Other
// vault errors stay best-effort (a task using no secrets still runs).
func (r *Runner) secretsEnv(ctx context.Context, spec *agentv1.TaskSpec) ([]string, error) {
	vars := map[string]string{}
	conns := map[string]string{}
	// vaultDenied records a liveness-enforce PermissionDenied on either vault RPC;
	// it gates the external branch (B2) so a non-live TI resolves nothing.
	vaultDenied := false
	if resp, err := r.Client.GetVariables(ctx, &agentv1.GetVariablesRequest{}); err != nil {
		if status.Code(err) == codes.PermissionDenied {
			vaultDenied = true
		}
		slog.Warn("fetching variables; Variable.get may be unavailable", "error", err)
	} else {
		for k, v := range resp.GetVariables() {
			vars[k] = v
		}
	}
	if resp, err := r.Client.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); err != nil {
		if status.Code(err) == codes.PermissionDenied {
			vaultDenied = true
		}
		slog.Warn("fetching connections; get_connection may be unavailable", "error", err)
	} else {
		for id, uri := range resp.GetConnectionUris() {
			conns[id] = uri
		}
	}

	// External backend: resolve the DECLARED names the operator's backend covers,
	// overriding the vault. Skipped when no backend is configured or the vault RPC
	// was liveness-denied (B2). A hard resolver error fails the task closed (B6).
	if r.Resolver != nil && !vaultDenied {
		resolved, err := r.resolveExternal(ctx, coveredRefs(spec, r.SecretBackend))
		if err != nil {
			// Mark the failure by KIND so classifyEnvFailure can recognize it without
			// reading the provider's error text, which must never reach the durable
			// outcome record (#930).
			return nil, fmt.Errorf("%w: %w", errSecretResolution, err)
		}
		for ref, val := range resolved {
			if ref.Kind == secretsource.KindConnection {
				conns[ref.Name] = val
			} else {
				vars[ref.Name] = val
			}
		}
		// Observability for the top field-support signal (#1): a declared name that
		// resolves from neither the backend nor the vault otherwise fails downstream
		// with a bare "not delivered / AIRFLOW_*_ unset" and no trace of why. conns
		// and vars are final here (vault populated above, external override applied).
		r.warnUnresolvedDeclared(spec, vars, conns)
	}

	out := make([]string, 0, len(vars)+len(conns))
	for k, v := range vars {
		out = append(out, "AIRFLOW_VAR_"+strings.ToUpper(k)+"="+v)
	}
	for id, uri := range conns {
		out = append(out, "AIRFLOW_CONN_"+strings.ToUpper(id)+"="+uri)
	}
	return out, nil
}

// warnUnresolvedDeclared logs the declared names the operator's backend covers but
// that resolved from neither the backend nor the vault (#1). A backend miss is
// often not a clean miss: the provider backend reports an auth/permission failure
// (e.g. the pod ran as the wrong ServiceAccount) as a None, indistinguishable from
// "no such secret", so the task otherwise fails downstream with no trace. Only
// names of kinds the backend covers are checked, so a plain author typo on a
// non-covered name is not misattributed to the backend identity.
func (r *Runner) warnUnresolvedDeclared(spec *agentv1.TaskSpec, vars, conns map[string]string) {
	var unresolved []string
	if r.SecretBackend.Covers(secretsource.KindVariable) {
		for _, n := range spec.GetDeclaredVariables() {
			if _, ok := vars[n]; !ok {
				unresolved = append(unresolved, "variable "+n)
			}
		}
	}
	if r.SecretBackend.Covers(secretsource.KindConnection) {
		for _, n := range spec.GetDeclaredConnections() {
			if _, ok := conns[n]; !ok {
				unresolved = append(unresolved, "connection "+n)
			}
		}
	}
	if len(unresolved) > 0 {
		slog.Warn("declared secrets unresolved after the external backend and the vault; "+
			"if these should come from the external backend, check the task pod's identity and permissions "+
			"(the provider backend reports an auth failure as a miss, not an error)",
			"unresolved", unresolved)
	}
}

// coveredRefs is the set of declared names the operator's backend covers — the
// exact request set the external resolver is asked for (declaration stays the
// scope authority, ADR 0055/0060).
func coveredRefs(spec *agentv1.TaskSpec, b secretsource.Backend) []secretsource.Ref {
	var refs []secretsource.Ref
	if b.Covers(secretsource.KindVariable) {
		for _, n := range spec.GetDeclaredVariables() {
			refs = append(refs, secretsource.Ref{Name: n, Kind: secretsource.KindVariable})
		}
	}
	if b.Covers(secretsource.KindConnection) {
		for _, n := range spec.GetDeclaredConnections() {
			refs = append(refs, secretsource.Ref{Name: n, Kind: secretsource.KindConnection})
		}
	}
	return refs
}

// resolveExternal resolves the given refs against the backend, preferring one
// batched call (the 2b subprocess pays a heavy startup) and falling back to the
// per-name port. Only hits are returned; a hard error fails the task closed (B6).
func (r *Runner) resolveExternal(ctx context.Context, refs []secretsource.Ref) (map[secretsource.Ref]string, error) {
	if len(refs) == 0 {
		return map[secretsource.Ref]string{}, nil
	}
	if br, ok := r.Resolver.(secretsource.BatchResolver); ok {
		return br.ResolveBatch(ctx, refs)
	}
	out := make(map[secretsource.Ref]string, len(refs))
	for _, ref := range refs {
		v, found, err := r.Resolver.Resolve(ctx, ref.Name, ref.Kind)
		if err != nil {
			return nil, fmt.Errorf("resolving external secret %q: %w", ref.Name, err)
		}
		if found {
			out[ref] = v
		}
	}
	return out, nil
}

// fetchFanInValues fetches each upstream's return_value and assembles them into
// a JSON array, in declaration order. Each element is the raw JSON of that
// upstream's return value, or `null` if the upstream produced no XCom (Airflow
// semantics: missing XCom is None). The function the runtime calls receives
// this as `list[T]` — len(upstreams) elements, never fewer.
func fetchFanInValues(ctx context.Context, client xcomFetcher, param string, upstreams []string) ([]byte, error) {
	pieces := make([][]byte, 0, len(upstreams))
	for _, upstream := range upstreams {
		resp, err := client.FetchXCom(ctx, &agentv1.FetchXComRequest{
			UpstreamTaskId: upstream,
			Key:            "return_value",
		})
		if status.Code(err) == codes.NotFound {
			pieces = append(pieces, []byte("null"))
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("fan-in: fetching xcom %q from %q: %w", param, upstream, err)
		}
		// Each upstream's payload is already JSON; concat with commas inside `[…]`.
		pieces = append(pieces, resp.GetValue())
	}
	var b []byte
	b = append(b, '[')
	for i, p := range pieces {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, p...)
	}
	b = append(b, ']')
	return b, nil
}

func (r *Runner) execute(ctx context.Context, argv, env []string, timeout time.Duration) error {
	if err := r.report(ctx, agentv1.TaskState_TASK_STATE_RUNNING, 0, ""); err != nil {
		return err
	}
	stdout := &logWriter{sink: r.Sink, stream: "stdout", level: agentv1.LogLevel_LOG_LEVEL_INFO}
	stderr := &logWriter{sink: r.Sink, stream: "stderr", level: agentv1.LogLevel_LOG_LEVEL_ERROR}

	runCtx, cancel := context.WithCancel(ctx)
	// Join the heartbeat goroutine before this attempt returns. Its last
	// Token.Set (adopting a renewed bearer) must complete before the caller
	// swaps to the next attempt's token — in warm mode r.Token is the SHARED
	// AttemptTokens and attempts are sequential, so a lingering heartbeat
	// finishing a Set after attempt N+1 adopted its token would make N+1 run
	// under N's superseded credential. cancel() signals the loop to stop, THEN
	// Wait() blocks for its in-flight cycle to finish; the order matters (a
	// single deferred func, not two defers). Single-shot is unaffected — the
	// process exits after Run either way; the join just adds a bounded wait.
	var hbWG sync.WaitGroup
	if r.HeartbeatInterval > 0 {
		hbWG.Add(1)
		go func() { defer hbWG.Done(); r.heartbeat(runCtx, cancel) }()
	}
	defer func() { cancel(); hbWG.Wait() }()

	// `execution_timeout_seconds` enforcement (#194): wrap the runCtx in a
	// deadline so a wedged user process is interrupted at the boundary the user
	// declared, not at the 90 s heartbeat reaper. Zero (unset) preserves the
	// previous "no time bound" behavior. The deadline-fired vs heartbeat-canceled
	// vs parent-canceled cases are disambiguated below by inspecting
	// timeoutCtx.Err() after Cmd.Run returns.
	timeoutCtx := runCtx
	if timeout > 0 {
		var timeoutCancel context.CancelFunc
		timeoutCtx, timeoutCancel = context.WithTimeout(runCtx, timeout)
		defer timeoutCancel()
	}

	// Frame the run so a task with no print() still has visible logs in the UI —
	// matching real Airflow, which always emits start/end framing (#119).
	start := time.Now()
	emitTaskStarted(r.Sink)
	emitTaskBoot(r.Sink, argv, env)
	exitCode, runErr := r.Cmd.Run(timeoutCtx, argv, env, stdout, stderr)
	stdout.flush()
	stderr.flush()
	emitTaskEnded(r.Sink, exitCode, runErr, time.Since(start))
	if cerr := r.Sink.Close(); cerr != nil {
		slog.Warn("closing log stream", "error", cerr)
	}

	// Override the failure reason when the timeout fired. The check is on
	// timeoutCtx (not runCtx or ctx) so a heartbeat-driven cancel or a
	// parent SIGTERM still flows through the generic fail path.
	if timeout > 0 && errors.Is(timeoutCtx.Err(), context.DeadlineExceeded) {
		msg := fmt.Sprintf("execution_timeout: task exceeded %s limit", timeout)
		// The message doubles as the classification here, and only here: it is built
		// from a fixed template whose only variable is the declared timeout, so it
		// carries nothing derived from an error.
		return r.failWithReason(ctx, exitCode, msg, msg)
	}
	// A reschedule-mode sensor poked not-ready: it exits with rescheduleExitCode AND
	// leaves its next-poke time in ReschedulePath. The FILE is the real signal — a
	// user task that exits 75 (EX_TEMPFAIL) with no file is an ordinary failure, not
	// a reschedule (#386). Report up_for_reschedule only when both line up, before
	// the generic non-zero-exit path; otherwise fall through and fail normally.
	if runErr == nil && exitCode == rescheduleExitCode {
		if when, ok := r.readReschedule(); ok {
			return r.reportReschedule(ctx, when)
		}
	}
	if runErr != nil || exitCode != 0 {
		// An ordinary non-zero exit stays UNCLASSIFIED: the record's own
		// "task failed (exit N)" rendering is the better operator string, and the
		// cause is raw error text whose detail belongs in the task logs.
		//
		// An OOM is NOT classified here, deliberately. The agent cannot tell one
		// apart from an external kill without reading its cgroup, and the pod
		// status already carries the answer: the kubelet sets memory.oom.group on
		// cgroup v2, so the container is marked OOMKilled whichever process the
		// kernel picked. The reconciler reads that (#1216).
		return r.fail(ctx, exitCode, runErr, "")
	}
	// The three output pushes below all run AFTER the user process exited 0, so a
	// failure here is the agent's own diagnosis: the task worked and only the
	// delivery of its outputs did not. Unclassified, the record would render as
	// "task failed (exit 0)" — a string that reads as a success and names no cause.
	if err := r.pushReturnValue(ctx); err != nil {
		return r.fail(ctx, 0, err, reasonOutputUndelivered)
	}
	if err := r.pushExtraLinks(ctx); err != nil {
		return r.fail(ctx, 0, err, reasonOutputUndelivered)
	}
	if err := r.pushCustomXComs(ctx); err != nil {
		return r.fail(ctx, 0, err, reasonOutputUndelivered)
	}
	return r.report(ctx, agentv1.TaskState_TASK_STATE_SUCCESS, 0, "")
}

// failWithReason is fail() but with a pre-built error message — used by the
// execution_timeout path so the operator sees "timeout" rather than the
// generic "task exited non-zero" or the raw context.DeadlineExceeded.
//
// msg and reason are SEPARATE parameters on purpose (#930). msg is the reported,
// logged description and may be raw error text. reason is what travels on the
// DURABLE outcome record, so it must be a classification from the closed set in
// failreason.go — or "", which records no reason at all. A call site that has a
// message but no classification for it passes "" and loses nothing: the record
// then renders from the exit code, as it always did.
func (r *Runner) failWithReason(ctx context.Context, exitCode int, msg, reason string) error {
	if rerr := r.reportClassified(ctx, agentv1.TaskState_TASK_STATE_FAILED, clampExit(exitCode), msg, reason); rerr != nil {
		slog.Warn("reporting failed state", "error", rerr)
	}
	return errors.New(msg)
}

// fail reports a terminal failure whose description is an error. reason follows
// the same rule as failWithReason's: a classification constant, or "" to leave the
// durable record carrying only the exit code. cause NEVER reaches the record.
func (r *Runner) fail(ctx context.Context, exitCode int, cause error, reason string) error {
	msg := "task exited non-zero"
	if cause != nil {
		msg = cause.Error()
	}
	if rerr := r.reportClassified(ctx, agentv1.TaskState_TASK_STATE_FAILED, clampExit(exitCode), msg, reason); rerr != nil {
		slog.Warn("reporting failed state", "error", rerr)
	}
	if cause != nil {
		return fmt.Errorf("task failed (exit %d): %w", exitCode, cause)
	}
	return fmt.Errorf("task failed with exit code %d", exitCode)
}

func (r *Runner) pushReturnValue(ctx context.Context) error {
	if r.ReturnPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.ReturnPath)
	if err != nil {
		return err
	}
	if !ok {
		return nil
	}
	resp, err := r.Client.PushXCom(ctx, &agentv1.PushXComRequest{
		Key:         "return_value",
		Value:       value,
		ContentType: "application/json",
	})
	if status.Code(err) == codes.Unimplemented {
		// XCom persistence lands in Phase 4; until then a return value is not
		// stored, but that must not fail an otherwise-successful task.
		slog.Warn("control plane does not implement XCom yet; dropping return value")
		return nil
	}
	if err != nil {
		return fmt.Errorf("pushing return value: %w", err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("control plane rejected return value: %s", resp.GetRejectionReason())
	}
	return nil
}

// pushExtraLinks ships the operator's computed UI deep-link buttons (the runtime wrote
// them to LinksPath) to the control plane as the reserved "_extra_links" XCom, so the
// task Details view can render them (#375). Absent file or no-XCom control plane is a
// no-op — links are UI sugar and must not fail an otherwise-successful task.
func (r *Runner) pushExtraLinks(ctx context.Context) error {
	if r.LinksPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.LinksPath)
	if err != nil || !ok {
		return err
	}
	resp, err := r.Client.PushXCom(ctx, &agentv1.PushXComRequest{
		Key:         "_extra_links",
		Value:       value,
		ContentType: "application/json",
	})
	if status.Code(err) == codes.Unimplemented {
		return nil
	}
	if err != nil {
		return fmt.Errorf("pushing extra links: %w", err)
	}
	if !resp.GetAccepted() {
		return fmt.Errorf("control plane rejected extra links: %s", resp.GetRejectionReason())
	}
	return nil
}

// pushCustomXComs stores the operator's custom-keyed ti.xcom_push values (the runtime
// wrote them to PushesPath as a {key: value} JSON map) as individual XComs, so they
// show in the XCom tab like Airflow (multi-key XCom). Absent file or a no-XCom control
// plane is a no-op — these are observability, not task success.
func (r *Runner) pushCustomXComs(ctx context.Context) error {
	if r.PushesPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.PushesPath)
	if err != nil || !ok {
		return err
	}
	var pushes map[string]json.RawMessage
	if uerr := json.Unmarshal(value, &pushes); uerr != nil {
		return fmt.Errorf("decoding xcom pushes: %w", uerr)
	}
	for _, key := range sortedKeys(pushes) {
		resp, perr := r.Client.PushXCom(ctx, &agentv1.PushXComRequest{
			Key:         key,
			Value:       pushes[key],
			ContentType: "application/json",
		})
		if status.Code(perr) == codes.Unimplemented {
			return nil
		}
		if perr != nil {
			return fmt.Errorf("pushing xcom %q: %w", key, perr)
		}
		if !resp.GetAccepted() {
			return fmt.Errorf("control plane rejected xcom %q: %s", key, resp.GetRejectionReason())
		}
	}
	return nil
}

// sortedKeys returns the map keys sorted, for deterministic push order.
func sortedKeys(m map[string]json.RawMessage) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// heartbeat pings the control plane on an interval while the task runs and
// cancels it when the control plane signals termination.
func (r *Runner) heartbeat(ctx context.Context, cancel context.CancelFunc) {
	ticker := time.NewTicker(r.HeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			resp, err := r.Client.Heartbeat(ctx, &agentv1.HeartbeatRequest{SentAt: timestamppb.Now()})
			if err != nil {
				slog.Warn("heartbeat failed", "error", err)
				continue
			}
			if r.applyHeartbeatResponse(resp) {
				slog.Warn("control plane requested task termination")
				cancel()
				return
			}
		}
	}
}

// applyHeartbeatResponse handles a heartbeat reply: it swaps in a renewed bearer
// (ADR 0055 Fix #4) and reports whether the control plane asked the task to
// terminate. The swap happens before the terminate check, but the two are
// mutually exclusive on the wire — the server sends a renewed token only on the
// live branch and should_terminate only on the superseded branch. The token is
// never logged.
func (r *Runner) applyHeartbeatResponse(resp *agentv1.HeartbeatResponse) (terminate bool) {
	if r.Token != nil {
		r.Token.Set(resp.GetRenewedToken()) // no-op on empty (keep current bearer)
	}
	return resp.GetShouldTerminate()
}

func (r *Runner) report(ctx context.Context, state agentv1.TaskState, exitCode int32, msg string) error {
	return r.reportClassified(ctx, state, exitCode, msg, "")
}

// reportClassified is report() for a failure the agent diagnosed itself: reason is
// carried into the durable outcome record so the diagnosis survives a report that
// is never delivered (#930). Everything else is identical to report(), which is
// this with an empty reason.
//
// The reason must be a CLASSIFICATION the agent built — not a raw error and
// nothing derived from a credential — because the record is durable and
// end-user visible (see taskoutcome.Record.Reason). msg carries the raw
// description instead; the two are never assumed to be the same string.
func (r *Runner) reportClassified(ctx context.Context, state agentv1.TaskState, exitCode int32, msg, reason string) error {
	r.recordOutcome(state, exitCode, reason)
	if r.BeforeReport != nil {
		r.BeforeReport(state)
	}
	return r.reportRequest(ctx, &agentv1.ReportStateRequest{
		State:        state,
		ExitCode:     exitCode,
		ErrorMessage: msg,
		OccurredAt:   timestamppb.Now(),
	})
}

// recordOutcome writes the durable outcome record for a terminal report state,
// before the report is delivered (ADR 0052). Keying off the reported state — not
// the individual fail()/failWithReason() call sites — guarantees every failure
// sink is covered. Non-terminal states are a no-op.
//
// A non-empty reason (the execution_timeout diagnosis, #930) is recorded ALONGSIDE
// the exit code, never instead of it: the reason names the cause the control plane
// cannot derive from a dead pod, the exit code stays the raw signal. An empty
// reason produces today's bytes exactly.
func (r *Runner) recordOutcome(state agentv1.TaskState, exitCode int32, reason string) {
	switch state {
	case agentv1.TaskState_TASK_STATE_SUCCESS:
		r.writeOutcome(taskoutcome.Succeeded())
	case agentv1.TaskState_TASK_STATE_FAILED:
		if reason != "" {
			r.writeOutcome(taskoutcome.FailedBecauseWith(exitCode, reason))
			return
		}
		r.writeOutcome(taskoutcome.FailedWith(exitCode))
	default:
		// Non-terminal states (RUNNING, and the reschedule handled separately in
		// reportReschedule) carry no durable outcome.
	}
}

// writeOutcome persists the task's true outcome to the termination-log path. It is
// best-effort: the report remains the primary delivery channel, so an encode or
// write failure is logged, never fatal. A no-op when the path is unset (Lite).
func (r *Runner) writeOutcome(rec taskoutcome.Record) {
	if r.TerminationLogPath == "" {
		return
	}
	enc, err := rec.WithAttemptEpoch(r.attemptEpoch).Encode()
	if err != nil {
		slog.Warn("encoding task outcome record", "error", err)
		return
	}
	if err := os.WriteFile(r.TerminationLogPath, []byte(enc), 0o644); err != nil {
		slog.Warn("writing task outcome record", "path", r.TerminationLogPath, "error", err)
	}
}

// reportRetryMaxDelay caps the pause between two attempts to deliver a report.
// It equals the heartbeat interval on purpose: the heartbeat is the agent's
// other channel to the control plane, and once the server is back a report
// retry lands within about one heartbeat plus the gRPC channel's own reconnect
// backoff (the channel re-dials on its own schedule, independent of this cap) —
// the same posture the kubelet's status manager takes toward an unreachable
// apiserver (keep trying at a bounded cadence, never abandon the status).
const reportRetryMaxDelay = DefaultHeartbeatInterval

// reportBackoff returns the delay before report retry attempt n (1-based): an
// exponential ramp from 1s that is clipped at reportRetryMaxDelay and then holds
// there — 1s, 2s, 4s, 8s, then the cap for every further attempt. It caps the
// per-attempt DELAY and never the DURATION: there is no attempt budget, so the
// policy alone never ends a retry loop; only the caller's context does. Out-of-
// range attempts (n < 1) are treated as the first.
func reportBackoff(attempt int) time.Duration {
	d := time.Second
	for i := 1; i < attempt && d < reportRetryMaxDelay; i++ {
		d *= 2
	}
	if d > reportRetryMaxDelay {
		d = reportRetryMaxDelay
	}
	return d
}

// jitterDelay spreads a backoff delay over [d/2, d] ("equal jitter"), so agents
// that failed together retry at different moments instead of hitting a
// recovering control plane in one synchronized burst on every attempt. It never
// lengthens the delay, so every cap on d still holds. math/rand is fine here:
// this is backoff jitter, nothing security-relevant.
func jitterDelay(d time.Duration) time.Duration {
	half := d / 2
	if half <= 0 {
		return d
	}
	return half + time.Duration(mathrand.Int64N(int64(half)+1)) //nolint:gosec // G404: backoff jitter, not security-relevant
}

// reportRequest sends a ReportState request and translates the response's
// should_terminate signal into an error. A transient RPC failure (the api pod
// Unavailable, a deadline) is retried until it lands, with the delay between
// attempts following reportBackoff, jittered (jitterDelay). Retrying is safe: the server's ReportState
// is idempotent (a report that already applied comes back as a stale ack, not a
// double-apply). A logical rejection or a credential rejection (Unauthenticated,
// PermissionDenied) is returned immediately, and a canceled context (parent
// shutdown, SIGTERM, pod deletion, execution timeout) aborts the loop at once
// with the context error.
//
// The loop deliberately has NO attempt budget. A control-plane restart lasts
// longer than any handful of attempts, and a terminal report that gives up while
// the server is down is how a SUCCEEDED task ends up marked failed by a reaper.
// The heartbeat loop already tolerates the outage indefinitely and renews the
// bearer on the first heartbeat the recovered server answers, so the report has
// no reason to give up earlier than the heartbeat does. The true outcome is
// durably recorded (recordOutcome) before this is called, so a report that never
// lands — the pod is deleted or times out first — is recovered by the reconciler
// from that record; the retry loop only shortens the path to the same result.
//
// The RUNNING pre-flight report takes this same path, so it too outlasts an
// outage — intentionally. An agent that cannot reach the control plane does not
// start user code until it can: the pod waits in its pre-flight rather than
// running half-observed work whose RUNNING transition the control plane never
// saw (the dispatch-lost reaper would then fail a task that is actually
// executing). The cost is a pod that holds its requests for the length of the
// outage; that is bounded by the pod's ActiveDeadlineSeconds, which the
// executor floors with the attempt credential ceiling when the task declares
// no timeout of its own, so no task pod is immortal.
//
// Warm-pool workers share this path (the per-attempt Runner is built by
// WarmRunner.attemptRunner). A warm worker retrying a report stays busy only
// while the control plane is down, which is exactly the window in which no new
// work can be assigned to it anyway; and the warm-worker-lost reaper is keyed
// on the warm pod's own liveness, so a live worker retrying is never falsely
// reaped for it.
func (r *Runner) reportRequest(ctx context.Context, req *agentv1.ReportStateRequest) error {
	for attempt := 1; ; attempt++ {
		resp, err := r.Client.ReportState(ctx, req)
		if err == nil {
			// should_terminate means the row already moved on — a reaper settled it,
			// a later attempt superseded it, or (on a retry here) our own earlier
			// attempt applied but its ack was lost. We cannot tell "superseded" from
			// "we already won" locally, so we conservatively surface it as an error
			// and stop. The DB is authoritative and already terminal either way; when
			// it was our own win, the state is correct and the reconciler no-ops on
			// the now-terminal TI, so the only cost is a non-zero agent exit.
			if resp.GetShouldTerminate() {
				return errors.New("control plane requested task termination")
			}
			return nil
		}
		if !retryableReportErr(err) {
			return fmt.Errorf("reporting state %v: %w", req.GetState(), err)
		}
		delay := jitterDelay(reportBackoff(attempt))
		slog.Warn("report failed; retrying after backoff",
			"state", req.GetState(), "attempt", attempt, "delay", delay, "error", err)
		select {
		case <-ctx.Done():
			return fmt.Errorf("reporting state %v: %w", req.GetState(), ctx.Err())
		case <-r.after(delay):
		}
	}
}

// retryableReportErr reports whether a ReportState error is a transient
// infrastructure failure worth retrying. The set is the canonical
// safe-to-retry gRPC codes: everything else (InvalidArgument, PermissionDenied,
// a logical rejection) is returned to the caller unretried. A stale report is
// never seen here — the server acknowledges it (with should_terminate) rather
// than returning an error.
func retryableReportErr(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	default:
		return false
	}
}

// reschedule reads the next-poke time a reschedule-mode sensor wrote to
// ReschedulePath and reports up_for_reschedule with it, so the scheduler
// re-dispatches the task instance later without consuming retry budget (#380). A
// missing or unparseable time fails loudly rather than silently dropping the task.
// readReschedule reads and parses the next-poke time a reschedule-mode sensor wrote
// to ReschedulePath. ok is false when the path is unset, the file is absent, or it
// is unparseable — in which case the exit is treated as an ordinary result, not a
// reschedule, so exit 75 alone never hijacks a normal task outcome (#386).
func (r *Runner) readReschedule() (time.Time, bool) {
	if r.ReschedulePath == "" {
		return time.Time{}, false
	}
	data, ok, err := ReadReturnValue(r.ReschedulePath)
	if err != nil || !ok {
		return time.Time{}, false
	}
	when, perr := time.Parse(time.RFC3339Nano, strings.TrimSpace(string(data)))
	if perr != nil {
		return time.Time{}, false
	}
	return when, true
}

// reportReschedule reports up_for_reschedule with the next-poke time so the
// scheduler re-dispatches the task later, consuming no retry budget (#380).
func (r *Runner) reportReschedule(ctx context.Context, when time.Time) error {
	r.writeOutcome(taskoutcome.RescheduledAt(when))
	return r.reportRequest(ctx, &agentv1.ReportStateRequest{
		State:        agentv1.TaskState_TASK_STATE_UP_FOR_RESCHEDULE,
		OccurredAt:   timestamppb.Now(),
		RescheduleAt: timestamppb.New(when),
	})
}

// leoflowEnvPrefix marks the variables Dexaflow itself owns in an inherited
// environment. Everything under it is stripped unless explicitly kept.
const leoflowEnvPrefix = "LEOFLOW_"

// dexaflowEnvPrefix is the same set of variables under their name since the
// rename. The agent mirrors one prefix onto the other at startup (envcompat), so
// every rule here applies to both.
const dexaflowEnvPrefix = "DEXAFLOW_"

// taskVisibleLeoflowEnv is the complete set of LEOFLOW_ variables a task is
// allowed to inherit. Everything else under the prefix is removed.
//
// The agent runs inside the task's pod (ADR 0004), so its environment IS the
// task's environment unless something removes the difference — and in Lite it is
// worse: the server spawns the agent with its own environment
// (internal/executor/subprocess.go:161 appends to os.Environ()), so the agent
// inherits LEOFLOW_SECRET_KEY (the AES key encrypting connections at rest),
// LEOFLOW_AUTH_JWT_SECRET (which signs every user and agent token — read it and
// you mint an admin), LEOFLOW_DATABASE_URL with its password, and more.
//
// Hence an allowlist, scoped to the prefix. A denylist naming today's secrets
// would not have caught those, and would not catch tomorrow's: it leaks by
// default and is only as good as the last person to remember it. Scoping the
// allowlist to LEOFLOW_ keeps the fail-closed property where the secrets live,
// without having to enumerate PATH, HOME, TZ, LANG, proxies and whatever a
// user's base image depends on — those pass through untouched.
//
// Only two entries qualify, and both are set by podEnv for the task's own use:
// the staging mount path user code reads, and the task-instance id it may log.
// Everything the runtime needs beyond these is INJECTED by mergeEnv's later
// arguments rather than inherited, so it never passes through this filter —
// verified by cross-checking every LEOFLOW_ name the Python runtime reads
// against what the agent injects.
//
// LEOFLOW_PYTHON is the case worth knowing about before adding to this list: the
// Lite subprocess executor sets it on the AGENT's environment, the agent reads it
// (BuildCommand) and resolves the interpreter into argv, and the task never needs
// it. The venv's PATH, which bash tasks do need for console scripts like dbt,
// carries no LEOFLOW_ prefix and passes through untouched.
var taskVisibleLeoflowEnv = []string{
	"LEOFLOW_STAGING_DIR",
	"LEOFLOW_TASK_INSTANCE_ID",
}

// isAgentOnlyEnv reports whether name is one of the agent's own variables, under
// either prefix, that a task must not inherit.
func isAgentOnlyEnv(name string) bool {
	for _, prefix := range []string{leoflowEnvPrefix, dexaflowEnvPrefix} {
		if suffix, ok := strings.CutPrefix(name, prefix); ok {
			return !slices.Contains(taskVisibleLeoflowEnv, leoflowEnvPrefix+suffix)
		}
	}
	return false
}

// stripAgentOnly removes Dexaflow's own variables from an inherited environment
// before it is handed to user code, keeping only those a task legitimately needs.
func stripAgentOnly(base []string) []string {
	out := make([]string, 0, len(base))
	for _, kv := range base {
		name, _, _ := strings.Cut(kv, "=")
		if isAgentOnlyEnv(name) {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// mergeEnv combines the base environment with the task spec variables (sorted for
// determinism) and the fetched XCom input variables. The base is filtered first:
// it is the agent's own environment, which carries credentials the task must not
// inherit (see agentOnlyEnv).
func mergeEnv(base []string, spec map[string]string, xcom []string) []string {
	base = stripAgentOnly(base)
	out := make([]string, 0, len(base)+len(spec)+len(xcom))
	out = append(out, base...)
	keys := make([]string, 0, len(spec))
	for k := range spec {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		out = append(out, k+"="+spec[k])
	}
	return append(out, xcom...)
}

// clampExit narrows an OS exit code to the byte range a process can return.
func clampExit(code int) int32 {
	if code < 0 || code > 255 {
		return 255
	}
	return int32(code)
}

// maxLogLineBytes bounds one log line and so the partial line a logWriter
// buffers. The control plane accepts gRPC messages up to 4 MiB, and a LogLine
// over that limit used to end the whole log stream; the bound leaves room for
// the message's other fields, so every line that was deliverable before is
// still sent whole. A longer line is sent in pieces of at most this size.
// var (not const) so tests can lower it.
var maxLogLineBytes = 4<<20 - 4<<10

// logWriter splits written bytes into newline-delimited log lines and forwards
// each one to the sink, tagging it with its stream name and level. A line longer
// than maxLogLineBytes is split, so the buffer of a stream that never writes a
// newline stays bounded.
type logWriter struct {
	sink   LogSink
	stream string
	level  agentv1.LogLevel
	buf    []byte
	line   int64
	splits int64 // extra lines produced by splitting over-long lines
}

// Write buffers p and emits every complete line it contains, then any piece of
// the pending partial line that reached the bound.
func (w *logWriter) Write(p []byte) (int, error) {
	n := len(p)
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			break
		}
		w.buf = append(w.buf, p[:i]...)
		w.emitLine(w.buf)
		w.buf = w.buf[:0]
		p = p[i+1:]
	}
	w.buf = append(w.buf, p...)
	for len(w.buf) > maxLogLineBytes {
		cut := splitPoint(w.buf)
		w.emitPiece(w.buf[:cut])
		w.buf = append(w.buf[:0], w.buf[cut:]...)
	}
	return n, nil
}

// emitLine emits one complete line, in pieces when it exceeds the bound.
func (w *logWriter) emitLine(b []byte) {
	for len(b) > maxLogLineBytes {
		cut := splitPoint(b)
		w.emitPiece(b[:cut])
		b = b[cut:]
	}
	w.emit(b)
}

// emitPiece emits the head of an over-long line and counts the split. The first
// split of a stream is logged; the total is logged on flush.
func (w *logWriter) emitPiece(b []byte) {
	if w.splits == 0 {
		slog.Warn("task log line exceeds the line bound; sending it in pieces",
			"stream", w.stream, "max_bytes", maxLogLineBytes)
	}
	w.splits++
	w.emit(b)
}

// splitPoint returns where to cut a line longer than the bound: at the bound,
// moved back to the start of a rune so both pieces stay valid UTF-8 (LogLine's
// message is a proto string). Bytes that are not UTF-8 are cut at the bound.
func splitPoint(b []byte) int {
	for cut := maxLogLineBytes; cut > maxLogLineBytes-utf8.UTFMax && cut > 0; cut-- {
		if utf8.RuneStart(b[cut]) {
			return cut
		}
	}
	return maxLogLineBytes
}

// flush emits any buffered line that lacked a trailing newline.
func (w *logWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(w.buf)
		w.buf = nil
	}
	if w.splits > 0 {
		slog.Warn("task log lines were split at the line bound",
			"stream", w.stream, "extra_lines", w.splits, "max_bytes", maxLogLineBytes)
	}
}

// emitTaskStarted writes a synthetic start-of-task event to the log sink, so the
// UI's Logs panel always shows at least one line — even for a task that calls no
// print(). Best-effort; a sink error is logged but not propagated (the task
// itself is what matters).
func emitTaskStarted(sink LogSink) {
	if err := sink.Send(&agentv1.LogLine{
		Time:    timestamppb.Now(),
		Level:   agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: "▸ task started",
		Stream:  "agent",
	}); err != nil {
		slog.Warn("emitting task-started log", "error", err)
	}
}

// emitTaskBoot writes a lifecycle line summarizing what's about to run: the
// interpreter argv and the names of injected AIRFLOW_CONN_*, AIRFLOW_VAR_*, and
// LEOFLOW_XCOM_* env vars. Without this, a task with no print() shows only the
// 2-line framing — leaving the user wondering whether their conns/vars were
// injected or their kwargs resolved. Best-effort: a sink error is logged.
func emitTaskBoot(sink LogSink, argv, env []string) {
	if len(argv) == 0 {
		return
	}
	var conns, vars, xcoms []string
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "AIRFLOW_CONN_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				conns = append(conns, kv[:eq])
			}
		case strings.HasPrefix(kv, "AIRFLOW_VAR_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				vars = append(vars, kv[:eq])
			}
		case strings.HasPrefix(kv, "LEOFLOW_XCOM_"):
			if eq := strings.IndexByte(kv, '='); eq > 0 {
				xcoms = append(xcoms, kv[:eq])
			}
		}
	}
	sort.Strings(conns)
	sort.Strings(vars)
	sort.Strings(xcoms)
	msg := fmt.Sprintf("running: %s", strings.Join(argv, " "))
	if err := sink.Send(&agentv1.LogLine{
		Time: timestamppb.Now(), Level: agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: msg, Stream: "agent",
	}); err != nil {
		slog.Warn("emitting task-boot log", "error", err)
	}
	if len(conns)+len(vars)+len(xcoms) == 0 {
		return
	}
	parts := make([]string, 0, 3)
	if len(conns) > 0 {
		parts = append(parts, fmt.Sprintf("conns: %s", strings.Join(conns, ", ")))
	}
	if len(vars) > 0 {
		parts = append(parts, fmt.Sprintf("vars: %s", strings.Join(vars, ", ")))
	}
	if len(xcoms) > 0 {
		parts = append(parts, fmt.Sprintf("xcom inputs: %s", strings.Join(xcoms, ", ")))
	}
	if err := sink.Send(&agentv1.LogLine{
		Time: timestamppb.Now(), Level: agentv1.LogLevel_LOG_LEVEL_INFO,
		Message: "injected " + strings.Join(parts, " | "), Stream: "agent",
	}); err != nil {
		slog.Warn("emitting task-boot env log", "error", err)
	}
}

// emitTaskEnded writes a synthetic end-of-task event with the run duration and
// either a success marker or the exit code + cause. Pairs with emitTaskStarted to
// guarantee the Logs panel is never empty for a completed task (#119).
func emitTaskEnded(sink LogSink, exitCode int, cause error, duration time.Duration) {
	d := duration.Round(time.Millisecond)
	var msg string
	level := agentv1.LogLevel_LOG_LEVEL_INFO
	if cause == nil && exitCode == 0 {
		msg = fmt.Sprintf("✓ task succeeded in %s", d)
	} else {
		level = agentv1.LogLevel_LOG_LEVEL_ERROR
		if cause != nil {
			msg = fmt.Sprintf("✗ task failed (exit %d) in %s: %s", exitCode, d, cause.Error())
		} else {
			msg = fmt.Sprintf("✗ task failed (exit %d) in %s", exitCode, d)
		}
	}
	if err := sink.Send(&agentv1.LogLine{
		Time:    timestamppb.Now(),
		Level:   level,
		Message: msg,
		Stream:  "agent",
	}); err != nil {
		slog.Warn("emitting task-ended log", "error", err)
	}
}

func (w *logWriter) emit(b []byte) {
	w.line++
	if err := w.sink.Send(&agentv1.LogLine{
		Time:       timestamppb.Now(),
		Level:      w.level,
		Message:    string(b),
		Stream:     w.stream,
		LineNumber: w.line,
	}); err != nil {
		slog.Warn("streaming log line", "stream", w.stream, "error", err)
	}
}
