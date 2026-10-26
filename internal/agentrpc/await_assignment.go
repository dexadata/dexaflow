package agentrpc

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

// SetWarmPools wires a prebuilt warm-worker assignment registry (ADR 0058 N1b).
// A nil registry (the default) leaves AwaitAssignment inert — it returns
// FailedPrecondition — so with execution.warm_pools_enabled off the transport is
// completely dormant and no running path changes. Used by tests to inject a
// registry with a deterministic lease; callers wire it via EnableWarmPools.
func (s *Server) SetWarmPools(reg *WorkerRegistry) { s.warmPools = reg }

// SetLeaderCheck gates AwaitAssignment to the scheduler leader (warm-pool Hole B).
// Each scheduler replica wires its OWN leadership predicate, so a follower refuses
// the stream (FailedPrecondition) and only the leader (whose leader-only placer
// consults the same in-memory registry the worker registers into) serves it. An
// open stream re-checks the predicate, so a leader that steps down sends its idle
// workers away with the same code (#1602). A
// nil predicate (the default) leaves the handler unchecked, so a single-node or
// unwired deployment serves exactly as before.
func (s *Server) SetLeaderCheck(fn func() bool) { s.leaderCheck = fn }

// EnableWarmPools turns on the warm-worker assignment transport with a
// production registry (ADR 0058 N1b). onReclaim (may be nil) observes reclaim
// events for the future placement layer to consume. Call only when
// execution.warm_pools_enabled is set — the default leaves the handler inert.
func (s *Server) EnableWarmPools(onReclaim func(ReclaimEvent)) {
	s.SetWarmPools(NewWorkerRegistry(onReclaim))
}

// AwaitAssignment is the warm-worker assignment transport (ADR 0058 N1b): a
// long-lived bidi stream over which the control plane pushes per-attempt
// WorkAssignments down and the worker sends its registration, acks, and
// slot-free signals up.
//
// Inert-when-off: if warm pools are not wired the handler refuses immediately
// with FailedPrecondition — the flag-gated dormant state.
//
// Identity: the registry key is the worker's AUTHENTICATED identity from the
// stream's bearer token (via identify), NOT the dag_version_id in the register
// payload — a worker cannot claim an arbitrary identity through the message. The
// payload's dag_version_id only names which pool the worker serves and must be
// non-empty.
//
// After registration two flows run concurrently: the receive loop drains
// WorkerMessages (acks feed the H1 lease machine, slot-free frees the worker)
// while the main select pumps assignments from the worker's outbound channel
// down the stream. The handler exits — deregistering the worker (defer) — on
// context cancellation, the control plane's shutdown signal (SetShutdown), a
// stream Send error, the receive loop ending (clean EOF or a transport
// error), a newer registration superseding this stream after it went silent
// past the liveness grace, or this replica losing scheduler leadership while the
// worker is idle (FailedPrecondition, so the worker re-dials toward the new
// leader). A second stream for an identity whose first stream is
// still live is refused with AlreadyExists (one live registration per identity).
func (s *Server) AwaitAssignment(stream agentv1.AgentService_AwaitAssignmentServer) error {
	if s.warmPools == nil {
		return status.Error(codes.FailedPrecondition, "warm pools disabled")
	}
	// Leader gate (warm-pool Hole B): a follower's in-memory registry is never
	// consulted by the leader-only placer, so a follower refuses the stream and the
	// worker reconnects toward the leader. A nil predicate is unchecked (single-node
	// / tests serve as before).
	if s.leaderCheck != nil && !s.leaderCheck() {
		return status.Error(codes.FailedPrecondition, "not the scheduler leader; reconnect to reach the leader")
	}
	ctx := stream.Context()
	id, err := s.identify(ctx)
	if err != nil {
		return err
	}
	// Scope gate (ADR 0058 D2): the assignment stream is the warm-worker control
	// channel — only a warm-worker-scoped token may open it. A task ("attempt")
	// token is PermissionDenied here, the mirror of requireAttemptToken on the
	// secret/task RPCs.
	if werr := s.requireWarmWorkerToken(id); werr != nil {
		return werr
	}
	// The registry key is the worker's AUTHENTICATED identity — its WorkerID (the
	// token Subject), not the register payload — so a worker cannot claim an
	// arbitrary identity through the message.
	worker, err := s.registerFromStream(stream, id.WorkerID, id.DagVersionID)
	if err != nil {
		return err
	}
	defer s.warmPools.Deregister(worker)
	announceRegistered(stream, worker.identity)

	// Receive loop: acks and slot-free signals feed the registry. It ends on EOF
	// (clean close) or any transport error, reported once on recvErr.
	recvErr := make(chan error, 1)
	go s.pumpWorkerMessages(stream, worker, recvErr)
	return s.serveWorkerStream(stream, worker, id, recvErr)
}

