package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

// ciTokenPath is the CI OIDC exchange (ADR 0092). It gets no generated method
// (see overlay.yaml): a new method on ClientInterface would break every
// implementation of it.
const ciTokenPath = "/api/v2/auth/ciToken"

// ErrCITokenNotServed is what ExchangeCIToken returns when the server answers
// 404: it trusts no CI issuer, or predates the exchange.
var ErrCITokenNotServed = errors.New("the server does not serve " + ciTokenPath)

// CITokenRequest is the body of POST /api/v2/auth/ciToken: the OIDC token the
// CI provider minted for the job.
type CITokenRequest struct {
	Token string `json:"token"`
}

// CITokenResponse is the deploy session POST /api/v2/auth/ciToken answers:
// the token, its lifetime, and the issuer entry, tenant, role and scopes it
// acts with.
type CITokenResponse struct {
	AccessToken string    `json:"access_token"`
	TokenType   string    `json:"token_type"`
	ExpiresIn   int       `json:"expires_in"`
	ExpiresAt   time.Time `json:"expires_at"`
	Issuer      string    `json:"issuer"`
	Tenant      string    `json:"tenant"`
	Role        string    `json:"role"`
	Scopes      []string  `json:"scopes"`
}

// ExchangeCIToken posts a CI job's OIDC token to POST /api/v2/auth/ciToken
// and returns the session the server answers. A 404 is ErrCITokenNotServed;
// any other status but 200 is an error naming it and the problem detail.
func (c *Client) ExchangeCIToken(ctx context.Context, body CITokenRequest, reqEditors ...RequestEditorFn) (*CITokenResponse, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("encoding the %s request: %w", ciTokenPath, err)
	}
	resp, err := c.doAuth(ctx, http.MethodPost, ciTokenPath, payload, reqEditors)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // read-only response
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrCITokenNotServed
	default:
		if detail := problemDetail(resp.Body); detail != "" {
			return nil, fmt.Errorf("%s returned %d: %s", ciTokenPath, resp.StatusCode, detail)
		}
		return nil, fmt.Errorf("%s returned %d", ciTokenPath, resp.StatusCode)
	}
	var s CITokenResponse
	if derr := json.NewDecoder(io.LimitReader(resp.Body, authAnswerMaxBody)).Decode(&s); derr != nil {
		return nil, fmt.Errorf("decoding %s: %w", ciTokenPath, derr)
	}
	return &s, nil
}
