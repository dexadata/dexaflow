package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// ciTokenServer is a control plane that serves the CI OIDC exchange: it
// records what each request sent and answers status with body.
type ciTokenServer struct {
	*httptest.Server
	mu       sync.Mutex
	tokens   []string
	bearers  []string
	status   int
	response string
}

func newCITokenServer(t *testing.T, status int, response string) *ciTokenServer {
	t.Helper()
	s := &ciTokenServer{status: status, response: response}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v2/auth/ciToken" {
			http.NotFound(w, r)
			return
		}
		var body struct {
			Token string `json:"token"`
		}
		data, _ := io.ReadAll(r.Body)   //nolint:errcheck // test server
		_ = json.Unmarshal(data, &body) //nolint:errcheck // test server
		s.mu.Lock()
		s.tokens = append(s.tokens, body.Token)
		s.bearers = append(s.bearers, r.Header.Get("Authorization"))
		s.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(s.status)
		_, _ = io.WriteString(w, s.response) //nolint:errcheck // test server
	}))
	t.Cleanup(s.Close)
	return s
}

const ciTokenIssued = `{"access_token":"dxci_session","token_type":"Bearer","expires_in":3600,` +
	`"expires_at":"2026-10-18T12:00:00Z","issuer":"github-acme","tenant":"acme","role":"deployer","scopes":["dexaflow:deploy"]}`

// TestAuthCITokenExchangesTheJobToken posts DEXAFLOW_OIDC_TOKEN to the
// resolved server and prints the session alone on stdout, for
// DEXAFLOW_TOKEN="$(dexaflow auth ci-token)". No bearer goes with it: the
// job's token is the credential, and a DEXAFLOW_TOKEN of the job is not
// sent along.
func TestAuthCITokenExchangesTheJobToken(t *testing.T) {
	isolateCLIEnv(t)
	srv := newCITokenServer(t, http.StatusOK, ciTokenIssued)
	t.Setenv("DEXAFLOW_SERVER_URL", srv.URL)
	t.Setenv("DEXAFLOW_OIDC_TOKEN", "job-oidc-token")
	t.Setenv("DEXAFLOW_TOKEN", "dxf_stored")

	out, stderr, err := run(t, "auth", "ci-token", "--config", emptyConfig(t))

	if err != nil {
		t.Fatalf("ci-token: %v\n%s", err, stderr)
	}
	if out != "dxci_session\n" {
		t.Errorf("stdout = %q, want the session alone", out)
	}
	if len(srv.tokens) != 1 || srv.tokens[0] != "job-oidc-token" {
		t.Errorf("posted tokens = %q, want the job's", srv.tokens)
	}
	if len(srv.bearers) > 0 && srv.bearers[0] != "" {
		t.Errorf("Authorization = %q, want none", srv.bearers[0])
	}
	if !strings.Contains(stderr, "deployer") || !strings.Contains(stderr, "acme") {
		t.Errorf("stderr = %q, want the role and tenant of the session", stderr)
	}
	if strings.Contains(stderr, "job-oidc-token") || strings.Contains(stderr, "dxci_session") {
		t.Errorf("stderr = %q carries a token", stderr)
	}
}

// TestAuthCITokenReadsTheLegacyName: LEOFLOW_OIDC_TOKEN works, and the
// new name wins when both are set.
func TestAuthCITokenReadsTheLegacyName(t *testing.T) {
	isolateCLIEnv(t)
	srv := newCITokenServer(t, http.StatusOK, ciTokenIssued)
	t.Setenv("DEXAFLOW_SERVER_URL", srv.URL)
	t.Setenv("DEXAFLOW_OIDC_TOKEN", "")
	t.Setenv("LEOFLOW_OIDC_TOKEN", "old-name")
	if _, _, err := run(t, "auth", "ci-token", "--config", emptyConfig(t)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DEXAFLOW_OIDC_TOKEN", "new-name")
	if _, _, err := run(t, "auth", "ci-token", "--config", emptyConfig(t)); err != nil {
		t.Fatal(err)
	}
	if strings.Join(srv.tokens, ",") != "old-name,new-name" {
		t.Errorf("posted %q, want old-name then new-name", srv.tokens)
	}
}

// TestAuthCITokenRefusals: without the variable or a server nothing is
// sent; a refusal or a server without the exchange is an error that says
// why, and prints no session.
func TestAuthCITokenRefusals(t *testing.T) {
	cases := []struct {
		name, oidc string
		server     bool
		status     int
		body, want string
	}{
		{"no token", "", true, http.StatusOK, ciTokenIssued, "DEXAFLOW_OIDC_TOKEN"},
		{"no server", "tok", false, http.StatusOK, ciTokenIssued, "DEXAFLOW_SERVER_URL"},
		{"refused", "tok", true, http.StatusForbidden, `{"status":403,"detail":"no rule of the CI issuer admits this job"}`, "no rule of the CI issuer admits this job"},
		{"not served", "tok", true, http.StatusNotFound, `{"status":404,"detail":"not found"}`, "does not accept CI OIDC tokens"},
		{"no session", "tok", true, http.StatusOK, `{"token_type":"Bearer"}`, "no session"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateCLIEnv(t)
			srv := newCITokenServer(t, tc.status, tc.body)
			if tc.server {
				t.Setenv("DEXAFLOW_SERVER_URL", srv.URL)
			}
			t.Setenv("DEXAFLOW_OIDC_TOKEN", tc.oidc)
			t.Setenv("LEOFLOW_OIDC_TOKEN", "")

			out, _, err := run(t, "auth", "ci-token", "--config", emptyConfig(t))

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %q", err, tc.want)
			}
			if out != "" {
				t.Errorf("stdout = %q, want nothing", out)
			}
			if (tc.oidc == "" || !tc.server) && len(srv.tokens) != 0 {
				t.Errorf("sent %d requests, want none", len(srv.tokens))
			}
		})
	}
}
