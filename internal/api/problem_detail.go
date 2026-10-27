package api

import (
	"errors"

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

// safeDetail returns the phrase Dexaflow composed for this failure, or fallback
// when the error carries no such phrase.
//
// The default is deny. Only a domain.SafeError, an error someone deliberately
// wrote as client-facing, contributes text to a response body; everything
// else, including every fmt.Errorf wrap of a driver error, is replaced. That
// way a newly introduced storage error cannot leak by omission: it leaks only
// if someone rewrites it as a SafeError, which is the one construct whose
// GoDoc says its message is shown to clients.
func safeDetail(err error, fallback string) string {
	var safe *domain.SafeError
	if errors.As(err, &safe) && safe.ClientMessage() != "" {
		return safe.ClientMessage()
	}
	return fallback
}