// warmRegisteredHeader is the response header AwaitAssignment sends once the
// worker is registered. A stream a follower refuses never carries it, so the
// worker can tell a leader that accepted it and later stepped down from one
// more refusal, and reset its not-leader reconnect budget (#1602). The agent
// reads the same name (internal/agent warmRegisteredHeader).
const warmRegisteredHeader = "dexaflow-warm-registered"

// announceRegistered sends the registration header. A send failure means the
// stream is already broken, which the serve loop sees on its own, so it is only
// logged.
func announceRegistered(stream agentv1.AgentService_AwaitAssignmentServer, identity string) {
	if err := stream.SendHeader(metadata.Pairs(warmRegisteredHeader, "true")); err != nil {
		slog.Debug("announcing warm worker registration", "identity", identity, "error", err)
	}
}

// serveWorkerStream is AwaitAssignment's main loop once the worker is
// registered: it pumps assignments down the stream and returns the status the
// stream ends with (see AwaitAssignment for the exits).
func (s *Server) serveWorkerStream(stream agentv1.AgentService_AwaitAssignmentServer, worker *registeredWorker, id *auth.AgentIdentity, recvErr <-chan error) error {
	ctx := stream.Context()
	leaderTick, stopLeaderTick := s.leaderRecheck()
	defer stopLeaderTick()

	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
		case <-leaderTick:
			if err := s.sendAwayAfterStepDown(worker); err != nil {
				return err
			}
		case <-s.shutdown:
			// An idle warm worker holds this stream open indefinitely by design, so
			// without this case the bounded graceful stop waits its full budget on
			// every shutdown: the forced path becomes the normal path and the
			// "exceeded its bound" warning stops meaning anything. Unavailable is
			// the code the log stream ends with, and the same code the forced
			// transport close already surfaces to the worker — so the worker's
			// handling is unchanged, it just happens promptly at SIGTERM instead of
			// after the whole stop budget burns. Deliberately NOT FailedPrecondition:
			// that code means "not the leader, reconnect", and a replica on its way
			// out must not pull a worker's bounded reconnect budget back to itself.
			// A nil channel never fires, so an unwired server (tests, embedders)
			// behaves exactly as before.
			return status.Error(codes.Unavailable, "control plane shutting down; assignment stream closed")
		case <-worker.superseded:
			// This stream went silent past the liveness grace and a newer
			// registration under the same identity replaced it. End it so only one
			// connection serves the identity at a time.
			return status.Error(codes.Aborted, "superseded by a newer registration of this worker")
		case rerr := <-recvErr:
			if errors.Is(rerr, io.EOF) {
				return nil
			}
			return peerStatus("receiving worker message", rerr, attemptAttrs(id)...)
		case a := <-worker.send:
			if serr := stream.Send(a); serr != nil {
				return serr
			}
		}
	}
}

// defaultLeaderPoll is how often an open assignment stream re-checks this
// replica's leadership. The leadership watch itself polls every 5 seconds, so a
// one-second re-check adds little to the time a step-down takes to reach the
// workers, and the check is an atomic load.
const defaultLeaderPoll = time.Second

// sendAwayAfterStepDown re-checks leadership for the life of the stream, not
// only at open (#1602): after a step-down without a restart this replica runs no
// placer, while the new leader's registry is empty and its reconciler counts
// these live pods as idle supply, so the pool would stay dark until
// worker_idle_ttl. It returns the FailedPrecondition the stream ends with (the
// worker re-dials toward the leader on it), or nil to keep serving. Only an
// idle worker is sent away (no acked attempt, no assignment awaiting its ack,
// and it leaves the free set in the same step): a busy one finishes and signals
// SlotFree on this stream first, and is sent away at the next check.
func (s *Server) sendAwayAfterStepDown(worker *registeredWorker) error {
	if s.leaderCheck() || !s.warmPools.retireIfIdle(worker) {
		return nil
	}
	slog.Info("warm worker sent to reconnect: this replica is no longer the scheduler leader",
		"identity", worker.identity)
	return status.Error(codes.FailedPrecondition, "not the scheduler leader; reconnect to reach the leader")
}

