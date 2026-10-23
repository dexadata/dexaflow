package api

import (
	"context"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"html/template"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// The device sign-in of the CLI (#1704, ADR 0081), for a machine with no
// browser or whose loopback the browser cannot reach: an SSH session, a
// container. It has the shape of the device authorization grant (RFC 8628),
// served by Dexaflow itself, so the server stays the only OIDC client
// (ADR 0079) and no IdP needs a device endpoint.
//
//  1. The CLI posts to /api/v2/auth/cli/device and receives a device code it
//     keeps, a short user code it prints, and the verification page's URL.
//  2. The person opens that page in any browser, which starts the server's
//     own OIDC login, the UI's round trip: the same callback, tenant pin,
//     role mapping and audit. No code is looked up before that sign-in, so
//     an anonymous caller can neither guess codes nor spend a budget that
//     someone else needs.
//  3. The callback asks the signed-in person for the user code (filled in
//     from verification_uri_complete when they opened that link). Wrong
//     codes are limited per user. A live code leads to a consent page that
//     shows the code, the account, and where and when the sign-in started,
//     and asks the person to confirm that the code is the one their
//     terminal shows (RFC 8628 section 5.4: a phished person was sent a code
//     they did not start). Only that same-origin form approves the sign-in.
//  4. The CLI polls /api/v2/auth/cli/token with the device code and hears
//     authorization_pending, slow_down, access_denied or expired_token until
//     the approval, then the token a password login answers, once.
//
// The poll and the approval can reach different replicas, so the pending
// sign-in lives in the database (CLIDeviceStore), keyed by HMACs of the two
// codes: neither code nor any token is stored. The token is minted when the
// approved sign-in is redeemed, with the roles the sign-in resolved.

// The routes of the device sign-in. The verification page sits directly
// under /api/v2/auth/ so the URL a person types stays short; the code entry
// and the consent form post under /api/v2/auth/cli/, where their cookies
// are scoped.
const (
	cliDevicePath        = "/api/v2/auth/cli/device"
	deviceVerifyPath     = "/api/v2/auth/device"
	cliDeviceCodePath    = "/api/v2/auth/cli/device/code"
	cliDeviceConsentPath = "/api/v2/auth/cli/device/consent"
)

// deviceGrantType is the grant_type of a device poll (RFC 8628 section 3.4).
const deviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// cliDeviceTTL bounds how long a device sign-in waits for its approval and
// redemption; cliDeviceInterval is how often the CLI may poll.
const (
	cliDeviceTTL      = 10 * time.Minute
	cliDeviceInterval = 5 * time.Second
)

// cliDeviceMaxLive caps the device sign-ins in progress across the cluster,
// so the unauthenticated start route cannot fill the table, and
// cliDeviceMaxLivePerRequester those started from one requester, so one
// requester cannot fill the cluster's cap: reaching it takes 2,000 of them.
// A requester is an IPv4 address or an IPv6 /56 (requesterV6Bits), the block
// a subscriber is commonly given, so a /48 counts as 256 requesters and not
// as 65,536.
const (
	cliDeviceMaxLive             = 10_000
	cliDeviceMaxLivePerRequester = 5
	requesterV6Bits              = 56
)

// The budgets of the device sign-in. Each is spent before the work it
// bounds, with auth.RateLimiter.Allow, which checks and spends in one step,
// so concurrent requests cannot overrun it.
//
//   - A start costs one of deviceStartRateLimitPerMinute per address (by
//     /64 for IPv6, see auth.AddressBucket).
//   - Opening the sign-in from the verification page costs one of
//     deviceSignInRateLimitPerMinute per address (by /64); it looks nothing
//     up.
//   - A user code entered after the sign-in costs one of
//     deviceCodeFailuresPerMinute of the signed-in user on this replica,
//     given back when the code is live, so only wrong codes count. No budget is shared between
//     users, so nobody can spend another person's.
//   - Polls get a budget of their own per address (by /64), generous enough
//     for several CLIs behind one address polling every five seconds,
//     because the interval of each sign-in is enforced by slow_down.
const (
	deviceStartRateLimitPerMinute  = 3
	deviceSignInRateLimitPerMinute = 10
	deviceCodeFailuresPerMinute    = 10
	devicePollRateLimitPerMinute   = 120
)

// The user code: eight letters from twenty consonants, 20^8 = 2.56e10
// codes, about 34.6 bits. No vowels, so no words, and no letters that read
// like digits (RFC 8628 section 6.1). Codes are entered only after single
// sign-on, so a guesser needs an account of the IdP that the tenant pin and
// the role mapping accept, and each account gets ten wrong codes a minute on
// each replica (the budget is in memory): 100 over a code's ten minutes,
// which with the cluster's cap of 10,000 live codes finds one with
// probability about 100 * 10000 / 2.56e10, roughly 1 in 25,600, per account,
// replica and ten minutes, every guess audited as that account. A found code only signs the stranger's CLI in as the guesser.
const (
	userCodeAlphabet = "BCDFGHJKLMNPQRSTVWXZ"
	userCodeLen      = 8
	// userCodeMaxInput bounds what normalizeUserCode reads.
	userCodeMaxInput = 32
)

// cliDeviceConsentCookie carries the sealed device consent between its page
// and its form, and cliDeviceEntryCookie the signed-in account between the
// page that asks for the code and its form, both scoped like the loopback
// consent cookie.
const (
	cliDeviceConsentCookie = "_oidc_cli_device"
	cliDeviceEntryCookie   = "_oidc_cli_device_entry"
)

// cliDeviceConsentAAD binds a sealed device consent and cliDeviceEntryAAD a
// sealed code entry, so none of a loopback consent, a code, an entry and a
// consent can be posted as another.
const (
	cliDeviceConsentAAD = "dexaflow cli device consent v1"
	cliDeviceEntryAAD   = "dexaflow cli device entry v1"
)

// auditCLIDeviceVerify records a user code refused after the sign-in, as
// the signed-in user.
const auditCLIDeviceVerify = "oidc.cli_device_verify"

// cliDeviceHashInfo is the HKDF info of the key the codes are hashed under.
const cliDeviceHashInfo = "dexaflow cli device code key"

// The purposes a code is hashed for, so a device code and a user code never
// share a hash.
const (
	hashDeviceCode = "device_code"
	hashUserCode   = "user_code"
	hashRequester  = "requester"
)

// requesterAddrMax bounds the address stored with a sign-in.
const requesterAddrMax = 64

// clientAddress is the caller's address as the router resolves it (trusted
// proxies applied), bounded for storage and display.
func clientAddress(c *gin.Context) string {
	ip := logSafe(c.ClientIP())
	if ip == "" {
		return "unknown"
	}
	if len(ip) > requesterAddrMax {
		return ip[:requesterAddrMax]
	}
	return ip
}

// rateLimitByAddress is rateLimitByIP keyed by auth.AddressBucket, so an
// IPv6 caller cannot rotate addresses within its /64 for a fresh budget.
func rateLimitByAddress(limiter *auth.RateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		if !spendRateBudget(c, limiter) {
			c.Abort()
			return
		}
		c.Next()
	}
}

