package api

import (
	"github.com/dexadata/dexaflow/internal/domain"
)

// Client-facing details for a repository failure whose cause must stay
// server-side. They are constants because a repository error normally wraps a
// driver error, and pgconn renders a *pgconn.PgError as "severity: message
// (SQLSTATE code)" with our constraint, table and column names inside the
// message: schema disclosure to any authenticated tenant (CWE-209, #961).
//
// One phrase per condition, not one phrase overall: the split is the same one
// unreadyDetail makes in health.go. "Does not exist", "conflicts", "was
// rejected" and "could not be completed" send a caller to four different
// places, and collapsing them into a single opaque message would cost real
// diagnosability to buy no extra privacy.
const (
	detailNotFound        = "the requested resource does not exist"
	detailConflict        = "the request conflicts with the current state of the resource"
	detailClientClosed    = "the client closed the request before it completed"
	detailInvalidInput    = "the request was rejected by a validation rule"
	detailLimitExceeded   = "the request would exceed a limit set for this tenant"
	detailTenantSuspended = "the tenant is suspended"
	detailInternal        = "the request could not be completed; see the server logs"
)

// safeDetail returns the phrase Dexaflow composed for this failure, or
// fallback: domain.ClientMessageOr, the deny-by-default rule shared with the
// agent gRPC service. Only the fallback phrases differ, on purpose: an API
// client is pointed at the server logs, an agent at the control-plane logs.
func safeDetail(err error, fallback string) string {
	return domain.ClientMessageOr(err, fallback)
}
