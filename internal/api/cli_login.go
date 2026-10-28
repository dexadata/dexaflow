package api

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/oidc"
)

// Single sign-on from the CLI (#1704, ADR 0079). The server stays the only
// OIDC client: `dexaflow auth login` opens the browser on GET
// /api/v2/auth/oidc/login with a loopback redirect_uri, its own state and the
// S256 challenge of a PKCE verifier it keeps. The browser round trip through
// the IdP is the UI's, the same callback and the same user resolution. Then,
// instead of setting the session cookie, the callback shows a consent page
// (cli_consent.go): only a click on it, a same-origin POST carrying the CSRF
// token bound to the consent cookie, sends the browser to the CLI's loopback
// with a one-time code. The CLI posts the code and its verifier to POST
// /api/v2/auth/cli/token and receives the token /auth/token would have
// answered.
//
// Why the click matters: whoever starts a login picks the challenge, and so
// holds the verifier. A page can navigate a victim's browser to a CLI login
// with its own challenge, and an IdP that signs the victim in silently would
// otherwise deliver the victim's code to the victim's loopback with no
// interaction at all. Anything that can read that port or the request URL
// (another local listener, a local service that logs URLs) would then hold
// the code, and the code with the attacker's verifier is the victim's token.
// The consent page names the account and the port, and nothing reaches the
// loopback until the person approves (RFC 8252 section 8.6).
//
// The code is the minted token sealed (AES-GCM) under a key derived with HKDF
// from the JWT secret, together with the challenge, the redirect it was issued
// for and its expiry. It is stateless, so any replica can redeem it, and it is
// useless without the verifier. Each replica remembers the codes it redeemed
// until they expire; a second replica could redeem a code once more, but only
// for whoever holds the verifier. Rotating the JWT secret invalidates every
// outstanding code and consent, which costs at most one retried login.

// cliTokenPath is the route the CLI exchanges its one-time code on.
const cliTokenPath = "/api/v2/auth/cli/token"

// authMethodsPath is the route that says which sign-in methods a server
// offers, read by the CLI before it chooses one.
const authMethodsPath = "/api/v2/auth/methods"

// cliCodeTTL bounds how long a one-time code may wait between the approval
// and the exchange. The CLI exchanges it the moment its loopback receives it.
const cliCodeTTL = 60 * time.Second

// oidcRateLimitPerMinute is the per-IP budget the OIDC routes share: the
// browser login and callback, the CLI consent and the CLI code exchange.
const oidcRateLimitPerMinute = 30

// cliKeyInfo is the HKDF info of the key that seals codes and consents, so it
// is never the key of anything else derived from the JWT secret.
const cliKeyInfo = "dexaflow cli login sealing key"

// The additional data each sealed value is bound to. A consent can never be
// redeemed as a code, nor a code posted as a consent, and a later format gets
// a new version.
const (
	cliCodeAAD    = "dexaflow cli code v1"
	cliConsentAAD = "dexaflow cli consent v1"
)

// Audit actions of the CLI sign-in. The login itself is audited as
// oidc.login.success with client=cli when the user is verified.
const (
	auditCLIConsent = "oidc.cli_consent"
	auditCLICode    = "oidc.cli_code"
)

// cliCodeRefused is the one answer every refused exchange gets, so a caller
// cannot tell an unknown code from an expired, used or mismatched one.
const cliCodeRefused = "the sign-in code is invalid or expired; run dexaflow auth login again"

// Limits on what a CLI may send. The challenge is the base64url SHA-256 of
// the verifier (RFC 7636 section 4.2), so exactly 43 characters, and the
// verifier is 43 to 128 unreserved characters (section 4.1).
const (
	pkceChallengeLen = 43
	pkceVerifierMin  = 43
	pkceVerifierMax  = 128
	cliStateMinLen   = 16
	cliStateMaxLen   = 128
	cliCodeMaxLen    = 8 << 10
)

// Stable reasons a code is refused, for the audit row and the log.
const (
	reasonCodeMalformed        = "malformed_request"
	reasonCodeNotAuthentic     = "not_authentic"
	reasonCodeExpired          = "expired"
	reasonCodeVerifierMismatch = "verifier_mismatch"
	reasonCodeRedirectMismatch = "redirect_mismatch"
	reasonCodeRedeemed         = "already_redeemed"
)