// deviceCodeMaxLen bounds the device code a poll may send.
const deviceCodeMaxLen = 128

// CLIDeviceStore keeps the device sign-ins of the CLI (ADR 0081). storage
// implements it. Every method decides on the database's clock and keys a
// sign-in by the HMAC of its codes, never the codes.
type CLIDeviceStore interface {
	// CreateCLIDeviceAuthorization starts a sign-in as req describes. It is
	// auth.ErrDeviceSignInsFull or auth.ErrDeviceSignInsFromRequester over a
	// cap, and domain.ErrConflict when the user code is taken.
	CreateCLIDeviceAuthorization(ctx context.Context, req auth.DeviceAuthorizationRequest) (auth.DeviceAuthorization, error)
	// FindCLIDeviceByUserCode returns the pending, unexpired sign-in of a
	// user code, or domain.ErrNotFound.
	FindCLIDeviceByUserCode(ctx context.Context, userCodeHash string) (auth.DeviceAuthorization, error)
	// DecideCLIDeviceAuthorization approves a pending sign-in as user, or
	// cancels it. It is domain.ErrNotFound when the sign-in is not pending,
	// expired, or the user is not in user.TenantID.
	DecideCLIDeviceAuthorization(ctx context.Context, id string, approve bool, user auth.User) error
	// PollCLIDeviceAuthorization answers one poll; an approved sign-in is
	// redeemed by exactly one.
	PollCLIDeviceAuthorization(ctx context.Context, deviceCodeHash string) (auth.DevicePoll, error)
}

// deviceStartResponse is what POST /api/v2/auth/cli/device answers (RFC 8628
// section 3.2).
type deviceStartResponse struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int    `json:"expires_in"`
	Interval                int    `json:"interval"`
}

// cliDeviceDeps is what the device routes need.
type cliDeviceDeps struct {
	store       CLIDeviceStore
	codes       *cliCodes
	hashKey     []byte
	flow        *oidc.Flow
	audit       AuthAuditWriter
	logger      *slog.Logger
	insecure    bool
	verifyURI   string
	idpOrigin   string
	jwtSecret   string
	tokenTTL    time.Duration
	pollLimiter *auth.RateLimiter
	// signInLimiter is the per-address budget of the verification page's
	// post and codeFailures the per-user budget of wrong codes.
	signInLimiter *auth.RateLimiter
	codeFailures  *auth.RateLimiter
}

// newCLIDeviceDeps builds the device sign-in when it can be served: single
// sign-on is configured, a store is wired, and the OIDC redirect URL names
// the server's public origin. It returns nil otherwise. The router fills in
// codes, which the device consent shares with the loopback sign-in.
func newCLIDeviceDeps(deps Dependencies) *cliDeviceDeps {
	if deps.Auth.OIDCFlow == nil || deps.Auth.CLIDevices == nil {
		return nil
	}
	verify := deviceVerifyURI(deps.Auth.OIDCSettings.RedirectURL)
	if verify == "" {
		if deps.Observability.Logger != nil {
			deps.Observability.Logger.Warn("oidc: the device sign-in of the CLI is off: the OIDC redirect URL names no public origin")
		}
		return nil
	}
	if len(deps.Server.TrustedProxies) == 0 && deps.Observability.Logger != nil {
		deps.Observability.Logger.Warn("oidc: the device sign-in of the CLI is served with no server.trusted_proxies: " +
			"behind a proxy or ingress every caller has its address, so they share its rate limits and the consent page cannot say where a sign-in started")
	}
	return &cliDeviceDeps{
		store:       deps.Auth.CLIDevices,
		hashKey:     newDeviceHashKey(deps.Auth.JWTSecret),
		flow:        deps.Auth.OIDCFlow,
		audit:       deps.Auth.AuthAudit,
		logger:      deps.Observability.Logger,
		insecure:    deps.Auth.SessionCookieInsecure,
		verifyURI:   verify,
		idpOrigin:   originOf(deps.Auth.OIDCFlow.AuthEndpoint()),
		jwtSecret:   deps.Auth.JWTSecret,
		tokenTTL:    time.Duration(deps.Auth.TokenTTLSecs) * time.Second,
		pollLimiter: auth.NewRateLimiter(devicePollRateLimitPerMinute, time.Minute),

		signInLimiter: auth.NewRateLimiter(deviceSignInRateLimitPerMinute, time.Minute),
		codeFailures:  auth.NewRateLimiter(deviceCodeFailuresPerMinute, time.Minute),
	}
}

