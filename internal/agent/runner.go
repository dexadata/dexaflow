package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"

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
	// afterFunc returns a channel that fires after the given delay; it exists so
	// tests can make the report-retry backoff instant. Nil uses time.After.
	afterFunc func(time.Duration) <-chan time.Time
	// now is the clock the transient-retry budget is measured on; tests inject
	// one that a hung call and a backoff advance. Nil uses time.Now.
	now func() time.Time
	// startPhase and outputPhase are the transient-retry budgets of the
	// attempt: every call before the task runs (spec, variables, connections,
	// upstream XComs) shares the first, and every output push shares the
	// second. runOneAttempt resets both.
	startPhase  retryPhase
	outputPhase retryPhase
	// warmAttempt marks an attempt a warm worker serves (attemptRunner). Its
	// pod outlives the attempt, so no reconciler reads a per-attempt outcome
	// record from it: a start the control plane never answered is reported
	// FAILED there, the only settle it has, instead of recorded as
	// unexecutable (startFailed).
	warmAttempt bool
	// attemptEpoch is the execution of the try this attempt is, from its task
	// spec, stamped on the outcome record (ADR 0052 amendment). It is reset for
	// every attempt a warm worker runs; 0 until the spec is read.
	attemptEpoch int64
	// reportLanded is set once the control plane has answered a ReportState for
	// this attempt (applied, or acknowledged as stale). A warm worker reads it to
	// settle an acked attempt that ended before any report reached the control
	// plane (#1603).
	reportLanded bool
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

// clock reads the injected clock when set (tests) and time.Now otherwise.
func (r *Runner) clock() time.Time {
	if r.now != nil {
		return r.now()
	}
	return time.Now()
}

// Run executes the task lifecycle and returns an error if the task failed. In
// single-shot mode the agent registers once and serves exactly one attempt, so
// Run is register followed by runOneAttempt. The warm worker (warm.go) reuses
// runOneAttempt directly, registering separately and driving many attempts.
func (r *Runner) Run(ctx context.Context) error {
	if err := r.register(ctx); err != nil {
		return r.startFailed(err)
	}
	return r.runOneAttempt(ctx)
}