// authMethods is what GET /api/v2/auth/methods answers.
type authMethods struct {
	// Password is whether a password login can succeed for anyone: always in
	// JWT mode, and only for break-glass accounts under OIDC.
	Password bool `json:"password"`
	// SSO is whether the OIDC browser flow is served.
	SSO bool `json:"sso"`
	// Device is whether the device sign-in of the CLI (ADR 0081) is served,
	// for a machine with no browser.
	Device bool `json:"device"`
}

// authMethodsHandler answers which sign-in methods the server offers. It is
// built from the same loginPageOpts as the sign-in page, so the CLI and the
// page cannot disagree about what is offered.
func authMethodsHandler(o loginPageOpts, oidcEnabled, device bool) gin.HandlerFunc {
	m := authMethods{SSO: o.sso, Password: !oidcEnabled || o.breakGlass, Device: o.sso && device}
	return func(c *gin.Context) { c.JSON(http.StatusOK, m) }
}

// isCLILogin reports whether a login request was started by the CLI: only
// the CLI sends a redirect_uri.
func isCLILogin(c *gin.Context) bool {
	_, present := c.GetQuery("redirect_uri")
	return present
}

// parseCLIBinding reads the CLI parameters of a login request, with an error
// when they are not acceptable.
func parseCLIBinding(c *gin.Context) (*oidc.CLIBinding, error) {
	b := &oidc.CLIBinding{
		Redirect:  c.Query("redirect_uri"),
		State:     c.Query("state"),
		Challenge: c.Query("code_challenge"),
	}
	canonical, err := canonicalLoopback(b.Redirect)
	if err != nil {
		return nil, err
	}
	b.Redirect = canonical
	if c.Query("code_challenge_method") != "S256" {
		return nil, errors.New("code_challenge_method must be S256")
	}
	if len(b.Challenge) != pkceChallengeLen || !isURLSafe(b.Challenge) {
		return nil, errors.New("code_challenge must be the base64url SHA-256 of the verifier")
	}
	if len(b.State) < cliStateMinLen || len(b.State) > cliStateMaxLen || !isURLSafe(b.State) {
		return nil, fmt.Errorf("state must be %d to %d URL-safe characters", cliStateMinLen, cliStateMaxLen)
	}
	return b, nil
}