// registerCLIDevice registers the device routes. The start has a small
// budget of its own; the verification form spends its own per address and
// the code entry its own per user (verifyHandler, codeHandler); the code
// entry and the consent also spend the OIDC routes' per-address budget.
func registerCLIDevice(r *gin.Engine, d *cliDeviceDeps, oidcLimiter *auth.RateLimiter) {
	startLimiter := auth.NewRateLimiter(deviceStartRateLimitPerMinute, time.Minute)
	r.POST(cliDevicePath, rateLimitByAddress(startLimiter), d.startHandler())
	r.GET(deviceVerifyPath, d.verifyPageHandler())
	r.POST(deviceVerifyPath, d.verifyHandler())
	r.POST(cliDeviceCodePath, rateLimitByIP(oidcLimiter), d.codeHandler())
	r.POST(cliDeviceConsentPath, rateLimitByIP(oidcLimiter), d.consentHandler())
}

// deviceVerifyURI is the absolute URL of the verification page, on the
// origin of the configured OIDC redirect URL: the public address of the
// server, which the operator set and the IdP already calls. It is "" when
// that URL has no origin, and the device sign-in is then not served.
func deviceVerifyURI(redirectURL string) string {
	u, err := url.Parse(redirectURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host + deviceVerifyPath
}

// originOf is the scheme and host of an absolute URL, or "".
func originOf(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

// newDeviceHashKey derives the key the codes are hashed under from the JWT
// secret, so every replica computes the same hash and nothing else derived
// from the secret shares the key.
func newDeviceHashKey(jwtSecret string) []byte {
	key, err := hkdf.Key(sha256.New, []byte(jwtSecret), nil, cliDeviceHashInfo, 32)
	if err != nil {
		panic(fmt.Sprintf("hkdf of a 32-byte key: %v", err)) // unreachable: 32 bytes is within the limit
	}
	return key
}

// hash is the hex HMAC-SHA256 of code for purpose.
func (d *cliDeviceDeps) hash(purpose, code string) string {
	mac := hmac.New(sha256.New, d.hashKey)
	mac.Write([]byte(purpose))
	mac.Write([]byte{0})
	mac.Write([]byte(code))
	return hex.EncodeToString(mac.Sum(nil))
}

// newUserCode draws a user code, every letter equally likely.
func newUserCode() (string, error) {
	// 240 is the largest multiple of 20 a byte holds: bytes at or above it
	// are dropped so the modulo does not favor the first letters.
	const limit = 240
	out := make([]byte, 0, userCodeLen)
	buf := make([]byte, 2*userCodeLen)
	for len(out) < userCodeLen {
		if _, err := rand.Read(buf); err != nil {
			return "", fmt.Errorf("drawing a user code: %w", err)
		}
		for _, b := range buf {
			if b >= limit || len(out) == userCodeLen {
				continue
			}
			out = append(out, userCodeAlphabet[int(b)%len(userCodeAlphabet)])
		}
	}
	return string(out), nil
}

// formatUserCode writes a normalized code as the CLI prints it: XXXX-XXXX.
func formatUserCode(code string) string {
	return code[:userCodeLen/2] + "-" + code[userCodeLen/2:]
}

// normalizeUserCode reads what a person typed: case, spaces and dashes do
// not matter. It returns the eight letters, or "" for anything that cannot be
// a code, which then reaches no lookup.
func normalizeUserCode(s string) string {
	if len(s) > userCodeMaxInput {
		return ""
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '-' || r == ' ' || r == '\t':
			continue
		case r >= 'a' && r <= 'z':
			r -= 'a' - 'A'
		}
		if !strings.ContainsRune(userCodeAlphabet, r) {
			return ""
		}
		b.WriteRune(r)
	}
	if b.Len() != userCodeLen {
		return ""
	}
	return b.String()
}

// startHandler implements POST /api/v2/auth/cli/device: it starts a device
// sign-in and answers its codes. Only the hashes reach the store.
func (d *cliDeviceDeps) startHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		deviceCode, err := randomURLToken()
		if err != nil {
			d.startFailed(c, "generating device code", err)
			return
		}
		requester := clientAddress(c)
		// A user code already live is drawn again; three collisions in a
		// row mean the space is crowded, which the cap answers below.
		for range 3 {
			userCode, uerr := newUserCode()
			if uerr != nil {
				d.startFailed(c, "generating user code", uerr)
				return
			}
			_, err = d.store.CreateCLIDeviceAuthorization(c.Request.Context(), auth.DeviceAuthorizationRequest{
				DeviceCodeHash: d.hash(hashDeviceCode, deviceCode),
				UserCodeHash:   d.hash(hashUserCode, userCode),
				RequesterHash:  d.hash(hashRequester, auth.AddressBlock(requester, requesterV6Bits)),
				RequesterAddr:  requester,
				TTL:            cliDeviceTTL, Interval: cliDeviceInterval,
				MaxLive: cliDeviceMaxLive, MaxLivePerRequester: cliDeviceMaxLivePerRequester,
			})
			if errors.Is(err, domain.ErrConflict) {
				continue
			}
			if err != nil {
				break
			}
			shown := formatUserCode(userCode)
			c.JSON(http.StatusOK, deviceStartResponse{
				DeviceCode:              deviceCode,
				UserCode:                shown,
				VerificationURI:         d.verifyURI,
				VerificationURIComplete: d.verifyURI + "?" + url.Values{"user_code": {shown}}.Encode(),
				ExpiresIn:               int(cliDeviceTTL.Seconds()),
				Interval:                int(cliDeviceInterval.Seconds()),
			})
			return
		}
		switch {
		case errors.Is(err, auth.ErrDeviceSignInsFromRequester):
			AbortProblem(c, http.StatusTooManyRequests, "rate limited", "too many device sign-ins from this address are in progress; finish one, or wait ten minutes")
			return
		case err == nil || errors.Is(err, domain.ErrConflict) || errors.Is(err, auth.ErrDeviceSignInsFull) || errors.Is(err, auth.ErrDeviceStartsBusy):
			AbortProblem(c, http.StatusServiceUnavailable, "service unavailable", "too many device sign-ins are in progress; try again in a few minutes")
			return
		}
		d.startFailed(c, "storing device sign-in", err)
	}
}

