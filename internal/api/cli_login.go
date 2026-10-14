package api

import (
	"crypto/aes"
	"crypto/cipher"
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

	"github.com/dexadata/dexaflow/internal/oidc"
)

// Single sign-on from the CLI (#1704). The server stays the only OIDC client:
// `dexaflow auth login` opens the browser on GET /api/v2/auth/oidc/login with
// a loopback redirect_uri, its own state and the S256 challenge of a PKCE
// verifier it keeps. The browser round trip through the IdP is the UI's, the
// same callback and the same user resolution, with one difference at the end:
// instead of setting the session cookie, the callback redirects the browser
// to the CLI's loopback with a one-time code. The CLI then posts the code and
// its verifier to POST /api/v2/auth/cli/token and receives the token
// /auth/token would have answered.
//
// The code is the minted token sealed (AES-GCM) under a key derived from the
// JWT secret, together with the challenge, the redirect it was issued for and
// its expiry. It is stateless, so any replica can redeem it, and it is useless
// without the verifier, which never leaves the CLI. Each replica remembers the
// codes it redeemed until they expire; a second replica could redeem a code
// once more, but only for whoever holds the verifier, which is the CLI that
// already has the token.

// cliTokenPath is the route the CLI exchanges its one-time code on.
const cliTokenPath = "/api/v2/auth/cli/token"

// authMethodsPath is the route that says which sign-in methods a server
// offers, read by the CLI before it chooses one.
const authMethodsPath = "/api/v2/auth/methods"

// cliCodeTTL bounds how long a one-time code may wait between the callback
// and the exchange. The CLI exchanges it the moment its loopback receives it.
const cliCodeTTL = 60 * time.Second

// oidcRateLimitPerMinute is the per-IP budget the OIDC routes share: the
// browser login and callback, and the CLI code exchange.
const oidcRateLimitPerMinute = 30

// cliCodeKeyLabel separates the code's encryption key from every other key
// derived from the JWT secret.
const cliCodeKeyLabel = "dexaflow-cli-login-code"

// cliCodeRefused is the one answer every refused exchange gets, so a caller
// cannot tell an unknown code from an expired, used or mismatched one.
const cliCodeRefused = "the sign-in code is invalid or expired; run dexaflow auth login again"

// Limits on what a CLI may send. The challenge is the base64url SHA-256 of
// the verifier (RFC 7636 section 4.2), so exactly 43 characters.
const (
	pkceChallengeLen = 43
	cliStateMinLen   = 16
	cliStateMaxLen   = 128
	pkceVerifierMax  = 128
	cliCodeMaxLen    = 8 << 10
)

// errCLICode is the reason recorded for a refused exchange; it never reaches
// the caller.
var errCLICode = errors.New("cli sign-in code")

// authMethods is what GET /api/v2/auth/methods answers.
type authMethods struct {
	// Password is whether a password login can succeed for anyone: always in
	// JWT mode, and only for break-glass accounts under OIDC.
	Password bool `json:"password"`
	// SSO is whether the OIDC browser flow is served.
	SSO bool `json:"sso"`
}

