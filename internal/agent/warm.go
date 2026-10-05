package agent

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"time"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// WarmRunner is the client side of the warm-worker transport (ADR 0058 D4): a
// long-lived process that registers once, opens the AwaitAssignment bidi stream,
// and serves MANY task attempts — one at a time — each in a fresh forked child.
//
// Two identities are kept deliberately separate:
//
//   - StreamClient carries the worker's BOOTSTRAP identity. Register and the
//     AwaitAssignment control stream run on it and never adopt an attempt token,
//     so the pod's membership in the pool is stable for the worker's whole life.
//   - WorkClient carries each attempt's PER-ATTEMPT identity. Its per-RPC
//     credential reads AttemptTokens, which the loop swaps to the assignment's
//     attempt_token before running. Because attempts are strictly sequential, no
//     two attempts' RPCs are ever in flight at once, so the swap is race-free; and
//     because the swap only touches AttemptTokens (a different TokenSource / dial
//     from the stream), it never disturbs the already-open bootstrap stream, whose
//     authorization header was sent once at stream open.
//
// In production StreamClient and WorkClient are two dials of the same control
// plane (see cmd/dexaflow-agent), one bound to the bootstrap TokenSource and one to
// AttemptTokens. They may be the same client only in tests that don't exercise the
// credential.
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
	//   - MaxReconnects: consecutive not-leader reconnects before giving up.
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

	// streamCloser holds the connection the most recent Redial opened, so it can be
	// closed on the following reconnect (or on exit) rather than leaked.
	streamCloser io.Closer
}

// Reconnect defaults (warm-pool Hole B) used when the corresponding WarmRunner
// field is zero. The base/cap are deliberately short: a Service rotation resolves
// in a few dials, and a persistent rejection should surface quickly.
const (
	reconnectBackoffBaseDefault = 500 * time.Millisecond
	reconnectBackoffMaxDefault  = 5 * time.Second
	maxReconnectsDefault        = 10
)

// Run serves the warm-worker lifecycle, reconnecting toward the scheduler leader
// on a not-leader rejection (warm-pool Hole B). Each serve() registers, opens the
// assignment stream, and serves assignments until the stream ends (server close /
// drain), ctx is canceled, or a self-lifecycle bound trips — all clean exits
// (nil). A FailedPrecondition from serve() is the not-leader rejection (the
// leader-gate, or warm pools not serving on this endpoint): Run re-dials and
// retries with jittered exponential backoff so one pod finds the leader across the
// Service's rotation, bounded by MaxReconnects so a misconfigured deployment
// eventually exits and lets the reconciler replace it. Any other error (a real
// registration / stream transport failure, or a fail-closed scratch failure) is
// returned as before. A failed TASK is a normal outcome and never ends serve().
func (w *WarmRunner) Run(ctx context.Context, dagVersionID string) error {
	base := w.ReconnectBackoff
	if base <= 0 {
		base = reconnectBackoffBaseDefault
	}
	maxBackoff := w.ReconnectMaxBackoff
	if maxBackoff <= 0 {
		maxBackoff = reconnectBackoffMaxDefault
	}
	maxReconnects := w.MaxReconnects
	if maxReconnects <= 0 {
		maxReconnects = maxReconnectsDefault
	}

	defer w.closeRedialConn()

	// Before any attempt runs in this container: non dumpable, so an attempt
	// cannot read the agent's tokens out of /proc, and child subreaper, so an
	// attempt's orphans stay findable by the post-attempt sweep (X3.3, X3.4).
	// Fail-closed: a worker that cannot isolate its attempts must not serve any.
	if err := hardenWarmProcess(); err != nil {
		return fmt.Errorf("hardening the warm worker process: %w", err)
	}

	reconnects := 0
	for {
		err := w.serve(ctx, dagVersionID)
		if err == nil {
			return nil
		}
		// Only a not-leader rejection triggers reconnect; every other error (real
		// transport failure, fail-closed scratch) surfaces as before.
		if status.Code(err) != codes.FailedPrecondition {
			return err
		}
		reconnects++
		if reconnects > maxReconnects {
			return fmt.Errorf("giving up after %d consecutive not-leader reconnects: %w", maxReconnects, err)
		}
		slog.Warn("warm worker not served by this endpoint (not the scheduler leader); reconnecting toward the leader",
			"reconnect", reconnects, "max_reconnects", maxReconnects, "error", err)
		if serr := w.backoffSleep(ctx, base, maxBackoff, reconnects); serr != nil {
			// ctx canceled during backoff (SIGTERM / parent shutdown) => clean exit.
			return nil
		}
		if rerr := w.redialStream(); rerr != nil {
			return fmt.Errorf("redialing control plane after not-leader rejection: %w", rerr)
		}
	}
}