// startFailed logs why a start failed and answers a 500 that says nothing
// more.
func (d *cliDeviceDeps) startFailed(c *gin.Context, what string, err error) {
	if d.logger != nil {
		d.logger.Error("oidc: cli device sign-in: "+what, "error", err)
	}
	AbortProblem(c, http.StatusInternalServerError, "internal error", "the device sign-in could not start; try again")
}

// deviceVerifyTemplate is the page the verification URI opens: it starts
// the sign-in, carrying the code of verification_uri_complete when there is
// one, and asks for the code only after it.
var deviceVerifyTemplate = template.Must(template.New("device").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dexaflow - Sign in the CLI on another device</title>
<style>
{{ .CSS }}
</style></head><body>
<form method="post" action="{{ .Action }}">
 <h1>Sign in the Dexaflow CLI</h1>
{{ if .Message }} <p class="ssoerr" role="alert">{{ .Message }}</p>
{{ end }} <p class="hint">Sign in with single sign-on first. You then enter the code that
 <code>dexaflow auth login --device</code> shows in your terminal, and confirm it before the CLI is signed in.</p>
 <input type="hidden" name="user_code" value="{{ .UserCode }}">
 <button type="submit" autofocus>Continue with single sign-on</button>
</form></body></html>`))

// deviceVerifyPage is what deviceVerifyTemplate renders.
type deviceVerifyPage struct {
	CSS      template.CSS
	Action   string
	UserCode string
	Message  string
}

// deviceEntryTemplate asks the signed-in person for the user code. It posts
// the code with the sealed entry.
var deviceEntryTemplate = template.Must(template.New("device-entry").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dexaflow - Sign in the CLI on another device</title>
<style>
{{ .CSS }}
 #user_code{font-size:1.2rem;letter-spacing:.15em;text-transform:uppercase;text-align:center}
</style></head><body>
<form method="post" action="{{ .Action }}">
 <h1>Sign in the Dexaflow CLI</h1>
{{ if .Message }} <p class="ssoerr" role="alert">{{ .Message }}</p>
{{ end }} <p class="hint">Signed in as <strong>{{ .Email }}</strong> (tenant <strong>{{ .Tenant }}</strong>).
 Enter the code that <code>dexaflow auth login --device</code> shows in your terminal.</p>
 <label for="user_code">Code</label>
 <input id="user_code" name="user_code" value="{{ .UserCode }}" autocomplete="off" autocapitalize="characters" spellcheck="false" maxlength="32" required autofocus>
 <input type="hidden" name="csrf" value="{{ .CSRF }}">
 <button type="submit">Continue</button>
</form></body></html>`))

// deviceEntryPage is what deviceEntryTemplate renders.
type deviceEntryPage struct {
	CSS      template.CSS
	Action   string
	Email    string
	Tenant   string
	UserCode string
	CSRF     string
	Message  string
}

// The messages of the verification and entry pages. None says why a
// sign-on was refused; the audit trail and the server log do.
const (
	deviceMsgUnknownCode = "That code is not valid, or it expired. Check the code in your terminal, or run dexaflow auth login --device again."
	deviceMsgRateLimited = "Too many attempts. Wait a minute and try again."
	deviceMsgSSORefused  = "Single sign-on did not complete. Ask whoever administers this Dexaflow; the reason is in its audit trail."
	deviceMsgSSOFailed   = "Single sign-on failed on the server. Try again; if it keeps failing, the server log has the cause."
	deviceMsgCrossSite   = "This form was not sent from this page. Open the address your terminal shows and start again there."
	deviceMsgServerError = "The code could not be checked. Try again in a moment."
	deviceMsgEntryGone   = "This page can no longer take a code. Open the address your terminal shows and sign in again."
)

// renderVerifyPage writes the verification page. Its form may post to this
// server only, and its post redirects to the IdP, which a browser checks
// against form-action too.
func (d *cliDeviceDeps) renderVerifyPage(c *gin.Context, status int, page deviceVerifyPage) {
	page.CSS = template.CSS(signInPageCSS) //nolint:gosec // G203: a constant of this package, never request data
	page.Action = deviceVerifyPath
	formAction := "'self'"
	if d.idpOrigin != "" {
		formAction += " " + d.idpOrigin
	}
	writeSignInPageHeaders(c, formAction)
	c.Status(status)
	if err := deviceVerifyTemplate.Execute(c.Writer, page); err != nil {
		c.Abort()
	}
}

