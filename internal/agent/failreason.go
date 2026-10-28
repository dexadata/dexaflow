package agent

import "errors"

// Operator-facing classifications for the failures the RUNNER diagnoses itself,
// after registration. They are the same shape, and exist for the same reason, as
// the pre-registration set in bootstrap.go: a CLOSED set of constants, because
// the reason they produce is written to the container termination message, is
// read back by the reconciler as the durable outcome record's Reason, and is then
// served to end users on the task-instance API. Interpolating an error into one
// of them would turn that field into an exfiltration path for credentials,
// internal endpoints, and TLS handshake detail, so no call site ever does.
//
// A failure with no constant here is deliberately UNCLASSIFIED: the record then
// carries no reason at all and the reconciler renders the exit code, which is
// strictly better than putting raw error text on a durable, end-user-visible
// field. Add a constant when there is real guidance to give.
const (
	reasonSecretUnresolved = "the task's external secret backend refused or failed to resolve a declared " +
		"secret, so the task was not started; check the task pod's identity and the backend's permissions."
	reasonOutputUndelivered = "the task's own code finished and its side effects already happened, but its " +
		"outputs (return value, XCom, extra links) could not be delivered; a retry re-runs that code. " +
		"Check the control plane and its XCom storage."
	reasonControlPlaneUnavailable = "the control plane, its database or its Redis did not answer the agent " +
		"before the task's code ran, so the attempt is re-placed without spending a task retry; " +
		"check the control plane's health."
)

// errSecretResolution marks a buildEnv failure that came from the external secret
// backend (ADR 0060 B6), so it can be classified by kind rather than by matching
// on the provider's error text. The sentinel is what keeps classifyEnvFailure off
// the error's message: the message is exactly what must not reach the record.
var errSecretResolution = errors.New("external secret resolution failed")

// classifyEnvFailure maps an environment-build failure to a short, operator-facing
// classification, or "" when it recognizes nothing.
//
// It reads only the error's IDENTITY (an errors.Is against a sentinel), never its
// message, so the result is always one of the constants above or the empty string.
// The unrecognized cases matter as much as the recognized ones: the XCom fetch
// wraps a raw gRPC error that can carry the control-plane endpoint and TLS
// handshake text, and the per-attempt TMPDIR failure carries a filesystem path.
// Both stay unclassified, so neither reaches the durable record.
func classifyEnvFailure(err error) string {
	if errors.Is(err, errSecretResolution) {
		return reasonSecretUnresolved
	}
	return ""
}
