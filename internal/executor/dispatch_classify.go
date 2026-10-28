package executor

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Disposition is the typed outcome of a dispatch attempt, returned by
// Executor.Execute so the scheduler can act on WHY a dispatch failed without
// reaching into Kubernetes error types itself. Classification lives on the
// execution layer — the only layer that knows how a given runtime signals
// backpressure — and travels up the seam as this enum (ADR 0051 Phase 4).
type Disposition int

const (
	// Dispatched means the task was handed to the runtime (a pod created, an agent
	// subprocess started). It is not a terminal outcome: the task's real result
	// arrives asynchronously over gRPC. It is the zero value, so a Request that
	// never touches classification reads as a clean hand-off.
	Dispatched Disposition = iota
	// Backpressure is transient cluster backpressure from the Kubernetes apiserver
	// — a ResourceQuota 403 or an API Priority & Fairness 429 — that clears once
	// the cluster has headroom again. The scheduler backs the task off and
	// re-offers it indefinitely, never counting it against the dispatch-attempt
	// budget and never driving the task to dispatch_failed: the cluster asking
	// Dexaflow to slow down is not the user's task failing.
	Backpressure
	// Rejected is a permanent dispatch failure that will not clear on its own: an
	// invalid image, an RBAC denial, an admission-webhook rejection, a bad spec,
	// or any error the classifier does not recognize (including every error a Lite
	// subprocess executor can return). The scheduler keeps the historical
	// bounded-backoff → dispatch_failed behavior (ADR 0031 Amendment A).
	Rejected
	// Deferred means the dispatch was never attempted: the control plane's own
	// buffered dispatch queue was full (or shutting down), or the Kubernetes
	// client's own rate limiter refused the request before sending it. Nothing reached the
	// runtime, so the scheduler leaves the task scheduled for a later tick and
	// records nothing: no backoff, no dispatch-attempt increment, no
	// dispatch_failed. It is local backpressure, not a failed dispatch.
	Deferred
	// Refused is a permanent "this task may not run here" decided before the
	// runtime is called, such as a task larger than its size under an operator
	// resource unit (ADR 0066 §3). A retry cannot change the verdict, so the
	// scheduler fails the task on the first attempt, with the message as its
	// reason, and spends no dispatch retries on it. Unlike Rejected, which also
	// covers errors that clear on retry, Refused is never retried.
	Refused
	// Unavailable means the Kubernetes API could not take the request: a 503,
	// a 504, a 500 (such as a fail-closed admission webhook that is down), a
	// connection refused or reset, or the client deadline (#1606). That is a
	// control plane outage (an upgrade, an etcd leader change), not a verdict
	// on the task, so the scheduler re-offers it without spending the
	// dispatch-attempt budget. Unlike Backpressure it is bounded: the task
	// fails as dispatch_failed once the API has been unavailable to it for
	// longer than the scheduler's window (ADR 0093). The create may have
	// reached the apiserver before the response was lost; retrying is still
	// safe because the re-offer and every dispatch bump the attempt epoch,
	// which fences a pod the lost create left behind: its agent's RUNNING
	// report is refused before user code starts.
	Unavailable
)

// String renders the disposition for logs and error notes.
func (d Disposition) String() string {
	switch d {
	case Dispatched:
		return "dispatched"
	case Backpressure:
		return "backpressure"
	case Rejected:
		return "rejected"
	case Deferred:
		return "deferred"
	case Refused:
		return "refused"
	case Unavailable:
		return "unavailable"
	default:
		return "unknown"
	}
}