// verifyPageHandler implements GET /api/v2/auth/device: the button that
// starts the sign-in, carrying the code of verification_uri_complete when it
// is well formed, and a message after a refused sign-on.
func (d *cliDeviceDeps) verifyPageHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		page := deviceVerifyPage{}
		if code := normalizeUserCode(c.Query("user_code")); code != "" {
			page.UserCode = formatUserCode(code)
		}
		switch c.Query(ssoErrorParam) {
		case ssoErrorRefused:
			page.Message = deviceMsgSSORefused
		case ssoErrorServer:
			page.Message = deviceMsgSSOFailed
		}
		d.renderVerifyPage(c, http.StatusOK, page)
	}
}

// verifyHandler implements POST /api/v2/auth/device: it starts the server's
// OIDC login for a device sign-in, carrying a well-formed code to fill in
// after it. It looks no code up.
func (d *cliDeviceDeps) verifyHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !browserMaySetSession(c) {
			d.renderVerifyPage(c, http.StatusForbidden, deviceVerifyPage{Message: deviceMsgCrossSite})
			return
		}
		if !d.signInLimiter.Allow(auth.AddressBucket(c.ClientIP())) {
			d.renderVerifyPage(c, http.StatusTooManyRequests, deviceVerifyPage{Message: deviceMsgRateLimited})
			return
		}
		prefill := ""
		if code := normalizeUserCode(c.PostForm("user_code")); code != "" {
			prefill = formatUserCode(code)
		}
		c.Set(contextKeyDeviceLogin, true)
		beginOIDCLogin(c, d.flow, d.logger, d.insecure, oidc.StatePayload{
			Next: "/", Device: &oidc.DeviceBinding{UserCode: prefill},
		}, http.StatusSeeOther)
	}
}

// askDeviceCode ends the sign-in of a device sign-in with the page that asks
// for the user code: it seals the account and its roles into the entry
// cookie. No code has been looked up and no token is minted.
func askDeviceCode(c *gin.Context, codes *cliCodes, insecure bool, logger *slog.Logger, user auth.User, prefill string, status int, message string) {
	csrf, err := randomURLToken()
	if err != nil {
		abortSSOServerFailure(c, logger, "generating entry token", err)
		return
	}
	entry := cliPending{CSRF: csrf, Roles: user.Roles, Tenant: user.TenantID, UserID: user.ID, Email: user.Email}
	sealed, err := codes.seal(entry, cliDeviceEntryAAD, cliConsentTTL)
	if err != nil {
		abortSSOServerFailure(c, logger, "sealing cli device entry", err)
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cliDeviceEntryCookie, sealed, int(cliConsentTTL.Seconds()), cliConsentCookiePath, "", cookieSecure(insecure), true)
	page := deviceEntryPage{
		CSS:    template.CSS(signInPageCSS), //nolint:gosec // G203: a constant of this package, never request data
		Action: cliDeviceCodePath, Email: user.Email, Tenant: user.TenantID,
		UserCode: prefill, CSRF: csrf, Message: message,
	}
	writeSignInPageHeaders(c, "'self'")
	c.Status(status)
	if err := deviceEntryTemplate.Execute(c.Writer, page); err != nil {
		c.Abort()
	}
}

// codeHandler implements POST /api/v2/auth/cli/device/code: a signed-in
// person's user code. The entry is checked as a consent is (openConsent).
// The code costs one of the user's budget before it is looked up, given back
// when it is live; a live code leads to the consent page, anything else to
// the same page asking again.
func (d *cliDeviceDeps) codeHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, status, reason := openConsent(c, d.codes, cliDeviceEntryCookie, cliDeviceEntryAAD)
		if reason != "" {
			d.refuseEntry(c, status, p, reason)
			return
		}
		d.clearEntryCookie(c)
		user := auth.User{ID: p.UserID, TenantID: p.Tenant, Email: p.Email, Roles: p.Roles}
		key := p.Tenant + "\x00" + p.UserID
		if !d.codeFailures.Allow(key) {
			d.auditCode(c, p, "rate_limited")
			askDeviceCode(c, d.codes, d.insecure, d.logger, user, "", http.StatusTooManyRequests, deviceMsgRateLimited)
			return
		}
		code := normalizeUserCode(c.PostForm("user_code"))
		var found auth.DeviceAuthorization
		err := domain.ErrNotFound
		if code != "" {
			found, err = d.store.FindCLIDeviceByUserCode(c.Request.Context(), d.hash(hashUserCode, code))
		}
		switch {
		case errors.Is(err, domain.ErrNotFound):
			d.auditCode(c, p, "unknown_code")
			askDeviceCode(c, d.codes, d.insecure, d.logger, user, "", http.StatusBadRequest, deviceMsgUnknownCode)
			return
		case err != nil:
			d.codeFailures.Refund(key)
			if d.logger != nil {
				d.logger.Error("oidc: cli device: finding user code", "error", err)
			}
			askDeviceCode(c, d.codes, d.insecure, d.logger, user, "", http.StatusInternalServerError, deviceMsgServerError)
			return
		}
		d.codeFailures.Refund(key)
		d.askDeviceConsent(c, deviceSignIn{
			ID: found.ID, UserCode: formatUserCode(code),
			RequesterAddr: found.RequesterAddr, StartedAt: found.CreatedAt,
		}, user)
	}
}

