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

// TestStartCLIDeviceLoginPostsAndDecodes: the start posts to
// /api/v2/auth/cli/device and decodes the RFC 8628 answer; a server without
// the route is ErrDeviceLoginNotServed.
func TestStartCLIDeviceLoginPostsAndDecodes(t *testing.T) {
	var gotPath, gotMethod string
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotMethod = r.URL.Path, r.Method
		if status != http.StatusOK {
			w.Header().Set("Content-Type", "application/problem+json")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(`{"title":"service unavailable","status":503,"detail":"too many device sign-ins are in progress; try again in a few minutes"}`))
			return
		}
		_, _ = w.Write([]byte(`{"device_code":"dc","user_code":"BCDF-GHJK","verification_uri":"https://x/api/v2/auth/device",` +
			`"verification_uri_complete":"https://x/api/v2/auth/device?user_code=BCDF-GHJK","expires_in":600,"interval":5}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	d, err := c.StartCLIDeviceLogin(context.Background())
	if err != nil {
		t.Fatalf("StartCLIDeviceLogin: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/auth/cli/device" {
		t.Errorf("request = %s %s, want POST /api/v2/auth/cli/device", gotMethod, gotPath)
	}
	if d.DeviceCode != "dc" || d.UserCode != "BCDF-GHJK" || d.VerificationURI != "https://x/api/v2/auth/device" ||
		d.VerificationURIComplete == "" || d.ExpiresIn != 600 || d.Interval != 5 {
		t.Errorf("authorization = %+v", d)
	}
	status = http.StatusNotFound
	if _, err := c.StartCLIDeviceLogin(context.Background()); !errors.Is(err, ErrDeviceLoginNotServed) {
		t.Errorf("404 err = %v, want ErrDeviceLoginNotServed", err)
	}
	status = http.StatusServiceUnavailable
	if _, err := c.StartCLIDeviceLogin(context.Background()); err == nil || !strings.Contains(err.Error(), "503") ||
		!strings.Contains(err.Error(), "try again in a few minutes") {
		t.Errorf("503 err = %v, want an error naming 503 and the server's detail", err)
	}
}

// TestPollCLIDeviceLoginMapsTheOAuthErrors: a poll posts the device grant
// and maps each RFC 8628 error to its sentinel; the token comes back on 200.
func TestPollCLIDeviceLoginMapsTheOAuthErrors(t *testing.T) {
	var answer string
	status := http.StatusBadRequest
	var got map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewDecoder(r.Body).Decode(&got)
		w.WriteHeader(status)
		_, _ = w.Write([]byte(answer))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	for oauthErr, want := range map[string]error{
		"authorization_pending": ErrDeviceAuthorizationPending,
		"slow_down":             ErrDeviceSlowDown,
		"access_denied":         ErrDeviceAccessDenied,
		"expired_token":         ErrDeviceExpired,
		"invalid_grant":         ErrCLILoginCodeRefused,
	} {
		answer = `{"type":"about:blank","status":400,"error":"` + oauthErr + `"}`
		if _, perr := c.PollCLIDeviceLogin(context.Background(), "dc"); !errors.Is(perr, want) {
			t.Errorf("%s: err = %v, want %v", oauthErr, perr, want)
		}
	}
	if got["grant_type"] != DeviceGrantType || got["device_code"] != "dc" {
		t.Errorf("body = %v, want the device grant and the device code", got)
	}
	status = http.StatusTooManyRequests
	if _, perr := c.PollCLIDeviceLogin(context.Background(), "dc"); !errors.Is(perr, ErrDeviceSlowDown) {
		t.Errorf("429: err = %v, want ErrDeviceSlowDown", perr)
	}
	status, answer = http.StatusOK, `{"access_token":"tok","token_type":"bearer","expires_in":3600}`
	tok, err := c.PollCLIDeviceLogin(context.Background(), "dc")
	if err != nil || tok.AccessToken == nil || *tok.AccessToken != "tok" {
		t.Fatalf("200: token=%+v err=%v", tok, err)
	}
	status, answer = http.StatusInternalServerError, ``
	if _, perr := c.PollCLIDeviceLogin(context.Background(), "dc"); perr == nil || !strings.Contains(perr.Error(), "500") {
		t.Errorf("500: err = %v, want an error naming 500", perr)
	}
}

// TestGetAuthMethodsReadsTheDeviceMethod: the device flag is decoded.
func TestGetAuthMethodsReadsTheDeviceMethod(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"password":false,"sso":true,"device":true}`))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if m, err := c.GetAuthMethods(context.Background()); err != nil || !m.Device {
		t.Fatalf("methods = %+v, %v; want device", m, err)
	}
}