// loopbackPathChars are the characters a loopback redirect path may use. The
// CLI only ever sends /callback; dots, escapes and everything else are
// refused so no two spellings can name the same redirect.
func isLoopbackPath(p string) bool {
	if p == "" || p[0] != '/' {
		return false
	}
	for _, r := range p {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '/', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// canonicalLoopback accepts only http://127.0.0.1:<port>/<path> or
// http://[::1]:<port>/<path> (RFC 8252 section 7.3) and returns it rebuilt
// from its parsed parts. Anything that does not rebuild to itself is refused:
// user info, a query or a bare "?", a fragment or a bare "#", an escaped or
// dotted path, another case of the scheme, a port with a leading zero, and
// IPv4-mapped IPv6. "localhost" is refused because a name can resolve
// elsewhere.
//
// This keeps the code on the machine whose browser approved the sign-in. It
// does not make the code harmless there: any local process that can read the
// port or the request URL could take it, which is why nothing reaches the
// loopback without the consent click (cli_consent.go).
func canonicalLoopback(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.Opaque != "" {
		return "", errors.New("redirect_uri must be http://127.0.0.1:<port>/<path>")
	}
	host := u.Hostname()
	if host != "127.0.0.1" && host != "::1" {
		return "", errors.New("redirect_uri must point at 127.0.0.1 or [::1]")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return "", errors.New("redirect_uri must name a port")
	}
	if !isLoopbackPath(u.Path) {
		return "", errors.New("redirect_uri path must be / and letters, digits, - or _")
	}
	canonical := "http://" + net.JoinHostPort(host, strconv.Itoa(port)) + u.Path
	if canonical != raw {
		return "", errors.New("redirect_uri must be written canonically, with no query or fragment")
	}
	return canonical, nil
}

// isURLSafe reports whether s holds only base64url characters.
func isURLSafe(s string) bool {
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// isPKCEVerifier reports whether v is a well-formed RFC 7636 verifier: 43 to
// 128 characters of ALPHA, DIGIT, "-", ".", "_" and "~".
func isPKCEVerifier(v string) bool {
	if len(v) < pkceVerifierMin || len(v) > pkceVerifierMax {
		return false
	}
	for _, r := range v {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.', r == '_', r == '~':
		default:
			return false
		}
	}
	return true
}

// pkceS256 is the RFC 7636 S256 transform of a verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// cliPending is the plaintext of a sealed consent and of a sealed code: the
// minted token, who it is for, and the CLI it goes to. A code carries no
// CSRF token and no CLI state.
type cliPending struct {
	ID           string `json:"id"`
	Token        string `json:"tok"`
	TokenExpires int64  `json:"tokexp"`
	Redirect     string `json:"redir"`
	State        string `json:"state,omitempty"`
	Challenge    string `json:"chal"`
	CSRF         string `json:"csrf,omitempty"`
	Tenant       string `json:"tenant"`
	UserID       string `json:"user"`
	Email        string `json:"email"`
	Expires      int64  `json:"exp"`
	// Device, UserCode and Roles are set on the consent of a device sign-in
	// (cli_device.go), which holds no token: the token is minted when the
	// CLI redeems the approved sign-in, with these roles.
	Device   string   `json:"dev,omitempty"`
	UserCode string   `json:"ucode,omitempty"`
	Roles    []string `json:"roles,omitempty"`
}

// binding is the CLI side of a pending sign-in.
func (p cliPending) binding() *oidc.CLIBinding {
	return &oidc.CLIBinding{Redirect: p.Redirect, State: p.State, Challenge: p.Challenge}
}

// cliCodes seals and opens consents and codes, and keeps this process's
// single-use records of them and of the IdP states of CLI logins.
type cliCodes struct {
	aead     cipher.AEAD
	now      func() time.Time
	redeemed onceSet
	consents onceSet
	states   onceSet
}

// newCLICodes derives the sealing key from the JWT secret with HKDF.
func newCLICodes(jwtSecret string) *cliCodes {
	key, err := hkdf.Key(sha256.New, []byte(jwtSecret), nil, cliKeyInfo, 32)
	if err != nil {
		panic(fmt.Sprintf("hkdf of a 32-byte key: %v", err)) // unreachable: 32 bytes is within the limit
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		panic(fmt.Sprintf("aes with a 32-byte key: %v", err)) // unreachable: the key is always 32 bytes
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("gcm: %v", err)) // unreachable for AES
	}
	return &cliCodes{aead: aead, now: time.Now}
}

// seal encrypts p bound to aad, with a fresh id and an expiry ttl from now.
func (k *cliCodes) seal(p cliPending, aad string, ttl time.Duration) (string, error) {
	id, err := randomURLToken()
	if err != nil {
		return "", fmt.Errorf("generating id: %w", err)
	}
	p.ID, p.Expires = id, k.now().Add(ttl).UnixNano()
	plain, err := json.Marshal(p)
	if err != nil {
		return "", fmt.Errorf("encoding: %w", err)
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(k.aead.Seal(nonce, nonce, plain, []byte(aad))), nil
}

// open decrypts a value sealed for aad. ok is false for anything this server
// did not seal for that purpose.
func (k *cliCodes) open(sealed, aad string) (cliPending, bool) {
	raw, err := base64.RawURLEncoding.DecodeString(sealed)
	if err != nil || len(raw) < k.aead.NonceSize() {
		return cliPending{}, false
	}
	plain, err := k.aead.Open(nil, raw[:k.aead.NonceSize()], raw[k.aead.NonceSize():], []byte(aad))
	if err != nil {
		return cliPending{}, false
	}
	var p cliPending
	if err := json.Unmarshal(plain, &p); err != nil {
		return cliPending{}, false
	}
	return p, true
}

// sealCode returns the one-time code for an approved sign-in.
func (k *cliCodes) sealCode(p cliPending) (string, error) {
	p.CSRF, p.State = "", ""
	return k.seal(p, cliCodeAAD, cliCodeTTL)
}

// redeem checks a code against the verifier and redirect the CLI presents.
// It returns the code's contents whenever the code is authentic, so a refusal
// can be audited against its user, and a stable reason when it refuses.
func (k *cliCodes) redeem(code, verifier, redirect string) (p cliPending, authentic bool, reason string) {
	p, ok := k.open(code, cliCodeAAD)
	if !ok {
		return cliPending{}, false, reasonCodeNotAuthentic
	}
	now := k.now()
	expires := time.Unix(0, p.Expires)
	switch {
	case !now.Before(expires):
		return p, true, reasonCodeExpired
	case subtle.ConstantTimeCompare([]byte(pkceS256(verifier)), []byte(p.Challenge)) != 1:
		return p, true, reasonCodeVerifierMismatch
	case subtle.ConstantTimeCompare([]byte(redirect), []byte(p.Redirect)) != 1:
		return p, true, reasonCodeRedirectMismatch
	}
	// Last, so a refusal for any other reason does not use the code up.
	if !k.redeemed.claim(p.ID, expires, now) {
		return p, true, reasonCodeRedeemed
	}
	return p, true, ""
}

// claimState records the IdP state of a CLI login as answered and reports
// whether it was not answered before. The callback claims it only once the
// IdP's code was exchanged and the ID token verified, so a state is used up
// only by a sign-in that actually happened.
func (k *cliCodes) claimState(state string) bool {
	now := k.now()
	return k.states.claim(state, now.Add(oidc.StateCookieTTL), now)
}

// onceSetMax caps a onceSet. Entries leave it when they expire; a full set
// refuses new entries rather than forgetting live ones. Every entry is a
// verified sign-in, so filling it takes 100k of them within their lifetime.
const onceSetMax = 100_000

// onceSet remembers ids until they expire, so each is accepted once per
// process.
type onceSet struct {
	mu  sync.Mutex
	exp map[string]time.Time
}

// claim records id until exp and reports whether it was not already there.
func (u *onceSet) claim(id string, exp, now time.Time) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.exp == nil {
		u.exp = make(map[string]time.Time)
	}
	if until, seen := u.exp[id]; seen && now.Before(until) {
		return false
	}
	if len(u.exp) >= onceSetMax {
		for k, until := range u.exp {
			if !now.Before(until) {
				delete(u.exp, k)
			}
		}
		if len(u.exp) >= onceSetMax {
			return false
		}
	}
	u.exp[id] = exp
	return true
}

// tokenExpiry reads the exp of a token this server just minted, without
// verifying it again, falling back to fallback.
func tokenExpiry(token string, fallback time.Time) int64 {
	var claims jwt.RegisteredClaims
	if _, _, err := jwt.NewParser().ParseUnverified(token, &claims); err != nil || claims.ExpiresAt == nil {
		return fallback.Unix()
	}
	return claims.ExpiresAt.Unix()
}

// contextKeyCLILogin holds the CLI binding of the callback being answered, so
// a refusal goes back to the CLI rather than to the sign-in page.
const contextKeyCLILogin = "dexaflow.cli_login"

// contextKeyDeviceLogin marks the callback of a device sign-in, so a refusal
// goes back to the device page rather than to the sign-in page.
const contextKeyDeviceLogin = "dexaflow.cli_device_login"

// ssoRefusalTarget is where a refused or failed single sign-on sends the
// browser: the sign-in page with the marker, for a device sign-in the device
// page with the marker, or for a CLI login the CLI's loopback with an OAuth
// error code and the CLI's state. None carries the reason.
func ssoRefusalTarget(c *gin.Context, marker string) string {
	if _, ok := c.Get(contextKeyDeviceLogin); ok {
		return deviceVerifyPath + "?" + ssoErrorParam + "=" + marker
	}
	if v, ok := c.Get(contextKeyCLILogin); ok {
		if b, ok := v.(*oidc.CLIBinding); ok && b != nil {
			code := "access_denied"
			if marker == ssoErrorServer {
				code = "server_error"
			}
			return loopbackURL(b, url.Values{"error": {code}})
		}
	}
	if marker == ssoErrorServer {
		return loginPageWithSSOServerFailure
	}
	return loginPageWithSSOError
}

// loopbackURL is the CLI's redirect with q and the CLI's state as its query.
// The redirect was checked to be canonical, with no query of its own.
func loopbackURL(b *oidc.CLIBinding, q url.Values) string {
	q.Set("state", b.State)
	return b.Redirect + "?" + q.Encode()
}

// cliTokenRequest is the body of POST /api/v2/auth/cli/token: a one-time
// code with its verifier and redirect, or, with grant_type set to the device
// grant, a device code to poll.
type cliTokenRequest struct {
	GrantType    string `json:"grant_type"`
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
	DeviceCode   string `json:"device_code"`
}

// cliTokenDeps is what the token route needs.
type cliTokenDeps struct {
	codes  *cliCodes
	logger *slog.Logger
	audit  AuthAuditWriter
	// limiter is the budget of the code exchange, shared with the OIDC
	// routes.
	limiter *auth.RateLimiter
	// device serves the device grant, with its own poll budget; nil when the
	// device sign-in is not served.
	device *cliDeviceDeps
}

// cliTokenHandler implements POST /api/v2/auth/cli/token: it redeems a
// one-time code for the token the CLI login minted, answering what
// /auth/token answers, with expires_in the token's remaining lifetime. Every
// refusal is the same 400 (error invalid_grant), whatever failed. Each
// outcome is audited, against the code's tenant and user when the code is
// authentic; the log and the audit row carry the reason, never the code or
// the token. A body with the device grant_type is a device poll instead
// (cli_device.go); another grant_type is unsupported_grant_type.
func cliTokenHandler(d cliTokenDeps) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req cliTokenRequest
		dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, cliCodeMaxLen))
		derr := dec.Decode(&req)
		if derr == nil && req.GrantType == deviceGrantType && d.device != nil {
			d.device.poll(c, req.DeviceCode)
			return
		}
		if !spendRateBudget(c, d.limiter) {
			return
		}
		if derr == nil && req.GrantType != "" && req.GrantType != "authorization_code" {
			abortTokenError(c, "unsupported_grant_type", "this server does not serve that grant_type")
			return
		}
		if derr != nil || req.Code == "" || len(req.Code) > cliCodeMaxLen || !isPKCEVerifier(req.CodeVerifier) {
			refuseCLICode(c, d.logger, d.audit, cliPending{}, reasonCodeMalformed)
			return
		}
		p, _, reason := d.codes.redeem(req.Code, req.CodeVerifier, req.RedirectURI)
		if reason != "" {
			refuseCLICode(c, d.logger, d.audit, p, reason)
			return
		}
		recordAuth(c, d.logger, d.audit, auditCLICode, p, "success", nil)
		remaining := max(p.TokenExpires-d.codes.now().Unix(), 0)
		c.JSON(http.StatusOK, tokenResponse{AccessToken: p.Token, TokenType: "bearer", ExpiresIn: int(remaining)})
	}
}

// refuseCLICode audits and logs why an exchange was refused and answers the
// one refusal.
func refuseCLICode(c *gin.Context, logger *slog.Logger, audit AuthAuditWriter, p cliPending, reason string) {
	if logger != nil {
		logger.Warn("oidc: cli sign-in code refused", "reason", reason, "tenant", logSafe(p.Tenant), "user", logSafe(p.UserID))
	}
	recordAuth(c, logger, audit, auditCLICode, p, "denied", map[string]string{"reason": reason})
	abortTokenError(c, "invalid_grant", cliCodeRefused)
}

// recordAuth writes one audit row for a CLI sign-in step, best-effort like
// every other auth audit: a sink error is logged and never changes the
// answer.
func recordAuth(c *gin.Context, logger *slog.Logger, audit AuthAuditWriter, action string, p cliPending, outcome string, extra map[string]string) {
	if audit == nil {
		return
	}
	if extra == nil {
		extra = map[string]string{}
	}
	extra["client"] = "cli"
	if err := audit.RecordAuthEvent(c.Request.Context(), p.Tenant, p.UserID, action, p.Email, outcome, extra); err != nil && logger != nil {
		logger.Error("oidc: recording audit event", "action", action, "error", err)
	}
}
