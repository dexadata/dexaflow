package agentrpc

import (
	"context"
	"errors"
	"io"
	"log/slog"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
	"google.golang.org/grpc/codes"
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
// the stream (FailedPrecondition) and only the leader — whose leader-only placer
// consults the same in-memory registry the worker registers into — serves it. A
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
// stream Send error, or the receive loop ending (clean EOF or a transport
// error).
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
	worker, err := s.registerFromStream(stream, id.WorkerID)
	if err != nil {
		return err
	}
	defer s.warmPools.Deregister(worker)

	// Receive loop: acks and slot-free signals feed the registry. It ends on EOF
	// (clean close) or any transport error, reported once on recvErr.
	recvErr := make(chan error, 1)
	go s.pumpWorkerMessages(stream, id.WorkerID, recvErr)

	for {
		select {
		case <-ctx.Done():
			return status.FromContextError(ctx.Err()).Err()
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

// registerFromStream reads and validates the mandatory first WorkerMessage (a
// WorkerRegister) and registers the worker under its authenticated identity. The
// dag_version_id in the payload names the pool and must be non-empty; the
// identity is NOT taken from the payload. The bearer token is never logged.
func (s *Server) registerFromStream(stream agentv1.AgentService_AwaitAssignmentServer, identity string) (*registeredWorker, error) {
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
	// pod_name is the worker's own downward-API pod name, the durable key a started
	// attempt is bound to (ADR 0058 N1d-a1). It is a locator, not a credential — the
	// identity above still governs authorization — so an empty value only means the
	// binding degrades to per-pod liveness for this worker, never a refusal.
	podName := reg.GetPodName()
	send := make(chan *agentv1.WorkAssignment, 1)
	worker := s.warmPools.Register(identity, dagVersion, podName, send)
	slog.Info("warm worker registered", "identity", identity, "dag_version", dagVersion, "pod_name", podName)
	return worker, nil
}

// pumpWorkerMessages drains WorkerMessages off the stream into the registry
// until the stream ends, then reports the terminating error once on recvErr.
func (s *Server) pumpWorkerMessages(stream agentv1.AgentService_AwaitAssignmentServer, identity string, recvErr chan<- error) {
	for {
		msg, rerr := stream.Recv()
		if rerr != nil {
			recvErr <- rerr
			return
		}
		switch m := msg.Msg.(type) {
		case *agentv1.WorkerMessage_Ack:
			if binding, ok := s.warmPools.Ack(m.Ack.GetAssignmentId(), m.Ack.GetStarted()); ok {
				s.bindWarmAttempt(stream.Context(), binding)
			}
		case *agentv1.WorkerMessage_SlotFree:
			s.warmPools.MarkFree(identity)
		case *agentv1.WorkerMessage_Register:
			// A re-registration on an established stream is redundant; the worker is
			// already keyed by its authenticated identity. Ignore.
		}
	}
}

// bindWarmAttempt persists the durable warm-attempt binding a started ack
// established (ADR 0058 N1d-a1): warm_worker_id on the running TI names the warm
// pod now serving the attempt, so a later failover reaper can recover which
// attempts a dead warm pod held. It is BEST-EFFORT — a persist failure is logged
// and swallowed, never fatal to the live stream: a DB blip must not tear down a
// worker that is already running the attempt. The reaper degrades gracefully to
// per-pod liveness for an attempt whose binding did not land.
func (s *Server) bindWarmAttempt(ctx context.Context, b *WarmBinding) {
	if err := s.store.BindWarmAttempt(ctx, b.RunID, b.TaskID, b.TryNumber, b.PodName); err != nil {
		slog.Warn("persisting warm attempt binding (best-effort; worker keeps serving)",
			"run", b.RunID, "task", b.TaskID, "try", b.TryNumber, "pod_name", b.PodName, "error", err)
	}
}