// leaderRecheck returns the tick channel the stream loop re-checks leadership
// on, and its stop function. With no leader check wired (single node, tests)
// the channel is nil and never fires.
func (s *Server) leaderRecheck() (ticks <-chan time.Time, stop func()) {
	if s.leaderCheck == nil {
		return nil, func() {}
	}
	every := s.leaderPoll
	if every <= 0 {
		every = defaultLeaderPoll
	}
	t := time.NewTicker(every)
	return t.C, t.Stop
}

// registerFromStream reads and validates the mandatory first WorkerMessage (a
// WorkerRegister) and registers the worker under its authenticated identity. The
// dag_version_id in the payload names the pool and must be non-empty and equal
// to the pool the token was minted for (tokenPool): a worker credential is
// issued for one tenant's DAG version, and registering anywhere else would hand
// it another pool's assignments and their secrets. The identity is NOT taken
// from the payload. The bearer token is never logged.
func (s *Server) registerFromStream(stream agentv1.AgentService_AwaitAssignmentServer, identity, tokenPool string) (*registeredWorker, error) {
	first, err := stream.Recv()
	if err != nil {
		if errors.Is(err, io.EOF) {
			return nil, status.Error(codes.FailedPrecondition, "stream closed before worker registration")
		}
		return nil, peerStatus("receiving worker registration", err, "worker", identity)
	}
	reg := first.GetRegister()
	if reg == nil {
		return nil, status.Error(codes.FailedPrecondition, "first worker message must be a register")
	}
	dagVersion := reg.GetDagVersionId()
	if dagVersion == "" {
		return nil, status.Error(codes.InvalidArgument, "worker register missing dag_version_id")
	}
	if dagVersion != tokenPool {
		slog.Warn("warm worker refused: registered for a pool its token was not minted for",
			"identity", identity, "token_dag_version", tokenPool, "register_dag_version", dagVersion)
		return nil, status.Error(codes.PermissionDenied, "worker token is not valid for this dag_version")
	}
	if serr := s.checkAttemptHomeSweep(reg, identity, dagVersion); serr != nil {
		return nil, serr
	}
	// pod_name is the worker's own downward-API pod name, the durable key a started
	// attempt is bound to (ADR 0058 N1d-a1). It is a locator, not a credential — the
	// identity above still governs authorization — so an empty value only means the
	// binding degrades to per-pod liveness for this worker, never a refusal.
	podName := reg.GetPodName()
	send := make(chan *agentv1.WorkAssignment, 1)
	info := WorkerInfo{PodName: podName, SourceDelivery: reg.GetSourceDelivery()}
	worker, err := s.warmPools.RegisterWorker(identity, dagVersion, info, send, stream.Context().Done())
	if err != nil {
		// One live registration per identity: a second stream for a worker whose
		// first stream is still connected and heartbeating is refused, so two
		// connections can never share one worker's assignments.
		slog.Warn("warm worker refused: identity already has a live registration",
			"identity", identity, "dag_version", dagVersion, "pod_name", podName)
		return nil, status.Error(codes.AlreadyExists, "worker identity already has a live registration")
	}
	slog.Info("warm worker registered", "identity", identity, "dag_version", dagVersion, "pod_name", podName)
	return worker, nil
}

