package agent

import (
	"log/slog"
	"os"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/dexadata/dexaflow/internal/taskoutcome"
)

// BootstrapStage names the startup step a pre-registration failure happened in.
// The stage narrows the classification: the same transport error means something
// different while reading a token file than while exchanging one.
type BootstrapStage int

const (
	// StageToken is reading the projected ServiceAccount token from the pod.
	StageToken BootstrapStage = iota
	// StageDial is establishing the gRPC channel to the control plane.
	StageDial
	// StageExchange is trading the bootstrap token for a task-scoped credential.
	StageExchange
)

// Operator-facing bootstrap failure classifications. They are a CLOSED set of
// constants on purpose: the reason they produce is written to the container
// termination message, travels to the control plane, and is served to end users
// through the API and the UI. Interpolating an error into one of these would turn
// an end-user-visible field into an exfiltration path for credentials, token
// paths, and internal endpoints, so no call site ever does. Each names the
// misconfiguration an operator should go and check.
const (
	reasonTokenUnreadable = "the agent could not read its projected ServiceAccount token inside the pod; " +
		"check the pod's projected token volume and the control plane's dispatch configuration."
	reasonUnreachable = "the agent could not reach the control plane (refused, timed out, or TLS trust failed); " +
		"check network policy, DNS, and the agent's CA bundle."
	reasonTokenRejected = "the control plane rejected this pod's projected ServiceAccount token; " +
		"check the control plane's RBAC for tokenreviews and the configured token audience."
	reasonExchangeDisabled = "the control plane's agent token exchange is not enabled; " +
		"check the control plane's agent transport configuration."
	reasonInsecureChannel = "the control plane refused to exchange a token over an insecure channel; " +
		"enable gRPC TLS between the agent and the control plane."
	reasonUnresolvedPod = "the control plane could not resolve this pod to a task attempt; " +
		"the attempt may have been retried or cleared while the pod was starting."
	reasonUnknown = "the agent failed to start before registering with the control plane; " +
		"inspect the task pod for the underlying cause."
)

// ClassifyBootstrapFailure maps a pre-registration startup failure to a short,
// operator-facing classification, or "" when err is nil.
//
// It reads only the gRPC status CODE and the stage — never the error's message —
// so the result is always one of the constants above. That is what makes the
// reason safe to persist and serve: the control plane deliberately does not echo
// token details back to the agent, and this classifier must not reintroduce a
// channel that does.
func ClassifyBootstrapFailure(stage BootstrapStage, err error) string {
	if err == nil {
		return ""
	}
	if stage == StageToken {
		return reasonTokenUnreadable
	}
	if stage == StageDial {
		return reasonUnreachable
	}
	switch status.Code(err) {
	case codes.Unauthenticated:
		return reasonTokenRejected
	case codes.Unimplemented:
		return reasonExchangeDisabled
	case codes.PermissionDenied:
		return reasonInsecureChannel
	case codes.Internal:
		return reasonUnresolvedPod
	case codes.Unavailable, codes.DeadlineExceeded:
		return reasonUnreachable
	default:
		// A code we have no specific guidance for, including codes.Unknown — which
		// is what a plain non-status error reports. Say plainly that the agent
		// never registered rather than inventing a cause.
		return reasonUnknown
	}
}

// ReportBootstrapFailure records a classified pre-registration failure on the
// container termination message, so the control plane learns WHY a pod died
// without its agent ever completing the handshake. Without it the reconciler sees
// only a failed pod and the operator is left with "no logs available".
//
// It is best-effort and never fails the caller: the agent is already exiting, and
// a lost diagnostic must not change the exit path. An empty path (outside a pod)
// is a no-op.
func ReportBootstrapFailure(path string, stage BootstrapStage, err error) {
	reason := ClassifyBootstrapFailure(stage, err)
	if path == "" || reason == "" {
		return
	}
	enc, encErr := taskoutcome.FailedBecause(reason).Encode()
	if encErr != nil {
		slog.Warn("encoding bootstrap failure record", "error", encErr)
		return
	}
	if werr := os.WriteFile(path, []byte(enc), 0o644); werr != nil { //nolint:gosec // G306: the kubelet reads this file; 0644 matches the outcome-record writer.
		slog.Warn("writing bootstrap failure record", "path", path, "error", werr)
	}
}