// serve runs one connection's worth of the warm lifecycle: register the worker,
// open the assignment stream, and serve assignments until the stream ends (server
// close / drain, or the control plane's own shutdown while this worker sits idle)
// or ctx is canceled — returning nil in all those cases. It
// returns a non-nil error only on a registration failure, a stream transport
// failure, or a failure to guarantee a clean scratch (isolation is fail-closed). A
// FailedPrecondition-coded error is the not-leader rejection Run reconnects on. A
// failed TASK is a normal outcome and never ends the loop.
func (w *WarmRunner) serve(ctx context.Context, dagVersionID string) error {
	stream, err := w.connect(ctx, dagVersionID)
	if err != nil {
		return err
	}

	// D9/D10 accounting: bound the worker by attempts served and wall-clock age.
	// Both are checked only BETWEEN attempts (after SlotFree), so a recycle is always
	// graceful — it never interrupts an in-flight attempt.
	workerStart := time.Now()
	attemptsServed := 0

	for {
		assignment, rerr := w.recvAssignment(ctx, stream)
		if rerr != nil {
			// D6 idle-recycle: no assignment arrived within IdleTTL, so recycle this
			// worker for freshness / scale-down. A closed / drained stream (EOF), a
			// parent shutdown (SIGTERM, surfaced on the stream as codes.Canceled), and
			// the control plane ending the stream at ITS shutdown (Unavailable) also
			// mean "stop serving" — all exit cleanly. Anything else is a real transport
			// failure worth surfacing.
			//
			// Unavailable is accepted HERE ONLY. This branch is the idle worker: it has
			// nothing in flight, so a control plane that went away is a clean recycle
			// and the reconciler brings the pod back against whichever replica
			// survives. Treated as an error it cost one Failed pod and one ERROR line
			// per warm worker per control-plane restart, which is the same signal-
			// burning the shutdown fix set out to remove, relocated from the server's
			// warnings to the workers' exit codes. It is deliberately NOT accepted in
			// serveAssignment's branch below, nor at Run's top level: an attempt dying
			// mid-flight, and a real outage, must both still surface as failures. The
			// consequence an operator sees is a busy worker that completes its attempt,
			// fails its slot-free send on the dead stream, and exits non-zero — one
			// Failed warm pod per busy worker per control-plane restart, which is what
			// the warm-worker-lost reaper keys on (#918, landed in #927).
			if errors.Is(rerr, errIdleRecycle) {
				slog.Info("warm worker idle-recycle: no assignment within idle TTL", "idle_ttl", w.IdleTTL)
				return nil
			}
			if errors.Is(rerr, io.EOF) || status.Code(rerr) == codes.Canceled {
				return nil
			}
			if status.Code(rerr) == codes.Unavailable {
				slog.Info("warm worker recycling: the control plane ended the assignment stream while this worker was idle", "error", rerr)
				return nil
			}
			return fmt.Errorf("receiving assignment: %w", rerr)
		}
		if serr := w.serveAssignment(ctx, stream, assignment); serr != nil {
			// A stream / isolation failure is loop-fatal, EXCEPT a canceled stream
			// (parent shutdown mid-attempt) — that is a clean shutdown, not an error.
			if status.Code(serr) == codes.Canceled {
				return nil
			}
			return serr
		}

		// D9/D10 graceful drain: the attempt above is DONE and its SlotFree is sent, so
		// stopping here awaits no more work and lets the process exit — the reconciler
		// then replaces the pod (RestartPolicy:Never). Checked here, never mid-attempt.
		attemptsServed++
		if w.MaxAttempts > 0 && attemptsServed >= w.MaxAttempts {
			slog.Info("warm worker draining: max attempts reached",
				"attempts_served", attemptsServed, "max_attempts", w.MaxAttempts)
			return nil
		}
		if w.MaxLifetime > 0 && time.Since(workerStart) >= w.MaxLifetime {
			slog.Info("warm worker draining: max lifetime reached",
				"age", time.Since(workerStart), "max_lifetime", w.MaxLifetime)
			return nil
		}
	}
}

