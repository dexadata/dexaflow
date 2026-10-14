package client

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// The CLI sign-in routes (#1704). They get no generated method (see
// overlay.yaml): a new method on ClientInterface would break every
// implementation of it.
const (
	authMethodsPath = "/api/v2/auth/methods"
	cliTokenPath    = "/api/v2/auth/cli/token"
	// authAnswerMaxBody bounds what these calls read from an answer.
	authAnswerMaxBody = 64 << 10
)

// ErrAuthMethodsNotServed is what GetAuthMethods returns when the server
// answers 404: it predates GET /api/v2/auth/methods, and so offers only a
// password login to the CLI.
var ErrAuthMethodsNotServed = errors.New("the server does not serve " + authMethodsPath)

// ErrCLILoginCodeRefused is what ExchangeCLILoginCode returns when the server
// refuses the code (400). The server does not say why, by design: the code
// may be unknown, expired, already used, or not bound to the verifier.
var ErrCLILoginCodeRefused = errors.New("the server refused the sign-in code")

// AuthMethods are the sign-in methods GET /api/v2/auth/methods reports.
type AuthMethods struct {
	// Password is whether a password login can succeed for someone.
	Password bool `json:"password"`
	// SSO is whether the OIDC single sign-on flow is served.
	SSO bool `json:"sso"`
}

// CLITokenRequest is the body of POST /api/v2/auth/cli/token.
type CLITokenRequest struct {
	// Code is the one-time code the CLI's loopback received.
	Code string `json:"code"`
	// CodeVerifier is the PKCE verifier of the login's code_challenge.
	CodeVerifier string `json:"code_verifier"`
	// RedirectURI is the redirect_uri the login was started with.
	RedirectURI string `json:"redirect_uri"`
}

// GetAuthMethods reads GET /api/v2/auth/methods through the client's Doer,
// server URL and request editors, then reqEditors. A server from before the
// route answers 404, which returns ErrAuthMethodsNotServed.
func (c *Client) GetAuthMethods(ctx context.Context, reqEditors ...RequestEditorFn) (*AuthMethods, error) {
	resp, err := c.doAuth(ctx, http.MethodGet, authMethodsPath, nil, reqEditors)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only response
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrAuthMethodsNotServed
	default:
		return nil, fmt.Errorf("%s returned %d", authMethodsPath, resp.StatusCode)
	}
	var m AuthMethods
	if derr := json.NewDecoder(io.LimitReader(resp.Body, authAnswerMaxBody)).Decode(&m); derr != nil {
		return nil, fmt.Errorf("decoding %s: %w", authMethodsPath, derr)
	}
	return &m, nil
}

// ExchangeCLILoginCode posts a one-time sign-in code with its PKCE verifier to
// POST /api/v2/auth/cli/token and returns the token the server answers, the
// same shape POST /auth/token answers. A refused code is
// ErrCLILoginCodeRefused; any other status but 200 is an error naming it.
func (c *Client) ExchangeCLILoginCode(ctx context.Context, body CLITokenRequest, reqEditors ...RequestEditorFn) (*TokenResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the %s request: %w", cliTokenPath, err)
	}
	resp, err := c.doAuth(ctx, http.MethodPost, cliTokenPath, payload, reqEditors)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only response
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest, http.StatusUnauthorized:
		return nil, ErrCLILoginCodeRefused
	default:
		return nil, fmt.Errorf("%s returned %d", cliTokenPath, resp.StatusCode)
	}
	var tok TokenResponse
	if derr := json.NewDecoder(io.LimitReader(resp.Body, authAnswerMaxBody)).Decode(&tok); derr != nil {
		return nil, fmt.Errorf("decoding %s: %w", cliTokenPath, derr)
	}
	return &tok, nil
}

// doAuth sends one request of the sign-in routes, with a JSON body when body
// is not nil.
func (c *Client) doAuth(ctx context.Context, method, path string, body []byte, reqEditors []RequestEditorFn) (*http.Response, error) {
	var reader io.Reader = http.NoBody
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.Server, "/")+path, reader)
	if err != nil {
		return nil, fmt.Errorf("building the %s request: %w", path, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if eerr := c.applyEditors(ctx, req, reqEditors); eerr != nil {
		return nil, eerr
	}
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("calling %s: %w", path, err)
	}
	return resp, nil
}