// checkAttemptHomeSweep applies the registry's sweep policy to a registration
// that does not confirm the attempt home sweep (#1678, warm workers with a
// read-only root). Such a worker comes from a task image whose agent does not
// report the sweep (an agent before runtime v0.5.1 does not sweep at all; later
// ones that predate the report sweep but cannot say so), or from a pod created
// before the read-only root was turned on. Under SweepWarn it is logged,
// metered and accepted. Under SweepRefuse it is refused and never joins the
// pool, so placement runs its DAG version's attempts on dedicated pods.
func (s *Server) checkAttemptHomeSweep(reg *agentv1.WorkerRegister, identity, dagVersion string) error {
	policy, rec := s.warmPools.sweepPolicyAndRecorder()
	if policy == SweepIgnore || reg.GetAttemptHomeSweep() {
		return nil
	}
	attrs := []any{"reason", ReasonAttemptHomeSweepUnconfirmed,
		"identity", identity, "dag_version", dagVersion, "pod_name", reg.GetPodName()}
	if policy != SweepRefuse {
		slog.Warn("warm worker does not confirm it empties /tmp and /dev/shm between attempts under warm_read_only_root_filesystem; rebuild the task image on a runtime that reports the sweep", attrs...)
		if rec != nil {
			rec.RecordSchedulerDecision(DecisionWarmWorkerSweepUnconfirmed)
		}
		return nil
	}
	slog.Warn("warm worker refused: it does not confirm it empties /tmp and /dev/shm between attempts and warm_require_attempt_home_sweep is on; rebuild the task image on a runtime that reports the sweep", attrs...)
	if rec != nil {
		rec.RecordSchedulerDecision(DecisionWarmWorkerRefusedNoSweep)
	}
	return status.Error(codes.PermissionDenied, ReasonAttemptHomeSweepUnconfirmed+
		": warm workers run with a read-only root filesystem and this worker does not confirm the /tmp and /dev/shm sweep")
}

// pumpWorkerMessages drains WorkerMessages off the stream into the registry
// until the stream ends, then reports the terminating error once on recvErr.
// Every message counts as a liveness signal for the worker's registration.
func (s *Server) pumpWorkerMessages(stream agentv1.AgentService_AwaitAssignmentServer, worker *registeredWorker, recvErr chan<- error) {
	identity := worker.identity
	for {
		msg, rerr := stream.Recv()
		if rerr != nil {
			recvErr <- rerr
			return
		}
		s.warmPools.Touch(worker)
		switch m := msg.Msg.(type) {
		case *agentv1.WorkerMessage_Ack:
			if binding, ok := s.warmPools.Ack(m.Ack.GetAssignmentId(), m.Ack.GetStarted()); ok {
				s.bindWarmAttempt(stream.Context(), binding)
				// After the bind, so the reconciler already sees this worker busy.
				s.warmPools.claimed()
			}
		case *agentv1.WorkerMessage_SlotFree:
			s.warmPools.MarkFree(identity)
		case *agentv1.WorkerMessage_Register:
			// A re-registration on an established stream is the worker's periodic
			// heartbeat: the Touch above already refreshed its liveness, and the
			// worker stays keyed by its authenticated identity.
		}
	}
}

// bindWarmAttempt persists the durable warm-attempt binding a started ack
// established (ADR 0058 N1d-a1): warm_worker_id on the running TI names the warm
// pod now serving the attempt, so a later failover reaper can recover which
// attempts a dead warm pod held.
//
// The worker is already running the attempt, so a missing binding is not
// harmless: an unbound running warm attempt looks like a dedicated attempt whose
// pod vanished, and the pod-lost reaper would fail it while it runs (#1600). A
// store error is therefore retried with backoff (bindRetryDelays) until the
// write applies or the stream ends. It is still never fatal to the live stream:
// once the retries are spent the error is logged and the worker keeps serving.
func (s *Server) bindWarmAttempt(ctx context.Context, b *WarmBinding) {
	err := s.store.BindWarmAttempt(ctx, b.RunID, b.TaskID, b.TryNumber, b.AttemptEpoch, b.PodName)
	for _, d := range s.bindRetryDelays {
		if err == nil {
			return
		}
		if werr := sleepCtx(ctx, d); werr != nil {
			err = errors.Join(err, werr)
			break
		}
		err = s.store.BindWarmAttempt(ctx, b.RunID, b.TaskID, b.TryNumber, b.AttemptEpoch, b.PodName)
	}
	if err != nil {
		slog.Error("persisting warm attempt binding failed after retries; the attempt runs unbound",
			"run", b.RunID, "task", b.TaskID, "try", b.TryNumber, "pod_name", b.PodName, "error", err)
	}
}

// sleepCtx waits d, or returns ctx's error if ctx ends first.
func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// defaultBindRetryDelays is the backoff between BindWarmAttempt retries: about
// 12 seconds in all, enough to ride out a connection reset or a short failover,
// and well inside the 60 second pod-lost grace that an unbound attempt would
// otherwise hit.
var defaultBindRetryDelays = []time.Duration{
	100 * time.Millisecond, 200 * time.Millisecond, 400 * time.Millisecond,
	800 * time.Millisecond, 1600 * time.Millisecond, 3200 * time.Millisecond,
	5 * time.Second,
}