// runOneAttempt fetches the task spec, builds the command and its environment, and
// runs the user process to its terminal ReportState. It is the whole per-attempt
// body: a single-shot agent calls it once (via Run); a warm worker calls it once
// per WorkAssignment, each time in a fresh forked child with a freshly-built env.
// It does NOT register, registration is a per-worker concern the caller owns.
func (r *Runner) runOneAttempt(ctx context.Context) error {
	r.attemptEpoch = 0
	r.startPhase, r.outputPhase = retryPhase{}, retryPhase{}
	var spec *agentv1.TaskSpec
	err := r.withTransientRetry(ctx, &r.startPhase, "GetTaskSpec", func(ctx context.Context) error {
		var cerr error
		spec, cerr = r.Client.GetTaskSpec(ctx, &agentv1.GetTaskSpecRequest{})
		return cerr
	})
	if err != nil {
		return r.startFailed(fmt.Errorf("fetching task spec: %w", err))
	}
	r.attemptEpoch = spec.GetAttemptEpoch()
	argv, err := BuildCommand(spec.GetOperator(), spec.GetEntrypoint(), spec.GetOperatorClass())
	if err != nil {
		return err
	}
	env, err := r.buildEnv(ctx, spec)
	if err != nil {
		// A buildEnv failure, notably a hard external-secret resolver error (ADR
		// 0060 B6), means the task cannot run. Report FAILED with the reason so the
		// TI settles as failed with a visible cause, instead of being stranded until
		// a reaper marks it agent_lost.
		//
		// The message and the classification are deliberately different values. The
		// message is the raw error, which belongs in the report and the logs; the
		// DURABLE record only ever carries what classifyEnvFailure recognizes, and
		// "" for everything else. Handing err.Error() to both would put a wrapped
		// gRPC error, control-plane endpoint, TLS handshake text, on a durable
		// field readable by anyone with pod read access in the task namespace
		// (#930).
		//
		// An upstream read the control plane never answered is not the task's
		// failure, though: it is settled as unexecutable, and not reported.
		if r.controlPlaneOutage(err) {
			return r.startFailed(err)
		}
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

func (r *Runner) execute(ctx context.Context, argv, env []string, timeout time.Duration) error {
	if err := r.report(ctx, agentv1.TaskState_TASK_STATE_RUNNING, 0, ""); err != nil {
		return r.startFailed(err)
	}
	stdout := &logWriter{sink: r.Sink, stream: "stdout", level: agentv1.LogLevel_LOG_LEVEL_INFO}
	stderr := &logWriter{sink: r.Sink, stream: "stderr", level: agentv1.LogLevel_LOG_LEVEL_ERROR}

	runCtx, cancel := context.WithCancel(ctx)
	// Join the heartbeat goroutine before this attempt returns. Its last
	// Token.Set (adopting a renewed bearer) must complete before the caller
	// swaps to the next attempt's token, in warm mode r.Token is the SHARED
	// AttemptTokens and attempts are sequential, so a lingering heartbeat
	// finishing a Set after attempt N+1 adopted its token would make N+1 run
	// under N's superseded credential. cancel() signals the loop to stop, THEN
	// Wait() blocks for its in-flight cycle to finish; the order matters (a
	// single deferred func, not two defers). Single-shot is unaffected, the
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

	// Frame the run so a task with no print() still has visible logs in the UI,
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
	// leaves its next-poke time in ReschedulePath. The FILE is the real signal, a
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
	// "task failed (exit 0)", a string that reads as a success and names no cause.
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

// failWithReason is fail() but with a pre-built error message, used by the
// execution_timeout path so the operator sees "timeout" rather than the
// generic "task exited non-zero" or the raw context.DeadlineExceeded.
//
// msg and reason are SEPARATE parameters on purpose (#930). msg is the reported,
// logged description and may be raw error text. reason is what travels on the
// DURABLE outcome record, so it must be a classification from the closed set in
// failreason.go, or "", which records no reason at all. A call site that has a
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
	resp, err := r.pushXCom(ctx, &agentv1.PushXComRequest{
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
// no-op, links are UI sugar and must not fail an otherwise-successful task.
func (r *Runner) pushExtraLinks(ctx context.Context) error {
	if r.LinksPath == "" {
		return nil
	}
	value, ok, err := ReadReturnValue(r.LinksPath)
	if err != nil || !ok {
		return err
	}
	resp, err := r.pushXCom(ctx, &agentv1.PushXComRequest{
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
// plane is a no-op, these are observability, not task success.
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
		resp, perr := r.pushXCom(ctx, &agentv1.PushXComRequest{
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
// mutually exclusive on the wire, the server sends a renewed token only on the
// live branch and should_terminate only on the superseded branch. The token is
// never logged.
func (r *Runner) applyHeartbeatResponse(resp *agentv1.HeartbeatResponse) (terminate bool) {
	if r.Token != nil {
		r.Token.Set(resp.GetRenewedToken()) // no-op on empty (keep current bearer)
	}
	return resp.GetShouldTerminate()
}

// pushXCom is PushXCom with the transient-error retry.
func (r *Runner) pushXCom(ctx context.Context, req *agentv1.PushXComRequest) (*agentv1.PushXComResponse, error) {
	var resp *agentv1.PushXComResponse
	err := r.withTransientRetry(ctx, &r.outputPhase, "PushXCom", func(ctx context.Context) error {
		var cerr error
		resp, cerr = r.Client.PushXCom(ctx, req)
		return cerr
	})
	return resp, err
}

// reschedule reads the next-poke time a reschedule-mode sensor wrote to
// ReschedulePath and reports up_for_reschedule with it, so the scheduler
// re-dispatches the task instance later without consuming retry budget (#380). A
// missing or unparseable time fails loudly rather than silently dropping the task.
// readReschedule reads and parses the next-poke time a reschedule-mode sensor wrote
// to ReschedulePath. ok is false when the path is unset, the file is absent, or it
// is unparseable, in which case the exit is treated as an ordinary result, not a
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

// clampExit narrows an OS exit code to the byte range a process can return.
func clampExit(code int) int32 {
	if code < 0 || code > 255 {
		return 255
	}
	return int32(code)
}
