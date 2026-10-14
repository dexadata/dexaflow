package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"html/template"
	"log/slog"
	"net/http"
	"net/url"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// The consent step of a CLI sign-in (ADR 0079 decision 3). The callback of a
// CLI login does not send the browser to the loopback: it answers this page,
// which names the account and the port, and seals the pending sign-in in a
// SameSite=Strict cookie. Only the page's own form, a same-origin POST that
// carries the CSRF token sealed in that cookie, issues the one-time code and
// redirects to the loopback. A page that can only navigate the browser never
// gets that far, and neither can a cross-site POST: it carries no Strict
// cookie, cannot read the token, and fetch metadata names it.

// cliConsentPath is the route the consent form posts to.
const cliConsentPath = "/api/v2/auth/cli/consent"

// cliConsentCookie carries the sealed pending sign-in between the consent
// page and its form. It is scoped to the CLI routes and SameSite=Strict. It
// has no __Host- prefix on purpose: that prefix requires Path=/, which would
// send the sealed sign-in with every request to the origin instead of only
// to the consent route.
const cliConsentCookie = "_oidc_cli_consent"

// cliConsentCookiePath scopes the consent cookie; the write and the clear
// share it so the browser matches the deletion.
const cliConsentCookiePath = "/api/v2/auth/cli/"

// cliConsentTTL bounds how long the consent page may wait for a click.
const cliConsentTTL = 5 * time.Minute

// The decisions the consent form posts.
const (
	consentApprove = "approve"
	consentCancel  = "cancel"
)

// consentArmScript enables the approve button only once the page has been
// visible for about a second and the person has moved the pointer or pressed
// a key on it. It defeats double-clickjacking, where a page opens this one
// under a double click it asked for and the second click lands on the
// button. It is UX only: the server's checks do not rely on it, and without
// it the button simply stays disabled. The CSP allows exactly this script by
// its hash (consentArmScriptHash), never inline script in general.
const consentArmScript = `(function(){var b=document.getElementById('approve'),since=0,moved=false;` +
	`function vis(){if(document.visibilityState==='visible'){if(!since){since=Date.now();}}else{since=0;}}` +
	`function mark(){moved=true;}` +
	`function tick(){vis();if(moved&&since&&Date.now()-since>=1000){b.disabled=false;return;}setTimeout(tick,100);}` +
	`document.addEventListener('visibilitychange',vis);document.addEventListener('pointermove',mark);` +
	`document.addEventListener('keydown',mark);tick();})();`

// consentArmScriptHash is the CSP source that allows consentArmScript.
var consentArmScriptHash = func() string {
	sum := sha256.Sum256([]byte(consentArmScript))
	return "'sha256-" + base64.StdEncoding.EncodeToString(sum[:]) + "'"
}()

