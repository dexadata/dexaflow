package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	mathrand "math/rand/v2"
	"os"
	"strings"
	"time"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

func (r *Runner) report(ctx context.Context, state agentv1.TaskState, exitCode int32, msg string) error {
	return r.reportClassified(ctx, state, exitCode, msg, "")
}

// reportClassified is report() for a failure the agent diagnosed itself: reason is
// carried into the durable outcome record so the diagnosis survives a report that
// is never delivered (#930). Everything else is identical to report(), which is
// this with an empty reason.
//
// The reason must be a CLASSIFICATION the agent built, not a raw error and
// nothing derived from a credential, because the record is durable and
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
// before the report is delivered (ADR 0052). Keying off the reported state, not
// the individual fail()/failWithReason() call sites, guarantees every failure
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
// other channel to the control plane. The gRPC channel redials on its own
// schedule (a fail-fast RPC on a channel in TRANSIENT_FAILURE does not start a
// connect), so Dial caps that schedule at this same value (reconnectBackoff):
// once the server is back the channel is up within about one heartbeat and the
// next report retry lands by the following one. It is the same posture the
// kubelet's status manager takes toward an unreachable apiserver (keep trying
// at a bounded cadence, never abandon the status).
const reportRetryMaxDelay = DefaultHeartbeatInterval