// connect establishes one connection's control channel: it exchanges the bootstrap
// token for a worker JWT (under the exchange transport), registers the worker, opens
// the AwaitAssignment stream, and sends the mandatory first WorkerRegister naming the
// pool. It runs on the initial connect and on every reconnect. A rejected exchange is
// fatal (the worker has no valid control-channel credential); every error is wrapped
// so serve()'s caller can tell a not-leader FailedPrecondition apart.
func (w *WarmRunner) connect(ctx context.Context, dagVersionID string) (agentv1.AgentService_AwaitAssignmentClient, error) {
	// Exchange the projected bootstrap token for a WORKER-scoped JWT before any RPC
	// (ADR 0058 D2), swapping it into the stream's bearer so Register + AwaitAssignment
	// authenticate as the warm worker.
	if w.ExchangeBootstrap {
		if err := ExchangeToken(ctx, w.StreamClient, w.StreamTokens); err != nil {
			return nil, fmt.Errorf("exchanging warm bootstrap token for a worker JWT: %w", err)
		}
	}
	if _, err := w.StreamClient.Register(ctx, &agentv1.RegisterRequest{
		AgentVersion: w.Version,
		Hostname:     w.Hostname,
	}); err != nil {
		return nil, fmt.Errorf("registering warm worker: %w", err)
	}
	stream, err := w.StreamClient.AwaitAssignment(ctx)
	if err != nil {
		return nil, fmt.Errorf("opening assignment stream: %w", err)
	}
	// The mandatory first WorkerMessage names which pool (dag_version) this worker
	// serves, so the control plane only dispatches matching assignments to it.
	if serr := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_Register{
			Register: &agentv1.WorkerRegister{DagVersionId: dagVersionID, PodName: w.PodName},
		},
	}); serr != nil {
		return nil, fmt.Errorf("sending worker register: %w", serr)
	}
	return stream, nil
}

