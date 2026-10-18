package client

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestExchangeCITokenPostsTheTokenAndDecodesTheSession: ExchangeCIToken
// posts the job's OIDC token as JSON to POST /api/v2/auth/ciToken and decodes
// the session the server answers (ADR 0092).
func TestExchangeCITokenPostsTheTokenAndDecodesTheSession(t *testing.T) {
	var got CITokenRequest
	var gotPath, gotMethod, gotType string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod, gotType = r.URL.Path, r.Method, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		_, _ = w.Write([]byte(`{"access_token":"s-1","token_type":"Bearer","expires_in":900,` +
			`"expires_at":"2026-10-18T12:00:00Z","issuer":"github-acme","tenant":"acme","role":"deployer","scopes":["dags:deploy"]}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	s, err := c.ExchangeCIToken(context.Background(), CITokenRequest{Token: "oidc-1"})
	if err != nil {
		t.Fatalf("ExchangeCIToken: %v", err)
	}
	if gotPath != "/api/v2/auth/ciToken" || gotMethod != http.MethodPost || gotType != "application/json" || got.Token != "oidc-1" {
		t.Errorf("request = %s %s (%s) %+v, want the token posted as JSON to /api/v2/auth/ciToken", gotMethod, gotPath, gotType, got)
	}
	if s.AccessToken != "s-1" || s.TokenType != "Bearer" || s.ExpiresIn != 900 || s.Issuer != "github-acme" ||
		s.Tenant != "acme" || s.Role != "deployer" || len(s.Scopes) != 1 || s.Scopes[0] != "dags:deploy" ||
		s.ExpiresAt.UTC().Format("15:04") != "12:00" {
		t.Errorf("session = %+v", s)
	}
}

// TestExchangeCITokenErrors: a server without the route is
// ErrCITokenNotServed, and a refusal names its status and problem detail.
func TestExchangeCITokenErrors(t *testing.T) {
	status, body := http.StatusNotFound, `{"status":404,"detail":"not found"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.ExchangeCIToken(context.Background(), CITokenRequest{Token: "t"}); !errors.Is(err, ErrCITokenNotServed) {
		t.Errorf("404: err = %v, want ErrCITokenNotServed", err)
	}
	status, body = http.StatusForbidden, `{"status":403,"detail":"no rule admits this job"}`
	_, err = c.ExchangeCIToken(context.Background(), CITokenRequest{Token: "t"})
	if err == nil || errors.Is(err, ErrCITokenNotServed) || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "no rule admits this job") {
		t.Errorf("403: err = %v, want the status and the detail", err)
	}
}