// reportBackoff returns the delay before report retry attempt n (1-based): an
// exponential ramp from 1s that is clipped at reportRetryMaxDelay and then holds
// there, 1s, 2s, 4s, 8s, then the cap for every further attempt. It caps the
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
// double-apply). A terminal report also retries the transport-class Unknown and
// Internal errors an outage produces behind a load balancer or a mesh
// (retryableTerminalReportErr) without a budget; the RUNNING report does not.
// Any other Internal is retried only within transientRetryBudget. A logical
// rejection or a credential rejection (Unauthenticated,
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
// lands, the pod is deleted or times out first, is recovered by the reconciler
// from that record; the retry loop only shortens the path to the same result.
//
// The RUNNING pre-flight report takes this same path, so it too outlasts an
// outage, intentionally. An agent that cannot reach the control plane does not
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
	// internalSpent is the backoff spent on Internal answers, which (unlike the
	// codes retryable names) are retried only within transientRetryBudget:
	// Internal is what the control plane answered for a store error before it
	// learned to say Unavailable, and still answers for a failure that repeats
	// on every attempt.
	var internalSpent time.Duration
	retryable := retryableReportErr
	if req.GetState() != agentv1.TaskState_TASK_STATE_RUNNING {
		retryable = retryableTerminalReportErr
	}
	for attempt := 1; ; attempt++ {
		resp, err := r.Client.ReportState(ctx, req)
		if err == nil {
			r.reportLanded = true
			// should_terminate means the row already moved on, a reaper settled it,
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
		delay := jitterDelay(reportBackoff(attempt))
		if !retryable(err) {
			if status.Code(err) != codes.Internal || internalSpent+delay > transientRetryBudget {
				return fmt.Errorf("reporting state %v: %w", req.GetState(), err)
			}
			internalSpent += delay
		}
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
// never seen here, the server acknowledges it (with should_terminate) rather
// than returning an error.
func retryableReportErr(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted, codes.Aborted:
		return true
	default:
		return false
	}
}

// transientRetryBudget bounds the wall time the agent spends retrying a
// transient control-plane error on the calls that read its inputs and deliver
// its outputs (GetTaskSpec, GetVariables, GetConnections, FetchXCom, PushXCom),
// and the backoff it spends on an Internal answer to ReportState (R-01, #1594).
// Two minutes covers a Postgres failover, a Redis restart and a rolling restart
// of a single control-plane replica, each of which used to fail every task that
// started or finished inside it, charged to the user's retries. The input and
// output calls share it per phase (retryPhase): the time inside each try counts,
// not only the backoff, so the bound holds against a control plane that hangs
// every call until its per-call deadline. A control plane that is still failing
// when it runs out ends the attempt as before, with the delays between tries
// following reportBackoff, jittered.
const transientRetryBudget = 2 * time.Minute

// retryPhase is the transient-retry budget one phase of an attempt shares
// across all of its calls. Its deadline is set by the first try of the
// phase's first call, transientRetryBudget after it; the zero value is a phase
// that has not started.
type retryPhase struct {
	deadline time.Time
}

// phaseContext starts phase if it has not started and returns ctx bounded by
// its deadline, so no try of the phase runs past it.
func (r *Runner) phaseContext(ctx context.Context, phase *retryPhase) (context.Context, context.CancelFunc) {
	if phase.deadline.IsZero() {
		phase.deadline = r.clock().Add(transientRetryBudget)
	}
	return context.WithTimeout(ctx, phase.deadline.Sub(r.clock()))
}

// retryableTransientErr reports whether an error from an input or output call
// is worth retrying: the control plane was unreachable or restarting
// (Unavailable), a store behind it failed (Internal, which an older control
// plane answers for a database or Redis outage), the call timed out
// (DeadlineExceeded) or lost a conflict (Aborted). ResourceExhausted is left
// out on purpose: on these calls it is a message over the size limit, which
// fails the same way every time.
func retryableTransientErr(err error) bool {
	switch status.Code(err) {
	case codes.Unavailable, codes.Internal, codes.DeadlineExceeded, codes.Aborted:
		return true
	default:
		return false
	}
}

// controlPlaneOutage reports whether err, which ended an attempt before the
// task's code ran, means the control plane did not answer: a transient code
// (retryableTransientErr) that outlasted the start budget. An external secret
// backend's failure is excluded even when its own gRPC code is transient: that
// backend belongs to the task's configuration, not to the control plane, and
// keeps its own classification (classifyEnvFailure). A warm attempt is
// excluded too (warmAttempt).
func (r *Runner) controlPlaneOutage(err error) bool {
	return !r.warmAttempt && retryableTransientErr(err) && !errors.Is(err, errSecretResolution)
}

// startFailed ends an attempt that stopped before the task's code ran and
// returns err. When the cause is a control-plane outage (controlPlaneOutage),
// the substrate could not run the task (ADR 0051, Unexecutable; R-01, #1594):
// it writes the unexecutable outcome record, which the reconciler settles on
// the infra rail without spending a task retry, and reports nothing, since a
// FAILED report would settle the attempt as the task's failure first. The
// record's reason is a fixed classification, never err's text (#930). On
// Lite, which reads no record, the unreported attempt is the dispatch-lost
// reaper's, which settles it on the same rail. Any other cause is returned
// unchanged, as before.
func (r *Runner) startFailed(err error) error {
	if r.controlPlaneOutage(err) {
		slog.Warn("control plane did not answer before the task started; recording the attempt as unexecutable", "error", err)
		r.writeOutcome(taskoutcome.UnexecutableBecause(reasonControlPlaneUnavailable))
	}
	return err
}

// withTransientRetry runs call until it succeeds, fails with an error
// retryableTransientErr rejects, or phase runs out of budget, and returns
// call's last error. Every try gets a context bounded by the phase deadline,
// so a try that hangs is cut there, and a backoff that would end past the
// deadline is not slept: the call gives up instead. A call that starts after
// the phase ran out fails at once with DeadlineExceeded. A canceled context
// stops it at once. Every call it wraps is safe to repeat: the reads are
// reads, and PushXCom stores the same value under the same key.
func (r *Runner) withTransientRetry(ctx context.Context, phase *retryPhase, op string, call func(context.Context) error) error {
	for attempt := 1; ; attempt++ {
		callCtx, cancel := r.phaseContext(ctx, phase)
		err := call(callCtx)
		cancel()
		if err == nil || !retryableTransientErr(err) {
			return err
		}
		delay := jitterDelay(reportBackoff(attempt))
		if r.clock().Add(delay).After(phase.deadline) {
			slog.Warn("control plane call still failing; giving up", "call", op, "attempts", attempt, "error", err)
			return err
		}
		slog.Warn("control plane call failed; retrying after backoff",
			"call", op, "attempt", attempt, "delay", delay, "error", err)
		select {
		case <-ctx.Done():
			return err
		case <-r.after(delay):
		}
	}
}

// retryingFetcher is an xcomFetcher that retries a transient FetchXCom error.
// A NotFound is an answer, not an error, and comes back on the first call.
type retryingFetcher struct {
	r     *Runner
	phase *retryPhase
	inner xcomFetcher
}

// FetchXCom fetches through inner with the transient-error retry.
func (f retryingFetcher) FetchXCom(ctx context.Context, in *agentv1.FetchXComRequest, opts ...grpc.CallOption) (*agentv1.FetchXComResponse, error) {
	var resp *agentv1.FetchXComResponse
	err := f.r.withTransientRetry(ctx, f.phase, "FetchXCom", func(ctx context.Context) error {
		var cerr error
		resp, cerr = f.inner.FetchXCom(ctx, in, opts...)
		return cerr
	})
	return resp, err
}

// retryableTerminalReportErr is retryableReportErr for a terminal report (the
// attempt's last word: SUCCESS, FAILED, UP_FOR_RESCHEDULE). It also retries the
// transport-class subset of Unknown and Internal (transportClassErr), which is
// how a control-plane outage reaches the agent behind an L7 load balancer or a
// service mesh: an HTTP 500 from the proxy, a stream reset or cut mid-call
// (#908). Giving up there sends the task to the reconciler's slower recovery
// from the durable outcome record. The retry stays bounded by the context, and
// the task pod's ActiveDeadlineSeconds floor bounds that, so even a
// misclassified error cannot keep a pod alive forever.
func retryableTerminalReportErr(err error) bool {
	return retryableReportErr(err) || transportClassErr(err)
}

// transportMarkers are the lowercase fragments grpc-go and the network stack
// put in a status message when the transport, not the handler, failed. The
// control plane's own Unknown and Internal statuses carry only the operation
// name (agentrpc redacts the cause), so they never match.
var transportMarkers = []string{
	"rst_stream", // stream terminated by RST_STREAM (proxy reset)
	"server closed the stream without sending trailers",
	"unexpected eof",
	"connection reset",
	"broken pipe",
	"transport is closing",
	"unexpected http status code received from server: 5", // LB/proxy 500 (grpc-go maps it to Unknown)
}

// transportClassErr reports whether an Unknown or Internal error is the
// transport-class kind: the stream or connection broke, or a proxy answered
// in the backend's place. Any other code, and an Unknown or Internal the
// handler itself returned, is not.
func transportClassErr(err error) bool {
	st, ok := status.FromError(err)
	if !ok || (st.Code() != codes.Unknown && st.Code() != codes.Internal) {
		return false
	}
	msg := strings.ToLower(st.Message())
	if msg == "eof" || strings.HasSuffix(msg, ": eof") {
		return true
	}
	for _, m := range transportMarkers {
		if strings.Contains(msg, m) {
			return true
		}
	}
	return false
}