// auditCode audits and logs a user code refused to a signed-in user.
func (d *cliDeviceDeps) auditCode(c *gin.Context, p cliPending, reason string) {
	if d.logger != nil {
		d.logger.Warn("oidc: cli device user code refused", "reason", reason, "tenant", logSafe(p.Tenant), "user", logSafe(p.UserID))
	}
	recordAuth(c, d.logger, d.audit, auditCLIDeviceVerify, p, "denied", map[string]string{"reason": reason, "grant": deviceGrantType})
}

// refuseEntry answers an entry post that cannot be used. One that names a
// signed-in user (expired, a CSRF mismatch, replayed) is audited as that
// user; one that names nobody (cross-site, no cookie, not sealed as an
// entry) is only logged, so anonymous posts cannot flood the audit trail.
func (d *cliDeviceDeps) refuseEntry(c *gin.Context, status int, p cliPending, reason string) {
	if p.UserID != "" {
		d.auditCode(c, p, reason)
	} else if d.logger != nil {
		d.logger.Warn("oidc: cli device entry refused", "reason", reason, "client", logSafe(c.ClientIP()))
	}
	message := deviceMsgEntryGone
	if reason == "cross_site" {
		message = deviceMsgCrossSite
	}
	d.renderVerifyPage(c, status, deviceVerifyPage{Message: message})
	c.Abort()
}

// clearEntryCookie expires the entry cookie with the attributes it was set
// with.
func (d *cliDeviceDeps) clearEntryCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cliDeviceEntryCookie, "", -1, cliConsentCookiePath, "", cookieSecure(d.insecure), true)
}

// deviceConsentTemplate asks the person to confirm the user code and
// approve, or tells them how the sign-in ended. The confirmation checkbox is
// required by the browser and checked again by the server.
var deviceConsentTemplate = template.Must(template.New("device-consent").Parse(`<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>Dexaflow - Sign in the CLI on another device</title>
<style>
{{ .CSS }}
 .cancel{background:#334155}
 .usercode{font-size:1.6rem;font-weight:700;letter-spacing:.2em;text-align:center;margin:1rem 0;color:#f8fafc}
 label.confirm{display:flex;gap:.5rem;align-items:flex-start;color:#e2e8f0;font-size:.8rem;line-height:1.35}
 label.confirm input{width:auto;margin:.1rem 0 0}
</style></head><body>
<form method="post" action="{{ .Action }}">
 <h1>Sign in the Dexaflow CLI on another device?</h1>
{{ if .Message }} <p class="ssoerr" role="alert">{{ .Message }}</p>
{{ else if .Notice }} <p class="hint">{{ .Notice }}</p>
{{ else }} <p class="hint">A <code>dexaflow auth login --device</code> asked to sign in as
 <strong>{{ .Email }}</strong> (tenant <strong>{{ .Tenant }}</strong>). Its terminal shows this code:</p>
 <p class="usercode">{{ .UserCode }}</p>
 <p class="hint">It was started {{ .Age }}{{ if .ShowAddresses }} from <strong>{{ .RequesterAddr }}</strong>.
 This browser is at <strong>{{ .BrowserAddr }}</strong>{{ end }}.</p>
 <p class="hint">Approve only if you started that command yourself just now and its terminal shows this exact code.
 If someone sent you this link or this code, cancel: approving would give them your access.</p>
 <label class="confirm"><input type="checkbox" name="confirm" value="yes" required>
 <span>I started this sign-in and my terminal shows {{ .UserCode }}</span></label>
 <input type="hidden" name="csrf" value="{{ .CSRF }}">
 <button type="submit" name="decision" value="approve" id="approve" disabled>Sign in the CLI</button>
 <button type="submit" name="decision" value="cancel" class="cancel" formnovalidate>Cancel</button>
{{ end }}</form>{{ if .Armed }}<script>{{ .Script }}</script>{{ end }}</body></html>`))

// deviceConsentPage is what deviceConsentTemplate renders: the form when
// neither Message nor Notice is set.
type deviceConsentPage struct {
	CSS      template.CSS
	Action   string
	Email    string
	Tenant   string
	UserCode string
	CSRF     string
	Message  string
	Notice   string
	Armed    bool
	Script   template.JS
	// Age, RequesterAddr and BrowserAddr say when and from where the
	// sign-in was started, next to where this browser is (RFC 8628 section
	// 5.4), so a person sent someone else's code can tell. The addresses
	// are shown only when ShowAddresses (showDeviceAddresses).
	Age           string
	RequesterAddr string
	BrowserAddr   string
	ShowAddresses bool
}

// deviceSignIn is the pending sign-in a person entered the live code of, as
// the consent page shows it.
type deviceSignIn struct {
	ID            string
	UserCode      string
	RequesterAddr string
	StartedAt     time.Time
}

// showDeviceAddresses reports whether the consent page compares the
// addresses. Two equal private, loopback or unknown addresses are most
// likely a proxy that is not in server.trusted_proxies, seen on both sides,
// so the comparison would say nothing and is left out.
func showDeviceAddresses(requester, browser string) bool {
	if requester != browser {
		return true
	}
	ip := net.ParseIP(requester)
	return ip != nil && !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast()
}

// The messages of the device consent.
const (
	deviceMsgConsentGone  = "This sign-in can no longer be approved here. Open the address your terminal shows and sign in again."
	deviceMsgNotConfirmed = "The code was not confirmed, so the CLI was not signed in. Open the address your terminal shows again and check that the code matches."
	deviceMsgNotPending   = "This code can no longer be approved: it expired, or it was already approved or canceled. Run dexaflow auth login --device again."
	deviceMsgDecideFailed = "The answer could not be recorded. Open the address your terminal shows again in a moment."
	deviceMsgCanceled     = "Sign-in canceled. The CLI stops waiting and signs in nothing."
)