// backoffSleep waits a jittered exponential backoff before reconnect n (1-based),
// capped at maxBackoff, returning ctx.Err() if ctx is canceled first (so a SIGTERM
// during backoff exits cleanly). The doubling loop stops once the cap is reached so
// a large n can never overflow the shift. jitterDelay (a uniform draw in [d/2, d])
// spreads a fleet's reconnects so they do not stampede the leader in lockstep.
func (w *WarmRunner) backoffSleep(ctx context.Context, base, maxBackoff time.Duration, n int) error {
	d := base
	for i := 1; i < n && d < maxBackoff; i++ {
		d *= 2
	}
	if d <= 0 || d > maxBackoff {
		d = maxBackoff
	}
	timer := time.NewTimer(jitterDelay(d))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// redialStream re-establishes the StreamClient for the next reconnect via Redial (a
// fresh dial => a fresh Service backend, so the worker can reach the leader across
// the rotation). It closes the previous reconnect's connection first so reconnects
// do not leak connections. With no Redial wired it is a no-op: the reconnect then
// retries the existing StreamClient (tests / single-dial).
func (w *WarmRunner) redialStream() error {
	if w.Redial == nil {
		return nil
	}
	client, tokens, closer, err := w.Redial()
	if err != nil {
		return err
	}
	w.closeRedialConn()
	w.StreamClient = client
	// Adopt the fresh connection's bearer source so serve()'s exchange (under the
	// exchange transport) swaps the new worker JWT into the source THIS client reads.
	if tokens != nil {
		w.StreamTokens = tokens
	}
	w.streamCloser = closer
	return nil
}

// closeRedialConn best-effort closes the connection the most recent Redial opened.
func (w *WarmRunner) closeRedialConn() {
	if w.streamCloser != nil {
		if cerr := w.streamCloser.Close(); cerr != nil {
			slog.Warn("closing previous warm stream connection after reconnect", "error", cerr)
		}
		w.streamCloser = nil
	}
}

// errIdleRecycle is the sentinel recvAssignment returns when no assignment arrives
// within IdleTTL, so Run can distinguish a D6 idle-recycle (clean exit) from a real
// stream error.
var errIdleRecycle = errors.New("warm worker idle-recycle")

// recvAssignment receives the next assignment, enforcing the D6 idle-TTL. With a
// non-positive IdleTTL it is a plain blocking Recv (the timeout disabled). Otherwise
// it runs Recv in a goroutine feeding a buffered channel and races it against the
// idle timer and ctx: if the timer wins it returns errIdleRecycle, and the Recv
// goroutine does NOT leak — the channel is buffered (size 1) so the goroutine's send
// never blocks even with no reader, and the blocked Recv unblocks with an error once
// Run returns and the stream closes.
func (w *WarmRunner) recvAssignment(ctx context.Context, stream agentv1.AgentService_AwaitAssignmentClient) (*agentv1.WorkAssignment, error) {
	if w.IdleTTL <= 0 {
		return stream.Recv()
	}
	type recvResult struct {
		a   *agentv1.WorkAssignment
		err error
	}
	recvCh := make(chan recvResult, 1) // buffered so the goroutine can always send and exit
	go func() {
		a, err := stream.Recv()
		recvCh <- recvResult{a, err}
	}()
	timer := time.NewTimer(w.IdleTTL)
	defer timer.Stop()
	select {
	case r := <-recvCh:
		return r.a, r.err
	case <-timer.C:
		return nil, errIdleRecycle
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

// serveAssignment claims, runs, and completes a single assignment, then signals
// availability for the next. The returned error is loop-fatal (a stream or
// isolation failure); a failed task is NOT such an error — runOneAttempt already
// reported the terminal FAILED state, so it is logged and the worker keeps serving.
func (w *WarmRunner) serveAssignment(ctx context.Context, stream agentv1.AgentService_AwaitAssignmentClient, a *agentv1.WorkAssignment) error {
	// D4 isolation, part 1: a fresh, empty agent scratch before the attempt runs.
	// This is fail-closed — if we cannot guarantee a clean scratch we tear the
	// worker down rather than risk one attempt observing another's writable state.
	// Done BEFORE the ack on purpose: a scratch failure here means we never ack, so
	// the assignment is reclaimed by lease expiry (the N1b1 path already built),
	// rather than being acked-as-started and then stranded on the worker teardown
	// (which would need the N1d reaper to recover). resetScratch is a fast
	// rm+mkdir, so the pre-ack window stays sub-millisecond — no lease-expiry race.
	if err := w.resetScratch(); err != nil {
		return fmt.Errorf("resetting scratch for assignment %q: %w", a.GetAssignmentId(), err)
	}

	// Claim the lease now that we have committed to running: the control plane
	// holds the attempt for us until this ack and reclaims it if we do not ack
	// within lease_seconds. Sent after the scratch reset so "started" is truthful.
	if err := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_Ack{
			Ack: &agentv1.AssignmentAck{AssignmentId: a.GetAssignmentId(), Started: true},
		},
	}); err != nil {
		return fmt.Errorf("acking assignment %q: %w", a.GetAssignmentId(), err)
	}

	// Adopt this attempt's own credential for every per-attempt unary RPC. Only
	// AttemptTokens (WorkClient's source) is touched; the bootstrap stream is
	// unaffected. The attempt's identity (run/task/try) is resolved by GetTaskSpec
	// from this token, exactly as a dedicated pod resolves it today.
	w.AttemptTokens.Set(a.GetAttemptToken())

	// D4 isolation, part 2 (fresh env) is inherent: attemptRunner builds a new
	// Runner whose buildEnv runs from scratch each attempt, so no attempt inherits
	// another attempt's secrets, XCom, or run-context env.
	sink := w.openSink(ctx)
	r := w.attemptRunner(sink)

	// H3 per-attempt watchdog: a hard ceiling on this attempt, INDEPENDENT of the
	// task's execution_timeout (which runOneAttempt still honors inside). A task that
	// declares no execution_timeout and then wedges is killed here — the child exec
	// runs on attemptCtx, so the deadline cancel kills it; the attempt then fails and
	// is reported, SlotFree fires below, and the worker keeps serving. A non-positive
	// watchdog disables the extra ceiling (fall back to just execution_timeout).
	attemptCtx := ctx
	if w.AttemptWatchdog > 0 {
		var cancel context.CancelFunc
		attemptCtx, cancel = context.WithTimeout(ctx, w.AttemptWatchdog)
		defer cancel()
	}
	if aerr := r.runOneAttempt(attemptCtx); aerr != nil {
		// A failed attempt is a normal, already-reported outcome. Keep serving.
		slog.Warn("warm attempt finished with an error",
			"assignment", a.GetAssignmentId(), "error", aerr)
	}

	// D4 isolation, part 3: no process of this attempt may outlive it. The exec
	// runner already killed the attempt's process group; this also catches a
	// descendant that left the group with setsid, and confirms nothing is left
	// before SlotFree invites the next attempt. Fail-closed: a survivor ends the
	// worker (the reconciler replaces the pod) rather than sharing it with the
	// next attempt (X3.3).
	if err := sweepDescendants(); err != nil {
		return fmt.Errorf("sweeping processes after assignment %q: %w", a.GetAssignmentId(), err)
	}

	// X3.2: sweep what the attempt left (its HOME, a generated dbt profile with a
	// connection secret, /dev/shm) now rather than at the next ack, so it does not
	// stay on disk while the worker idles. Fail-closed like the pre-ack reset: the
	// attempt is already reported, so tearing the worker down loses nothing.
	if w.AttemptHome {
		if err := w.resetScratch(); err != nil {
			return fmt.Errorf("sweeping after assignment %q: %w", a.GetAssignmentId(), err)
		}
	}

	// Signal availability so the control plane may dispatch the next assignment.
	if err := stream.Send(&agentv1.WorkerMessage{
		Msg: &agentv1.WorkerMessage_SlotFree{SlotFree: &agentv1.SlotFree{}},
	}); err != nil {
		return fmt.Errorf("signaling slot free after assignment %q: %w", a.GetAssignmentId(), err)
	}
	return nil
}

// resetScratch removes and recreates the agent's per-attempt scratch dir. This is
// the D4 filesystem scrub: it deletes everything the previous attempt (or its user
// process) wrote UNDER the agent's own scratch — the return-value, extra-links,
// xcom-pushes, and reschedule files, the per-attempt TMPDIR (attemptRunner points
// the child's TMPDIR at a subdir of scratch, #728), plus anything else the runtime
// staged there — so no attempt observes another attempt's writable fs state under
// the agent's control. It deletes ONLY the agent-owned scratch; image-level paths
// and mounts (and $HOME, unless the pod runs with a read-only root filesystem) are
// left untouched and persist for the worker's lifetime.
func (w *WarmRunner) resetScratch() error {
	if err := os.RemoveAll(w.ScratchDir); err != nil {
		return fmt.Errorf("removing scratch %q: %w", w.ScratchDir, err)
	}
	if w.AttemptHome && w.SharedTmpDir != "" {
		if err := w.sweepSharedTmp(); err != nil {
			return err
		}
	}
	if w.AttemptHome && w.SharedMemDir != "" {
		if err := sweepDir(w.SharedMemDir, ""); err != nil {
			return err
		}
	}
	if err := os.MkdirAll(w.ScratchDir, 0o700); err != nil {
		return fmt.Errorf("recreating scratch %q: %w", w.ScratchDir, err)
	}
	return nil
}

// sweepSharedTmp removes every entry of SharedTmpDir except the one holding
// ScratchDir (X3.2). With a read-only root that emptyDir is writable, and the
// runtime writes there by default: the image points DBT_PROFILES_DIR at
// /tmp/leoflow/dbt, and the generated profiles.yml carries the connection
// secret. Like resetScratch it is fail-closed.
func (w *WarmRunner) sweepSharedTmp() error {
	return sweepDir(w.SharedTmpDir, w.ScratchDir)
}

// sweepDir removes every entry of dir except the one that is, or contains, keep
// (empty keeps nothing). The dir itself stays, since it is a mount point.
func sweepDir(dir, keep string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("listing shared dir %q: %w", dir, err)
	}
	if keep != "" {
		keep = filepath.Clean(keep)
	}
	for _, e := range entries {
		p := filepath.Join(dir, e.Name())
		if keep != "" && (p == keep || strings.HasPrefix(keep, p+string(filepath.Separator))) {
			continue
		}
		if err := os.RemoveAll(p); err != nil {
			return fmt.Errorf("removing %q from the shared dir: %w", p, err)
		}
	}
	return nil
}

