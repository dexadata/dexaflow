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

// TestGetAuthMethodsReadsTheServerMethods: GetAuthMethods sends GET
// /api/v2/auth/methods through the client's Doer and decodes the answer.
func TestGetAuthMethodsReadsTheServerMethods(t *testing.T) {
	var gotPath, gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		_, _ = w.Write([]byte(`{"password":false,"sso":true}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	m, err := c.GetAuthMethods(context.Background())
	if err != nil {
		t.Fatalf("GetAuthMethods: %v", err)
	}
	if gotMethod != http.MethodGet || gotPath != "/api/v2/auth/methods" {
		t.Errorf("request = %s %s, want GET /api/v2/auth/methods", gotMethod, gotPath)
	}
	if m == nil || m.Password || !m.SSO {
		t.Errorf("methods = %+v, want sso only", m)
	}
}

// TestGetAuthMethodsOlderServer: 404 is ErrAuthMethodsNotServed, any other
// status but 200 is another error naming it.
func TestGetAuthMethodsOlderServer(t *testing.T) {
	status := http.StatusNotFound
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := c.GetAuthMethods(context.Background()); m != nil || !errors.Is(err, ErrAuthMethodsNotServed) {
		t.Errorf("404: methods=%+v err=%v, want ErrAuthMethodsNotServed", m, err)
	}
	status = http.StatusBadGateway
	if m, err := c.GetAuthMethods(context.Background()); m != nil || err == nil || errors.Is(err, ErrAuthMethodsNotServed) || !strings.Contains(err.Error(), "502") {
		t.Errorf("502: methods=%+v err=%v, want an error naming 502", m, err)
	}
}

// TestExchangeCLILoginCodePostsTheCodeAndVerifier: the exchange posts the
// JSON body the server reads and decodes the token response; a refusal is
// ErrCLILoginCodeRefused.
func TestExchangeCLILoginCodePostsTheCodeAndVerifier(t *testing.T) {
	var got CLITokenRequest
	var gotPath, gotType string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotType = r.URL.Path, r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		if status == http.StatusOK {
			_, _ = w.Write([]byte(`{"access_token":"jwt-1","token_type":"bearer","expires_in":3600}`))
		}
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	req := CLITokenRequest{Code: "c-1", CodeVerifier: "v-1", RedirectURI: "http://127.0.0.1:1/callback"}
	tok, err := c.ExchangeCLILoginCode(context.Background(), req)
	if err != nil {
		t.Fatalf("ExchangeCLILoginCode: %v", err)
	}
	if gotPath != "/api/v2/auth/cli/token" || gotType != "application/json" || got != req {
		t.Errorf("request = %s (%s) %+v, want the code posted as JSON to /api/v2/auth/cli/token", gotPath, gotType, got)
	}
	if tok == nil || tok.AccessToken == nil || *tok.AccessToken != "jwt-1" {
		t.Errorf("token = %+v, want jwt-1", tok)
	}
	status = http.StatusBadRequest
	if tok, err := c.ExchangeCLILoginCode(context.Background(), req); tok != nil || !errors.Is(err, ErrCLILoginCodeRefused) {
		t.Errorf("400: token=%+v err=%v, want ErrCLILoginCodeRefused", tok, err)
	}
	status = http.StatusTooManyRequests
	if tok, err := c.ExchangeCLILoginCode(context.Background(), req); tok != nil || err == nil || errors.Is(err, ErrCLILoginCodeRefused) || !strings.Contains(err.Error(), "429") {
		t.Errorf("429: token=%+v err=%v, want an error naming 429", tok, err)
	}
}