// authMethodsHandler answers which sign-in methods the server offers. It is
// built from the same loginPageOpts as the sign-in page, so the CLI and the
// page cannot disagree about what is offered.
func authMethodsHandler(o loginPageOpts, oidcEnabled bool) gin.HandlerFunc {
	m := authMethods{SSO: o.sso, Password: !oidcEnabled || o.breakGlass}
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
	if err := validateLoopbackRedirect(b.Redirect); err != nil {
		return nil, err
	}
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

// validateLoopbackRedirect accepts only http://127.0.0.1:<port>/... or
// http://[::1]:<port>/... with no user info, query or fragment (RFC 8252
// section 7.3). "localhost" is refused because a name can resolve elsewhere.
// This is what keeps a link crafted by someone else from sending a victim's
// code anywhere but the victim's own machine.
func validateLoopbackRedirect(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return errors.New("redirect_uri must be http://127.0.0.1:<port>/<path>")
	}
	ip := net.ParseIP(u.Hostname())
	if ip == nil || !ip.IsLoopback() || (ip.To4() != nil && !ip.Equal(net.IPv4(127, 0, 0, 1))) {
		return errors.New("redirect_uri must point at 127.0.0.1 or [::1]")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("redirect_uri must name a port")
	}
	return nil
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

// pkceS256 is the RFC 7636 S256 transform of a verifier.
func pkceS256(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

// cliCodes seals and redeems the one-time codes, and remembers the IdP
// states of CLI logins it has already answered.
type cliCodes struct {
	aead cipher.AEAD
	now  func() time.Time
	// redeemed and states are this process's single-use records, each kept
	// until the code or the state cookie could no longer be used anyway.
	redeemed onceSet
	states   onceSet
}

// newCLICodes derives the code key from the JWT secret.
func newCLICodes(jwtSecret string) *cliCodes {
	key := sha256.Sum256([]byte(jwtSecret + "|" + cliCodeKeyLabel))
	block, err := aes.NewCipher(key[:])
	if err != nil {
		panic(fmt.Sprintf("aes with a 32-byte key: %v", err)) // unreachable: the key is always 32 bytes
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		panic(fmt.Sprintf("gcm: %v", err)) // unreachable for AES
	}
	return &cliCodes{aead: aead, now: time.Now}
}

// sealedCode is the plaintext of a one-time code.
type sealedCode struct {
	ID        string `json:"id"`
	Token     string `json:"tok"`
	Challenge string `json:"chal"`
	Redirect  string `json:"redir"`
	Expires   int64  `json:"exp"`
}

// seal returns a one-time code carrying token for the CLI described by b.
func (k *cliCodes) seal(token string, b oidc.CLIBinding) (string, error) {
	id, err := randomURLToken()
	if err != nil {
		return "", fmt.Errorf("generating code id: %w", err)
	}
	plain, err := json.Marshal(sealedCode{
		ID: id, Token: token, Challenge: b.Challenge, Redirect: b.Redirect,
		Expires: k.now().Add(cliCodeTTL).UnixNano(),
	})
	if err != nil {
		return "", fmt.Errorf("encoding code: %w", err)
	}
	nonce := make([]byte, k.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("generating code nonce: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(k.aead.Seal(nonce, nonce, plain, nil)), nil
}

// redeem returns the token a code carries when the code is authentic, unexpired,
// issued for redirect, bound to verifier, and not redeemed before on this
// replica. The returned error names the failed check for the log only.
func (k *cliCodes) redeem(code, verifier, redirect string) (string, error) {
	raw, err := base64.RawURLEncoding.DecodeString(code)
	if err != nil || len(raw) < k.aead.NonceSize() {
		return "", fmt.Errorf("%w: malformed", errCLICode)
	}
	plain, err := k.aead.Open(nil, raw[:k.aead.NonceSize()], raw[k.aead.NonceSize():], nil)
	if err != nil {
		return "", fmt.Errorf("%w: not issued by this server", errCLICode)
	}
	var sc sealedCode
	if err := json.Unmarshal(plain, &sc); err != nil {
		return "", fmt.Errorf("%w: undecodable", errCLICode)
	}
	now := k.now()
	expires := time.Unix(0, sc.Expires)
	if !now.Before(expires) {
		return "", fmt.Errorf("%w: expired", errCLICode)
	}
	if subtle.ConstantTimeCompare([]byte(pkceS256(verifier)), []byte(sc.Challenge)) != 1 {
		return "", fmt.Errorf("%w: verifier does not match the challenge", errCLICode)
	}
	if subtle.ConstantTimeCompare([]byte(redirect), []byte(sc.Redirect)) != 1 {
		return "", fmt.Errorf("%w: redirect_uri differs from the login's", errCLICode)
	}
	// Last, so a refusal for any other reason does not use the code up.
	if !k.redeemed.claim(sc.ID, expires, now) {
		return "", fmt.Errorf("%w: already redeemed", errCLICode)
	}
	return sc.Token, nil
}

// claimState records the IdP state of a CLI login as answered and reports
// whether it was not answered before.
func (k *cliCodes) claimState(state string) bool {
	now := k.now()
	return k.states.claim(state, now.Add(oidc.StateCookieTTL), now)
}

// onceSetMax caps a onceSet. Entries leave it when they expire; a full set
// refuses new entries rather than forgetting live ones.
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

// contextKeyCLILogin holds the CLI binding of the callback being answered, so
// a refusal goes back to the CLI rather than to the sign-in page.
const contextKeyCLILogin = "dexaflow.cli_login"

// ssoRefusalTarget is where a refused or failed single sign-on sends the
// browser: the sign-in page with the marker, or for a CLI login the CLI's
// loopback with an OAuth error code and the CLI's state. Neither carries the
// reason.
func ssoRefusalTarget(c *gin.Context, marker string) string {
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
// The redirect was validated to carry no query of its own.
func loopbackURL(b *oidc.CLIBinding, q url.Values) string {
	q.Set("state", b.State)
	return b.Redirect + "?" + q.Encode()
}

// cliTokenRequest is the body of POST /api/v2/auth/cli/token.
type cliTokenRequest struct {
	Code         string `json:"code"`
	CodeVerifier string `json:"code_verifier"`
	RedirectURI  string `json:"redirect_uri"`
}

// cliTokenHandler implements POST /api/v2/auth/cli/token: it redeems a
// one-time code for the token the CLI login minted, answering exactly what
// /auth/token answers. Every refusal is the same 400, whatever failed; the
// reason goes to the log, and neither the code nor the token ever does.
func cliTokenHandler(codes *cliCodes, ttlSeconds int, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req cliTokenRequest
		dec := json.NewDecoder(http.MaxBytesReader(c.Writer, c.Request.Body, cliCodeMaxLen))
		if err := dec.Decode(&req); err != nil || req.Code == "" || req.CodeVerifier == "" ||
			len(req.Code) > cliCodeMaxLen || len(req.CodeVerifier) > pkceVerifierMax {
			refuseCLICode(c, logger, fmt.Errorf("%w: malformed request", errCLICode))
			return
		}
		token, err := codes.redeem(req.Code, req.CodeVerifier, req.RedirectURI)
		if err != nil {
			refuseCLICode(c, logger, err)
			return
		}
		c.JSON(http.StatusOK, tokenResponse{AccessToken: token, TokenType: "bearer", ExpiresIn: ttlSeconds})
	}
}

// refuseCLICode logs why an exchange was refused and answers the one refusal.
func refuseCLICode(c *gin.Context, logger *slog.Logger, reason error) {
	if logger != nil {
		logger.Warn("oidc: cli sign-in code refused", "reason", reason.Error())
	}
	AbortProblem(c, http.StatusBadRequest, "bad request", cliCodeRefused)
}