// consentPageTemplate asks the person to approve the CLI sign-in. It shares
// the sign-in page's look and carries no secret: the pending sign-in is in
// the HttpOnly cookie, the form only the CSRF token.
var consentPageTemplate = template.Must(template.New("consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dexaflow - Sign in the CLI</title>
<style>
{{ .CSS }}
 .cancel{background:#334155}
</style></head><body>
<form method="post" action="{{ .Action }}">
 <h1>Sign in the Dexaflow CLI?</h1>
{{ if .Message }} <p class="ssoerr" role="alert">{{ .Message }}</p>
{{ else }} <p class="hint">A <code>dexaflow auth login</code> on this computer asked to sign in as
 <strong>{{ .Email }}</strong> (tenant <strong>{{ .Tenant }}</strong>). Its token goes to the program
 listening on this computer at port <strong>{{ .Port }}</strong>.</p>
 <p class="hint">Approve only if you started that command just now. If you did not, cancel:
 a page you visited may have opened this sign-in for someone else.</p>
 <input type="hidden" name="csrf" value="{{ .CSRF }}">
 <button type="submit" name="decision" value="approve" id="approve" disabled>Sign in the CLI</button>
 <button type="submit" name="decision" value="cancel" class="cancel">Cancel</button>
{{ end }}</form>{{ if not .Message }}<script>{{ .Script }}</script>{{ end }}</body></html>`))

// consentPage is what consentPageTemplate renders.
type consentPage struct {
	CSS     template.CSS
	Action  string
	Email   string
	Tenant  string
	Port    string
	CSRF    string
	Message string
	Script  template.JS
}

// renderConsentPage writes the page with headers that keep it out of frames
// and caches. form-action names the loopback too, because a browser applies
// it to the redirect that follows the form.
func renderConsentPage(c *gin.Context, status int, page consentPage, loopback string) {
	page.CSS = template.CSS(signInPageCSS) //nolint:gosec // G203: a constant of this package, never request data
	page.Action = cliConsentPath
	page.Script = template.JS(consentArmScript) //nolint:gosec // G203: a constant of this package, allowed by its CSP hash
	formAction := "'self'"
	if u, err := url.Parse(loopback); err == nil && u.Host != "" {
		formAction += " http://" + u.Host
	}
	writeSignInPageHeaders(c, formAction)
	c.Status(status)
	if err := consentPageTemplate.Execute(c.Writer, page); err != nil {
		c.Abort()
	}
}

// writeSignInPageHeaders sets the headers every page of the CLI sign-in
// answers with: never cached, never framed, no script but the arming script
// by its hash, and forms that may only post to formAction.
func writeSignInPageHeaders(c *gin.Context, formAction string) {
	c.Header("Content-Type", "text/html; charset=utf-8")
	c.Header("Cache-Control", "no-store")
	c.Header("X-Frame-Options", "DENY")
	c.Header("Content-Security-Policy", "default-src 'none'; script-src "+consentArmScriptHash+"; style-src 'unsafe-inline'; form-action "+formAction+"; frame-ancestors 'none'")
}

// askCLIConsent ends a verified CLI login with the consent page: it seals the
// minted token with the account and the CLI binding into the consent cookie
// and shows who and which port will be signed in.
func (d oidcDeps) askCLIConsent(c *gin.Context, b oidc.CLIBinding, user *auth.User, token string) {
	csrf, err := randomURLToken()
	if err != nil {
		abortSSOServerFailure(c, d.logger, "generating consent token", err)
		return
	}
	pending := cliPending{
		Token: token, TokenExpires: tokenExpiry(token, d.cliCodes.now().Add(d.tokenTTL)),
		Redirect: b.Redirect, State: b.State, Challenge: b.Challenge, CSRF: csrf,
		Tenant: user.TenantID, UserID: user.ID, Email: user.Email,
	}
	sealed, err := d.cliCodes.seal(pending, cliConsentAAD, cliConsentTTL)
	if err != nil {
		abortSSOServerFailure(c, d.logger, "sealing cli consent", err)
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cliConsentCookie, sealed, int(cliConsentTTL.Seconds()), cliConsentCookiePath, "", cookieSecure(d.session.insecure), true)
	port := ""
	if u, perr := url.Parse(b.Redirect); perr == nil {
		port = u.Port()
	}
	renderConsentPage(c, http.StatusOK, consentPage{Email: user.Email, Tenant: user.TenantID, Port: port, CSRF: csrf}, b.Redirect)
}

// cliConsentDeps is what the consent handler needs.
type cliConsentDeps struct {
	codes    *cliCodes
	audit    AuthAuditWriter
	logger   *slog.Logger
	insecure bool
}

// cliConsentHandler implements POST /api/v2/auth/cli/consent. It accepts only
// a same-origin post (by fetch metadata, when the browser sends it) that
// carries the consent cookie and the CSRF token sealed in it, each consent
// once. Approve issues the one-time code and sends the browser to the CLI's
// loopback; cancel sends it there with error=access_denied. Every other post
// is refused on a page, and nothing reaches the loopback.
func cliConsentHandler(d cliConsentDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		p, status, reason := openConsent(c, d.codes, cliConsentCookie, cliConsentAAD)
		if reason != "" {
			d.refuse(c, status, p, reason)
			return
		}
		d.clearCookie(c)
		d.decide(c, p, c.PostForm("decision"))
	}
}

// openConsent runs the checks every consent post shares: a same-origin post
// (by fetch metadata, when the browser sends it) carrying the consent cookie
// named cookieName, sealed for aad and unexpired, and the CSRF token sealed
// in it. The consent is then claimed, so each is accepted once per replica.
// A refusal returns its status and a stable reason, with the consent's
// contents when it was authentic.
func openConsent(c *gin.Context, codes *cliCodes, cookieName, aad string) (p cliPending, status int, reason string) {
	if !browserMaySetSession(c) {
		return cliPending{}, http.StatusForbidden, "cross_site"
	}
	cookie, err := c.Request.Cookie(cookieName)
	if err != nil || cookie.Value == "" {
		return cliPending{}, http.StatusBadRequest, "missing_consent"
	}
	p, ok := codes.open(cookie.Value, aad)
	if !ok {
		return cliPending{}, http.StatusBadRequest, "invalid_consent"
	}
	now := codes.now()
	expires := time.Unix(0, p.Expires)
	switch {
	case !now.Before(expires):
		return p, http.StatusBadRequest, "consent_expired"
	case subtle.ConstantTimeCompare([]byte(c.PostForm("csrf")), []byte(p.CSRF)) != 1:
		return p, http.StatusBadRequest, "csrf_mismatch"
	case !codes.consents.claim(p.ID, expires, now):
		return p, http.StatusBadRequest, "consent_replayed"
	}
	return p, 0, ""
}

// decide answers a genuine consent post: a code for approve, a refusal to the
// CLI for anything else.
func (d cliConsentDeps) decide(c *gin.Context, p cliPending, decision string) {
	if decision != consentApprove {
		reason := "declined"
		if decision != consentCancel {
			reason = "unknown_decision"
		}
		recordAuth(c, d.logger, d.audit, auditCLIConsent, p, "denied", map[string]string{"reason": reason})
		c.Redirect(http.StatusSeeOther, loopbackURL(p.binding(), url.Values{"error": {"access_denied"}}))
		return
	}
	code, err := d.codes.sealCode(p)
	if err != nil {
		if d.logger != nil {
			d.logger.Error("oidc: sealing cli sign-in code", "error", err)
		}
		c.Redirect(http.StatusSeeOther, loopbackURL(p.binding(), url.Values{"error": {"server_error"}}))
		return
	}
	recordAuth(c, d.logger, d.audit, auditCLIConsent, p, "success", nil)
	c.Redirect(http.StatusSeeOther, loopbackURL(p.binding(), url.Values{"code": {code}}))
}

// refuse audits and logs a refused consent post and answers it with the
// page, never a redirect.
func (d cliConsentDeps) refuse(c *gin.Context, status int, p cliPending, reason string) {
	if d.logger != nil {
		d.logger.Warn("oidc: cli consent refused", "reason", reason, "tenant", logSafe(p.Tenant), "user", logSafe(p.UserID))
	}
	recordAuth(c, d.logger, d.audit, auditCLIConsent, p, "denied", map[string]string{"reason": reason})
	renderConsentPage(c, status, consentPage{
		Message: "This sign-in can no longer be approved here. Run dexaflow auth login again.",
	}, "")
	c.Abort()
}

// clearCookie expires the consent cookie with the attributes it was set with.
func (d cliConsentDeps) clearCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cliConsentCookie, "", -1, cliConsentCookiePath, "", cookieSecure(d.insecure), true)
}