// classifyDispatchError decides whether a synchronous dispatch failure is
// transient cluster backpressure (Backpressure: retry forever, off the attempt
// budget), an unavailable Kubernetes API (Unavailable: retried off the attempt
// budget for a bounded time, ADR 0093) or a request the apiserver understood
// and refused (Rejected: bounded retry → dispatch_failed). Only the Kubernetes
// executor calls it; the Lite subprocess executor returns its own verdicts
// and never reaches it, so its dispatch handling is unchanged (ADR 0053).
//
// The two backpressure signals:
//   - 429 Too Many Requests: API Priority & Fairness shed the request. Always
//     transient.
//   - 403 Forbidden carrying the ResourceQuota admission plugin's "exceeded
//     quota" marker: the namespace quota is full. Transient — it clears as the
//     namespace's running pods complete.
//
// A 403 Forbidden WITHOUT the quota marker is an RBAC denial: permanent, and
// deliberately not conflated with quota (apierrors.IsForbidden covers both — the
// message is the only discriminator). An admission-webhook rejection ("denied the
// request") also arrives as Forbidden and is likewise treated as permanent: a
// webhook that rejects a spec rejects every re-offer of that same spec, so
// retrying forever would loop silently — a bounded retry surfaces it as
// dispatch_failed instead. A webhook that is down under a Fail policy is a 500,
// not a 403, and is Unavailable (isAPIUnavailable): bounded by a time window
// rather than retried forever, so a poison spec cannot loop silently either.
func classifyDispatchError(err error) Disposition {
	if err == nil {
		// Not a real input: Execute only classifies a non-nil dispatch error.
		// Guarded so the classifier is total and defaults to the safe (bounded)
		// path.
		return Rejected
	}
	if isClientRateLimiterRefusal(err) {
		return Deferred
	}
	if apierrors.IsTooManyRequests(err) {
		return Backpressure
	}
	if apierrors.IsForbidden(err) && isQuotaExceeded(err) {
		return Backpressure
	}
	if isAPIUnavailable(err) {
		return Unavailable
	}
	return Rejected
}

// isAPIUnavailable reports whether err says the Kubernetes API could not take
// the request, rather than that it took the request and refused it (#1606):
//   - 503 ServiceUnavailable: the apiserver is starting, shutting down, or lost
//     etcd (a leader change).
//   - 504 Timeout and 500 ServerTimeout: the apiserver or a proxy in front of it
//     gave up waiting.
//   - 500 InternalError: the apiserver failed, which includes a fail-closed
//     admission webhook that cannot be reached and a 502 from a load balancer
//     in front of a managed control plane (client-go reads a non-Status 5xx as
//     InternalError). A webhook that answered and denied the request is a 403
//     and stays Rejected.
//   - a connection refused or reset, or a timeout of the transport: no
//     response at all.
//   - the client deadline (context.DeadlineExceeded): the request ran out of
//     time before an answer came back. The client rate limiter's own deadline
//     refusal is Deferred and handled before this.
//
// Only typed errors count: a bare string that merely mentions "connection
// refused" is not an apiserver or transport error and stays Rejected.
func isAPIUnavailable(err error) bool {
	if apierrors.IsServiceUnavailable(err) || apierrors.IsTimeout(err) ||
		apierrors.IsServerTimeout(err) || apierrors.IsInternalError(err) {
		return true
	}
	if errors.Is(err, context.DeadlineExceeded) ||
		errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.ECONNRESET) {
		return true
	}
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}

// clientRateLimiterRefusal is the prefix client-go puts on an error from its
// own client-side rate limiter (rest.Request.tryThrottle). The limiter returns
// it before the request is sent, when waiting for a token would run past the
// caller's deadline, so nothing reached the apiserver. client-go leaves a plain
// context cancellation or deadline unwrapped, so this marker never covers a
// request that may have been sent.
const clientRateLimiterRefusal = "client rate limiter Wait returned an error"

// isClientRateLimiterRefusal reports whether err is the client-side rate
// limiter refusing a request before sending it. That is local backpressure, the
// same as a full dispatch buffer, so the task is Deferred: left scheduled for a
// later tick without spending its dispatch-attempt budget (#1596). Counting it
// as Rejected let a throttled wide fan-out fail healthy tasks as
// dispatch_failed although no create was ever attempted.
func isClientRateLimiterRefusal(err error) bool {
	return strings.Contains(err.Error(), clientRateLimiterRefusal)
}

// isQuotaExceeded reports whether a Forbidden error is a ResourceQuota rejection
// rather than an RBAC denial. Both surface as reason=Forbidden, so the message is
// the discriminator: the ResourceQuota admission plugin formats its rejection as
// "exceeded quota: <name>, requested: ...", while an RBAC denial reads "... cannot
// create resource ...", which lacks the marker. The caller gates this on
// IsForbidden, so a non-apiserver error whose text merely mentions quota can
// never reach it.
func isQuotaExceeded(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "exceeded quota")
}
