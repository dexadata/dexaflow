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
	"unicode"
)

// The CLI sign-in routes (#1704). They get no generated method (see
// overlay.yaml): a new method on ClientInterface would break every
// implementation of it.
const (
	authMethodsPath = "/api/v2/auth/methods"
	cliTokenPath    = "/api/v2/auth/cli/token"
	cliDevicePath   = "/api/v2/auth/cli/device"
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
	// Device is whether the device sign-in for machines with no browser is
	// served (POST /api/v2/auth/cli/device). Servers from before it omit it.
	Device bool `json:"device"`
}

// DeviceGrantType is the grant_type of a device poll (RFC 8628).
const DeviceGrantType = "urn:ietf:params:oauth:grant-type:device_code"

// ErrDeviceLoginNotServed is what StartCLIDeviceLogin returns when the
// server answers 404: it does not serve the device sign-in.
var ErrDeviceLoginNotServed = errors.New("the server does not serve " + cliDevicePath)

// The answers of a device poll that are not a token (RFC 8628 section 3.5).
// ErrCLILoginCodeRefused is the answer for a device code that is not live.
var (
	// ErrDeviceAuthorizationPending: nobody approved the sign-in yet; poll
	// again after the interval.
	ErrDeviceAuthorizationPending = errors.New("the sign-in is waiting for its approval in the browser")
	// ErrDeviceSlowDown: the poll came too soon; add five seconds to the
	// interval.
	ErrDeviceSlowDown = errors.New("polling too fast")
	// ErrDeviceAccessDenied: the sign-in was canceled in the browser.
	ErrDeviceAccessDenied = errors.New("the sign-in was canceled in the browser")
	// ErrDeviceExpired: the sign-in expired before it was approved.
	ErrDeviceExpired = errors.New("the sign-in expired")
)

// CLIDeviceAuthorization is what POST /api/v2/auth/cli/device answers: the
// device code the CLI polls with, and what the person needs to approve it.
type CLIDeviceAuthorization struct {
	// DeviceCode is the secret the CLI polls with. Never print it.
	DeviceCode string `json:"device_code"`
	// UserCode is the short code the person enters and confirms.
	UserCode string `json:"user_code"`
	// VerificationURI is the page the person enters the code on.
	VerificationURI string `json:"verification_uri"`
	// VerificationURIComplete is that page with the code filled in.
	VerificationURIComplete string `json:"verification_uri_complete"`
	// ExpiresIn is the sign-in's lifetime in seconds.
	ExpiresIn int `json:"expires_in"`
	// Interval is the minimum number of seconds between polls.
	Interval int `json:"interval"`
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

// StartCLIDeviceLogin starts a device sign-in with POST
// /api/v2/auth/cli/device. A server without the route answers 404, which
// returns ErrDeviceLoginNotServed.
func (c *Client) StartCLIDeviceLogin(ctx context.Context, reqEditors ...RequestEditorFn) (*CLIDeviceAuthorization, error) {
	resp, err := c.doAuth(ctx, http.MethodPost, cliDevicePath, []byte("{}"), reqEditors)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only response
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrDeviceLoginNotServed
	default:
		if detail := problemDetail(resp.Body); detail != "" {
			return nil, fmt.Errorf("%s returned %d: %s", cliDevicePath, resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("%s returned %d", cliDevicePath, resp.StatusCode)
	}
	var d CLIDeviceAuthorization
	if derr := json.NewDecoder(io.LimitReader(resp.Body, authAnswerMaxBody)).Decode(&d); derr != nil {
		return nil, fmt.Errorf("decoding %s: %w", cliDevicePath, derr)
	}
	return &d, nil
}

// devicePollErrors maps the OAuth error of a refused poll to its sentinel.
var devicePollErrors = map[string]error{
	"authorization_pending": ErrDeviceAuthorizationPending,
	"slow_down":             ErrDeviceSlowDown,
	"access_denied":         ErrDeviceAccessDenied,
	"expired_token":         ErrDeviceExpired,
}

// PollCLIDeviceLogin polls a device sign-in once on POST
// /api/v2/auth/cli/token and returns the token once it was approved. A
// refusal is one of the ErrDevice sentinels, or ErrCLILoginCodeRefused for a
// device code that is not live; a 429 is ErrDeviceSlowDown too.
func (c *Client) PollCLIDeviceLogin(ctx context.Context, deviceCode string, reqEditors ...RequestEditorFn) (*TokenResponse, error) {
	payload, err := json.Marshal(map[string]string{"grant_type": DeviceGrantType, "device_code": deviceCode})
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
	case http.StatusTooManyRequests:
		return nil, ErrDeviceSlowDown
	case http.StatusBadRequest:
		var refusal struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, authAnswerMaxBody)).Decode(&refusal) //nolint:errcheck // an unreadable refusal is a refused code
		if sentinel, ok := devicePollErrors[refusal.Error]; ok {
			return nil, sentinel
		}
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

// problemDetailMax bounds the detail of a problem document quoted in an
// error.
const problemDetailMax = 200

// problemDetail is the detail of the problem document in body (RFC 9457),
// bounded and stripped of control characters, or "" when there is none.
func problemDetail(body io.Reader) string {
	var p struct {
		Detail string `json:"detail"`
	}
	if json.NewDecoder(io.LimitReader(body, authAnswerMaxBody)).Decode(&p) != nil {
		return ""
	}
	detail := strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, p.Detail)
	if len(detail) > problemDetailMax {
		detail = strings.ToValidUTF8(detail[:problemDetailMax], "")
	}
	return detail
}
