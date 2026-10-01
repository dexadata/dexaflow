package agentrpc

import (
	"github.com/dexadata/dexaflow/internal/auth"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Scope gating (ADR 0058 D2) — the security guarantee that a warm worker's
// bootstrap credential authorizes ONLY the control channel and can never resolve
// secrets or a task. It is enforced per-RPC, right after identify(), so a
// leaked or forged warm-worker token that reaches a secret/task handler is
// rejected before it touches the vault, the task spec, or XCom.
//
// The two credentials are disjoint by scope:
//   - a warm-worker token (Scope == auth.ScopeWarmWorker) is accepted ONLY on
//     Register + AwaitAssignment (the control channel) and PermissionDenied on
//     every secret/task RPC (requireAttemptToken);
//   - a task ("attempt") token (Scope == "") is accepted on every secret/task RPC
//     as before and PermissionDenied on AwaitAssignment (requireWarmWorkerToken).
//
// Register accepts both: warm workers and task pods both register.

// requireAttemptToken rejects a warm-worker-scoped credential on a secret/task
// RPC. A warm worker's control-channel token must never resolve secrets, the task
// spec, or XCom, nor report/heartbeat a task — those are the per-attempt token's
// sole province (delivered in-band over WorkAssignment). Returns nil for a task
// token (the default scope), leaving today's behavior unchanged.
func (s *Server) requireAttemptToken(id *auth.AgentIdentity) error {
	if id.Scope == auth.ScopeWarmWorker {
		return status.Error(codes.PermissionDenied,
			"a warm-worker token authorizes only Register and AwaitAssignment, not secrets or task RPCs")
	}
	return nil
}

// requireWarmWorkerToken rejects a task-scoped credential on the warm-worker
// control stream (AwaitAssignment). Only a warm worker's bootstrap token may open
// the assignment stream; a task token calling it is a misuse (or an attempt to
// pull assignments with the wrong credential) and is denied.
func (s *Server) requireWarmWorkerToken(id *auth.AgentIdentity) error {
	if id.Scope != auth.ScopeWarmWorker {
		return status.Error(codes.PermissionDenied,
			"AwaitAssignment requires a warm-worker token")
	}
	return nil
}
