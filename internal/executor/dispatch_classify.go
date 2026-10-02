package executor

import (
	"strings"

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
	default:
		return "unknown"
	}
}

// classifyDispatchError decides whether a synchronous dispatch failure is
// transient cluster backpressure (Backpressure: retry forever, off the attempt
// budget) or a permanent condition (Rejected: bounded retry → dispatch_failed).
// Only Kubernetes apiserver error types can yield the Backpressure verdict; every
// other error is Rejected, which preserves today's path exactly for the Lite
// subprocess executor — its errors are never apiserver StatusErrors, so its
// dispatch handling is unreachable by the Backpressure branch (ADR 0053).
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
// dispatch_failed instead. A genuinely transient webhook (call timeout under a
// Fail policy) is not special-cased; it falls to the permanent default, matching
// today's behavior, rather than risking an infinite re-offer loop on a poison
// spec.
func classifyDispatchError(err error) Disposition {
	if err == nil {
		// Not a real input: Execute only classifies a non-nil dispatch error.
		// Guarded so the classifier is total and defaults to the safe (bounded)
		// path.
		return Rejected
	}
	if apierrors.IsTooManyRequests(err) {
		return Backpressure
	}
	if apierrors.IsForbidden(err) && isQuotaExceeded(err) {
		return Backpressure
	}
	return Rejected
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
