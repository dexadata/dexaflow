// Package oidc implements the OIDC/SSO login flow: the Authorization Code +
// PKCE redirect, keyless ID-token verification against the issuer's public
// JWKS, fail-closed tenant pinning, and group→role mapping. It ends by handing
// the caller a verified identity; minting the app's own session token is the
// caller's job (the JWT authenticator stays the request-path verifier).
package oidc

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// stateIssuer marks the short-lived state cookie so it can never be confused
// with the app's own session token (which uses a different issuer and a
// different key).
const stateIssuer = "leoflow-oidc-state"

// ErrInvalidState is returned when the state cookie is missing, malformed,
// expired, or not signed by us — every case is a failed CSRF/replay check.
var ErrInvalidState = errors.New("invalid oidc state")

// StatePayload is the login-flow state carried statelessly in a signed,
// HttpOnly cookie between the login redirect and the callback: the CSRF
// `state`, the ID-token `nonce`, the PKCE `code_verifier`, and the sanitized
// post-login redirect target. No server-side session store is needed.
type StatePayload struct {
	State    string
	Nonce    string
	Verifier string
	Next     string
	// CLI is set when the login was started by `dexaflow auth login` rather
	// than by the browser's own sign-in: the callback then hands the CLI a
	// one-time code on its loopback redirect instead of setting a session
	// cookie. Nil for a browser login.
	CLI *CLIBinding
	// Device is set when the login is the first step of a device sign-in of
	// the CLI (`dexaflow auth login --device`, ADR 0081): the callback then
	// asks for the user code instead of setting a session cookie. Nil for
	// any other login.
	Device *DeviceBinding
}

// DeviceBinding is what a device sign-in carries through the IdP round trip,
// signed with the rest of the state: the user code of the link the person
// opened (verification_uri_complete), well formed but not looked up, to fill
// in on the page that asks for the code after the sign-in. It is "" when the
// person opened the bare page.
type DeviceBinding struct {
	UserCode string `json:"user_code,omitempty"`
}

// CLIBinding is what a CLI-started login carries through the IdP round trip,
// signed with the rest of the state so the callback can trust it: where the
// CLI listens, the CLI's own state to echo back, and the S256 challenge of the
// PKCE verifier only the CLI holds, which the one-time code is bound to.
type CLIBinding struct {
	Redirect  string `json:"redirect"`
	State     string `json:"state"`
	Challenge string `json:"challenge"`
}

// StateCodec signs and verifies the state cookie with a key derived from the
// app's HS256 secret. Deriving a distinct key (rather than reusing the secret
// directly) keeps a state cookie from ever validating as a session token and
// vice versa.
type StateCodec struct {
	key []byte
}

// NewStateCodec builds a StateCodec from the app's HS256 secret, deriving a
// dedicated signing key so state cookies and session tokens never cross-verify.
func NewStateCodec(hs256Secret string) *StateCodec {
	sum := sha256.Sum256([]byte(hs256Secret + "|" + stateIssuer))
	return &StateCodec{key: sum[:]}
}

// stateClaims is the signed cookie payload.
type stateClaims struct {
	Nonce    string         `json:"nonce"`
	Verifier string         `json:"vfy"`
	Next     string         `json:"next"`
	CLI      *CLIBinding    `json:"cli,omitempty"`
	Device   *DeviceBinding `json:"dev,omitempty"`
	jwt.RegisteredClaims
}

// Encode signs the payload into a compact token valid for ttl. The CSRF `state`
// travels as the JWT ID (jti), so verifying the callback's state query param is
// a direct comparison against the signed cookie.
func (c *StateCodec) Encode(p StatePayload, ttl time.Duration) (string, error) {
	now := time.Now()
	claims := stateClaims{
		Nonce:    p.Nonce,
		Verifier: p.Verifier,
		Next:     p.Next,
		CLI:      p.CLI,
		Device:   p.Device,
		RegisteredClaims: jwt.RegisteredClaims{
			ID:        p.State,
			Issuer:    stateIssuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	signed, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(c.key)
	if err != nil {
		return "", fmt.Errorf("signing oidc state: %w", err)
	}
	return signed, nil
}

// Decode verifies the cookie's signature and expiry and returns its payload.
// Any failure — bad signature, wrong issuer, expired, malformed — is
// ErrInvalidState, so the callback fails closed on a tampered or stale cookie.
func (c *StateCodec) Decode(token string) (StatePayload, error) {
	var claims stateClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		return c.key, nil
	}, jwt.WithIssuer(stateIssuer), jwt.WithValidMethods([]string{"HS256"}))
	if err != nil || !parsed.Valid {
		return StatePayload{}, ErrInvalidState
	}
	return StatePayload{
		State:    claims.ID,
		Nonce:    claims.Nonce,
		Verifier: claims.Verifier,
		Next:     claims.Next,
		CLI:      claims.CLI,
		Device:   claims.Device,
	}, nil
}
