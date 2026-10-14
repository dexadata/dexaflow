package client

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// New builds a typed /api/v2 client for the control plane at baseURL (an origin
// such as "http://localhost:8080", with or without a trailing slash). When token
// is non-empty every request carries "Authorization: Bearer <token>"; the MCP and
// CLI pass the caller's JWT through unchanged and never mint one (ADR 0050 D9). An
// empty token leaves requests unauthenticated, for dev/loopback use. Extra
// ClientOptions (e.g. WithHTTPClient) are applied after the auth editor.
func New(baseURL, token string, opts ...ClientOption) (*ClientWithResponses, error) {
	baseURL = strings.TrimRight(baseURL, "/")
	all := make([]ClientOption, 0, len(opts)+1)
	if token != "" {
		all = append(all, WithRequestEditorFn(bearerAuth(token)))
	}
	all = append(all, opts...)
	c, err := NewClientWithResponses(baseURL, all...)
	if err != nil {
		return nil, fmt.Errorf("building /api/v2 client for %q: %w", baseURL, err)
	}
	return c, nil
}

// bearerAuth returns a request editor that sets the bearer Authorization header.
func bearerAuth(token string) RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		req.Header.Set("Authorization", "Bearer "+token)
		return nil
	}
}

// WithDagSourceVersion returns a request editor that asks GET
// /api/v2/dagSources/{dag_id} for the source of the DAG version numbered n
// (its version_number in the dagVersions listing) instead of the newest one:
//
//	c.GetDagSourceWithResponse(ctx, dagID, client.WithDagSourceVersion(n))
//
// The generated GetDagSource does not take the parameter itself, so that its
// signature stays compatible (see overlay.yaml).
func WithDagSourceVersion(n int) RequestEditorFn {
	return func(_ context.Context, req *http.Request) error {
		q := req.URL.Query()
		q.Set("version_number", strconv.Itoa(n))
		req.URL.RawQuery = q.Encode()
		return nil
	}
}