// renderDeviceConsent writes the device consent page. Its form posts here
// and answers with a page, so form-action is this server alone.
func renderDeviceConsent(c *gin.Context, status int, page deviceConsentPage) {
	page.CSS = template.CSS(signInPageCSS) //nolint:gosec // G203: a constant of this package, never request data
	page.Action = cliDeviceConsentPath
	page.Armed = page.Message == "" && page.Notice == ""
	page.Script = template.JS(consentArmScript) //nolint:gosec // G203: a constant of this package, allowed by its CSP hash
	writeSignInPageHeaders(c, "'self'")
	c.Status(status)
	if err := deviceConsentTemplate.Execute(c.Writer, page); err != nil {
		c.Abort()
	}
}

// askDeviceConsent answers a live user code with the consent page: it
// seals the account, its roles and the pending sign-in into the consent
// cookie and shows the user code to confirm. No token is minted yet.
func (d *cliDeviceDeps) askDeviceConsent(c *gin.Context, s deviceSignIn, user auth.User) {
	csrf, err := randomURLToken()
	if err != nil {
		abortSSOServerFailure(c, d.logger, "generating consent token", err)
		return
	}
	pending := cliPending{
		Device: s.ID, UserCode: s.UserCode, CSRF: csrf, Roles: user.Roles,
		Tenant: user.TenantID, UserID: user.ID, Email: user.Email,
	}
	sealed, err := d.codes.seal(pending, cliDeviceConsentAAD, cliConsentTTL)
	if err != nil {
		abortSSOServerFailure(c, d.logger, "sealing cli device consent", err)
		return
	}
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cliDeviceConsentCookie, sealed, int(cliConsentTTL.Seconds()), cliConsentCookiePath, "", cookieSecure(d.insecure), true)
	browser := clientAddress(c)
	renderDeviceConsent(c, http.StatusOK, deviceConsentPage{
		Email: user.Email, Tenant: user.TenantID, UserCode: s.UserCode, CSRF: csrf,
		Age:           deviceAge(time.Since(s.StartedAt)),
		RequesterAddr: s.RequesterAddr,
		BrowserAddr:   browser,
		ShowAddresses: showDeviceAddresses(s.RequesterAddr, browser),
	})
}

// deviceAge says how long ago a sign-in started, in whole minutes.
func deviceAge(d time.Duration) string {
	switch m := int(d / time.Minute); {
	case m < 1:
		return "less than a minute ago"
	case m == 1:
		return "1 minute ago"
	default:
		return fmt.Sprintf("%d minutes ago", m)
	}
}

// consentHandler implements POST /api/v2/auth/cli/device/consent. It
// accepts what the loopback consent accepts (openConsent) and, to approve,
// the confirmation that the code matches. Approve and cancel are recorded in
// the store, once across every replica; the browser gets a page either way.
func (d *cliDeviceDeps) consentHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		p, status, reason := openConsent(c, d.codes, cliDeviceConsentCookie, cliDeviceConsentAAD)
		if reason != "" {
			d.refuseConsent(c, status, p, reason, deviceMsgConsentGone)
			return
		}
		d.clearCookie(c)
		decision := c.PostForm("decision")
		if decision != consentApprove {
			d.cancel(c, p, decision)
			return
		}
		if c.PostForm("confirm") != "yes" {
			d.refuseConsent(c, http.StatusBadRequest, p, "code_not_confirmed", deviceMsgNotConfirmed)
			return
		}
		user := auth.User{ID: p.UserID, TenantID: p.Tenant, Email: p.Email, Roles: p.Roles}
		if !d.decide(c, p, true, user) {
			return
		}
		recordAuth(c, d.logger, d.audit, auditCLIConsent, p, "success", map[string]string{"grant": deviceGrantType})
		renderDeviceConsent(c, http.StatusOK, deviceConsentPage{
			Notice: "The CLI is signed in as " + p.Email + ". Return to your terminal: the command finishes on its own. You can close this window.",
		})
	}
}

// cancel records a declined device sign-in, so the CLI hears access_denied.
func (d *cliDeviceDeps) cancel(c *gin.Context, p cliPending, decision string) {
	reason := "declined"
	if decision != consentCancel {
		reason = "unknown_decision"
	}
	if !d.decide(c, p, false, auth.User{}) {
		return
	}
	recordAuth(c, d.logger, d.audit, auditCLIConsent, p, "denied", map[string]string{"reason": reason, "grant": deviceGrantType})
	renderDeviceConsent(c, http.StatusOK, deviceConsentPage{Notice: deviceMsgCanceled})
}

// decide records the answer in the store, and answers the page itself when
// it could not: false then means the caller stops.
func (d *cliDeviceDeps) decide(c *gin.Context, p cliPending, approve bool, user auth.User) bool {
	err := d.store.DecideCLIDeviceAuthorization(c.Request.Context(), p.Device, approve, user)
	switch {
	case err == nil:
		return true
	case errors.Is(err, domain.ErrNotFound):
		d.refuseConsent(c, http.StatusBadRequest, p, "not_pending", deviceMsgNotPending)
	default:
		if d.logger != nil {
			d.logger.Error("oidc: cli device: recording the decision", "error", err)
		}
		d.refuseConsent(c, http.StatusInternalServerError, p, "decide_failed", deviceMsgDecideFailed)
	}
	return false
}

