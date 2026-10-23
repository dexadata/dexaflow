package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func (r *Runner) buildEnv(ctx context.Context, spec *agentv1.TaskSpec) ([]string, error) {
	// The batched prefetch is not retried (a failure falls back to the
	// per-value reads, which are), but it runs inside the start phase's budget.
	prefetchCtx, cancel := r.phaseContext(ctx, &r.startPhase)
	inner := r.prefetchXCom(prefetchCtx, spec)
	cancel()
	fetcher := retryingFetcher{r: r, phase: &r.startPhase, inner: inner}
	var xcom []string
	for param, upstreams := range spec.GetXcomInputMapping() {
		taskIDs := upstreams.GetTaskIds()
		switch len(taskIDs) {
		case 0:
			// Empty upstream list, parser invariant prevents this, but guard anyway.
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
	// Force-unbuffered Python and explicit UTF-8 for every spawned subprocess,
	// including any python re-execed by the user's task. The `-u` flag on
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
		// not the shared global default, so concurrent tasks and other users never
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
		// between attempts) so anything the child writes to $TMPDIR, a token cache,
		// a dbt profile, ~/.aws-style credentials, is gone before the next attempt
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
// ti.xcom_pull, a python @task gets its inputs via the param-keyed xcom_input_mapping,
// so the map is built for airflow_operator tasks only, avoiding wasted fetches. An
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
// the context is blank in every executor, ti.task_id="", run_id="", try_number
// defaulting to 1, and run_operator falling back to the operator class name as the
// task_id, a silent-wrong gap for operators that read them. The server carries
// these on the TaskSpec. ts/ds are derived from the single logical_date (ts is the
// RFC3339 value, ds its UTC date). A field the server leaves empty is left unset
// rather than fabricated.
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
	// The run conf alone and the run type, for the runtime's dag_run (#1843).
	// Unset from a control plane that predates them, so the runtime can tell an
	// unknown conf from an empty one.
	if v := spec.GetRunConfJson(); v != "" {
		env = append(env, "LEOFLOW_RUN_CONF="+v)
	}
	if v := spec.GetRunType(); v != "" {
		env = append(env, "LEOFLOW_RUN_TYPE="+v)
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
// miss) fails the task. If a vault RPC returns PermissionDenied, the
// liveness-enforce denial (ADR 0055), external resolution is skipped entirely: a
// non-live task instance resolves nothing, from the vault or externally. Other
// vault errors stay best-effort (a task using no secrets still runs).
func (r *Runner) secretsEnv(ctx context.Context, spec *agentv1.TaskSpec) ([]string, error) {
	vars := map[string]string{}
	conns := map[string]string{}
	// vaultDenied records a liveness-enforce PermissionDenied on either vault RPC;
	// it gates the external branch (B2) so a non-live TI resolves nothing.
	vaultDenied := false
	var varsResp *agentv1.GetVariablesResponse
	if err := r.withTransientRetry(ctx, &r.startPhase, "GetVariables", func(ctx context.Context) error {
		var cerr error
		varsResp, cerr = r.Client.GetVariables(ctx, &agentv1.GetVariablesRequest{})
		return cerr
	}); err != nil {
		if status.Code(err) == codes.PermissionDenied {
			vaultDenied = true
		}
		slog.Warn("fetching variables; Variable.get may be unavailable", "error", err)
	} else {
		for k, v := range varsResp.GetVariables() {
			vars[k] = v
		}
	}
	var connsResp *agentv1.GetConnectionsResponse
	if err := r.withTransientRetry(ctx, &r.startPhase, "GetConnections", func(ctx context.Context) error {
		var cerr error
		connsResp, cerr = r.Client.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
		return cerr
	}); err != nil {
		if status.Code(err) == codes.PermissionDenied {
			vaultDenied = true
		}
		slog.Warn("fetching connections; get_connection may be unavailable", "error", err)
	} else {
		for id, uri := range connsResp.GetConnectionUris() {
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

// coveredRefs is the set of declared names the operator's backend covers, the
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
// this as `list[T]`, len(upstreams) elements, never fewer.
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
// task's environment unless something removes the difference, and in Lite it is
// worse: the server spawns the agent with its own environment
// (internal/executor/subprocess.go:161 appends to os.Environ()), so the agent
// inherits LEOFLOW_SECRET_KEY (the AES key encrypting connections at rest),
// LEOFLOW_AUTH_JWT_SECRET (which signs every user and agent token, read it and
// you mint an admin), LEOFLOW_DATABASE_URL with its password, and more.
//
// Hence an allowlist, scoped to the prefix. A denylist naming today's secrets
// would not have caught those, and would not catch tomorrow's: it leaks by
// default and is only as good as the last person to remember it. Scoping the
// allowlist to LEOFLOW_ keeps the fail-closed property where the secrets live,
// without having to enumerate PATH, HOME, TZ, LANG, proxies and whatever a
// user's base image depends on, those pass through untouched.
//
// Only two entries qualify, and both are set by podEnv for the task's own use:
// the staging mount path user code reads, and the task-instance id it may log.
// Everything the runtime needs beyond these is INJECTED by mergeEnv's later
// arguments rather than inherited, so it never passes through this filter,
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
