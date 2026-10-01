package api

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/dexadata/dexaflow/internal/oidc"
)

func denyLog(t *testing.T, run func(d oidcDeps, c *gin.Context)) string {
	t.Helper()
	gin.SetMode(gin.TestMode)
	var buf bytes.Buffer
	d := oidcDeps{logger: slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequestWithContext(context.Background(), "GET", "/api/v2/auth/oidc/callback", nil)
	run(d, c)
	return buf.String()
}

// TestEveryDenyReachesTheLog is the diagnosability half of the SSO audit.
//
// Every fail-closed path funnels through deny, which recorded an audit row and
// wrote a 403 and logged nothing. The audit row is the record of truth, but it
// lives in a Postgres table reachable only through an API that needs a working
// session, and with provider: oidc and an empty break-glass list there is no
// session to be had. So the one channel an operator can actually read carried
// nothing at all: a deployment rejecting 100% of logins looked, in the logs,
// exactly like a deployment nobody was trying to use.
func TestEveryDenyReachesTheLog(t *testing.T) {
	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.deny(c, auditOIDCLoginFailure, "default", "", "someone@example.com", "no_user_jit_off")
	})

	if !strings.Contains(out, "no_user_jit_off") {
		t.Errorf("the deny reason never reaches the log, so the only copy is in a table an SSO-locked-out operator cannot query:\n%s", out)
	}
	if !strings.Contains(out, "WARN") {
		t.Errorf("the deny is not logged at WARN; a rejected login is not a debug detail:\n%s", out)
	}
}

// TestTokenInvalidCarriesTheUnderlyingError is the worst of the diagnosability
// holes. verifyReason collapses everything it does not recognize into
// token_invalid: a JWKS fetch failure, a network timeout to the IdP, an audience
// mismatch, a signature surprise. The reason is stable and non-secret, which is
// right for an audit row, but the real error was discarded entirely, so a
// transient IdP outage and a wrong client_id were indistinguishable.
func TestTokenInvalidCarriesTheUnderlyingError(t *testing.T) {
	underlying := errors.New("oidc: expected audience \"right-client\" got [\"wrong-client\"]")

	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.rejectVerify(c, underlying)
	})

	if !strings.Contains(out, "token_invalid") {
		t.Fatalf("the audit reason is missing from the log:\n%s", out)
	}
	if !strings.Contains(out, "expected audience") {
		t.Errorf("token_invalid is logged without the error that produced it, so every unrecognized failure is indistinguishable:\n%s", out)
	}
}

// TestRecognizedReasonsDoNotRepeatThemselves guards the other direction. Every
// sentinel verifyReason recognizes is a bare package-level errors.New with fixed
// text, returned unwrapped, so attaching it beside its own reason writes the same
// words twice. The exception is ErrGroupOverage, whose text carries the remedy
// rather than a restatement of the reason.
func TestRecognizedReasonsDoNotRepeatThemselves(t *testing.T) {
	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.rejectVerify(c, oidc.ErrTenantNotAllowed)
	})

	if !strings.Contains(out, "tenant_not_allowed") {
		t.Fatalf("the reason is missing:\n%s", out)
	}
	if strings.Contains(out, "error=") {
		t.Errorf("a recognized reason attached its own sentinel again, which says nothing the reason did not:\n%s", out)
	}
}

// TestNamedVerificationFailuresGetTheirOwnReason is the same diagnosability
// defect one layer down. verify.go declares ten sentinels and verifyReason maps
// seven, so ErrMissingExpiry, ErrNoSubject and ErrGroupOverage fell into the
// catch-all and were audited as token_invalid, the reason that means "we did not
// recognize this". Each is recognized, each names a distinct and operator-fixable
// condition, and the overage one is the worst to lose: Entra past ~200 group
// memberships omits the groups claim entirely, so the most heavily grouped users
// (usually the most privileged) are the only ones who cannot log in, and the
// audit row says only that their token was invalid.
func TestNamedVerificationFailuresGetTheirOwnReason(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{oidc.ErrMissingExpiry, "token_missing_expiry"},
		{oidc.ErrNoSubject, "token_no_subject"},
		{oidc.ErrGroupOverage, "group_claim_overage"},
	} {
		if got := verifyReason(tc.err); got != tc.want {
			t.Errorf("verifyReason(%v) = %q, want %q; it is audited as the catch-all, so a condition the server recognized reads as one it did not", tc.err, got, tc.want)
		}
	}
}

// TestGroupOverageKeepsItsGuidance guards the one recognized sentinel whose text
// is the fix. Giving it a reason of its own moves it off the arm that attaches
// the cause, so the actionable half ("configure Entra app roles or the groups
// scope") would have been dropped in the same change that made it diagnosable.
func TestGroupOverageKeepsItsGuidance(t *testing.T) {
	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.rejectVerify(c, oidc.ErrGroupOverage)
	})

	if !strings.Contains(out, "group_claim_overage") {
		t.Fatalf("the reason is missing:\n%s", out)
	}
	if !strings.Contains(out, "app roles") {
		t.Errorf("the overage guidance never reaches the operator, so the log names the condition and not the fix:\n%s", out)
	}
}

// TestDenyLogsTheUserItAlreadyResolved covers the three denials that happen after
// the user row is in hand (inactive, tenant_mismatch, role_reconcile_failed).
// deny takes a userID on every one of them and the log line dropped it, so the
// operator saw which reason fired and not which of possibly many accounts it
// fired for. The email is not a substitute: a JIT-provisioned duplicate is
// exactly the case where two rows share one address.
func TestDenyLogsTheUserItAlreadyResolved(t *testing.T) {
	out := denyLog(t, func(d oidcDeps, c *gin.Context) {
		d.deny(c, auditOIDCLoginFailure, "default", "usr_42", "someone@example.com", "tenant_mismatch")
	})

	if !strings.Contains(out, "usr_42") {
		t.Errorf("the denial names no user, so the reason cannot be tied to a row:\n%s", out)
	}
}