// refuseConsent audits and logs a refused device consent post and answers it
// with a page.
func (d *cliDeviceDeps) refuseConsent(c *gin.Context, status int, p cliPending, reason, message string) {
	if d.logger != nil {
		d.logger.Warn("oidc: cli device consent refused", "reason", reason, "tenant", logSafe(p.Tenant), "user", logSafe(p.UserID))
	}
	recordAuth(c, d.logger, d.audit, auditCLIConsent, p, "denied", map[string]string{"reason": reason, "grant": deviceGrantType})
	renderDeviceConsent(c, status, deviceConsentPage{Message: message})
	c.Abort()
}

// clearCookie expires the device consent cookie with the attributes it was
// set with.
func (d *cliDeviceDeps) clearCookie(c *gin.Context) {
	c.SetSameSite(http.SameSiteStrictMode)
	c.SetCookie(cliDeviceConsentCookie, "", -1, cliConsentCookiePath, "", cookieSecure(d.insecure), true)
}

// The OAuth errors a device poll answers (RFC 8628 section 3.5, RFC 6749
// section 5.2), each with a detail for the person reading it.
var devicePollErrors = map[auth.DevicePollOutcome][2]string{
	auth.DevicePollPending:  {"authorization_pending", "the sign-in is waiting for its approval in the browser"},
	auth.DevicePollSlowDown: {"slow_down", "polling faster than the interval; wait five seconds more between polls"},
	auth.DevicePollExpired:  {"expired_token", "the sign-in expired; run dexaflow auth login --device again"},
	auth.DevicePollDenied:   {"access_denied", "the sign-in was canceled in the browser"},
}

// poll answers one device poll of POST /api/v2/auth/cli/token. Every device
// code that is not live (unknown, malformed, redeemed, denied and already
// answered) gets the same invalid_grant.
func (d *cliDeviceDeps) poll(c *gin.Context, deviceCode string) {
	if !spendRateBudget(c, d.pollLimiter) {
		return
	}
	if deviceCode == "" || len(deviceCode) > deviceCodeMaxLen || !isURLSafe(deviceCode) {
		d.refusePoll(c, reasonCodeMalformed)
		return
	}
	res, err := d.store.PollCLIDeviceAuthorization(c.Request.Context(), d.hash(hashDeviceCode, deviceCode))
	if err != nil {
		if d.logger != nil {
			d.logger.Error("oidc: cli device: polling", "error", err)
		}
		AbortProblem(c, http.StatusInternalServerError, "internal error", "the sign-in could not be checked; keep polling")
		return
	}
	if res.Outcome == auth.DevicePollApproved {
		d.redeem(c, res.User)
		return
	}
	if e, ok := devicePollErrors[res.Outcome]; ok {
		abortTokenError(c, e[0], e[1])
		return
	}
	d.refusePoll(c, "unknown_device_code")
}

// redeem answers the poll that redeemed an approved sign-in with a token
// minted for the user it was approved as, as /auth/token would answer.
func (d *cliDeviceDeps) redeem(c *gin.Context, user auth.User) {
	p := cliPending{Tenant: user.TenantID, UserID: user.ID, Email: user.Email}
	token, err := auth.MintUserToken(d.jwtSecret, d.tokenTTL, user)
	if err != nil {
		if d.logger != nil {
			d.logger.Error("oidc: cli device: minting token", "error", err)
		}
		recordAuth(c, d.logger, d.audit, auditCLICode, p, "denied", map[string]string{"reason": "mint_failed", "grant": deviceGrantType})
		AbortProblem(c, http.StatusInternalServerError, "internal error", "the token could not be minted; run dexaflow auth login --device again")
		return
	}
	recordAuth(c, d.logger, d.audit, auditCLICode, p, "success", map[string]string{"grant": deviceGrantType})
	c.JSON(http.StatusOK, tokenResponse{AccessToken: token, TokenType: "bearer", ExpiresIn: int(d.tokenTTL.Seconds())})
}

// refusePoll logs and answers a device code that is not live. It is tied to
// no sign-in, so it is not audited: anonymous polls must not be able to
// flood the audit trail.
func (d *cliDeviceDeps) refusePoll(c *gin.Context, reason string) {
	if d.logger != nil {
		d.logger.Warn("oidc: cli device code refused", "reason", reason, "client", logSafe(c.ClientIP()))
	}
	abortTokenError(c, "invalid_grant", cliCodeRefused)
}

// tokenProblem is a refusal of POST /api/v2/auth/cli/token: a problem
// document with the OAuth error code a client acts on.
type tokenProblem struct {
	Problem
	Error string `json:"error"`
}

// abortTokenError answers a 400 refusal of the token route with oauthErr.
func abortTokenError(c *gin.Context, oauthErr, detail string) {
	c.Set(contextKeyProblemDetail, detail)
	c.Header("Content-Type", "application/problem+json")
	c.AbortWithStatusJSON(http.StatusBadRequest, tokenProblem{
		Problem: Problem{Type: "about:blank", Title: "bad request", Status: http.StatusBadRequest, Detail: detail, Instance: c.Request.URL.Path},
		Error:   oauthErr,
	})
}

// spendRateBudget spends one request of limiter for the caller's address
// (by /64 for IPv6), or answers 429 and returns false when it is spent, as
// rateLimitByIP does for a whole route.
func spendRateBudget(c *gin.Context, limiter *auth.RateLimiter) bool {
	if !limiter.Allow(auth.AddressBucket(c.ClientIP())) {
		AbortProblem(c, http.StatusTooManyRequests, "rate limited", rateLimitedDetail)
		return false
	}
	return true
}