// openSink opens a fresh per-attempt log sink, falling back to NoopLogSink when no
// factory is configured or the stream cannot be opened (logs are best-effort).
func (w *WarmRunner) openSink(ctx context.Context) LogSink {
	if w.NewSink == nil {
		return NoopLogSink{}
	}
	sink, err := w.NewSink(ctx)
	if err != nil {
		slog.Warn("opening per-attempt log sink; logs will not ship this attempt", "error", err)
		return NoopLogSink{}
	}
	return sink
}

// attemptRunner builds the single-shot Runner that executes one attempt. Its
// per-task output paths live under the agent-owned scratch (wiped between
// attempts), and its Client is the WorkClient bound to AttemptTokens so every
// per-attempt RPC carries the attempt_token.
func (w *WarmRunner) attemptRunner(sink LogSink) *Runner {
	home := ""
	if w.AttemptHome {
		home = filepath.Join(w.ScratchDir, "home")
	}
	return &Runner{
		HomeDir:        home,
		Client:         w.WorkClient,
		Cmd:            w.Cmd,
		Sink:           sink,
		Hostname:       w.Hostname,
		Version:        w.Version,
		Env:            w.Env,
		ReturnPath:     filepath.Join(w.ScratchDir, "return_value.json"),
		LinksPath:      filepath.Join(w.ScratchDir, "extra_links.json"),
		PushesPath:     filepath.Join(w.ScratchDir, "xcom_pushes.json"),
		ReschedulePath: filepath.Join(w.ScratchDir, "reschedule.txt"),
		// Per-attempt TMPDIR under the scratch resetScratch wipes, so the child's
		// temp files never persist into the next attempt on this warm worker (#728).
		TmpDir:             filepath.Join(w.ScratchDir, "tmp"),
		TerminationLogPath: w.TerminationLogPath,
		HeartbeatInterval:  w.HeartbeatInterval,
		Token:              w.AttemptTokens,
	}
}
