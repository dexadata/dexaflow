package cli

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// tokensServer is a fake control plane for the API token commands: it
// answers a login, records token creations and revokes, and lists one token.
type tokensServer struct {
	auths   []string
	creates []map[string]any
	revokes []string
	logins  int
}

func (s *tokensServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	s.auths = append(s.auths, r.Header.Get("Authorization"))
	w.Header().Set("Content-Type", "application/json")
	switch {
	case r.Method == http.MethodPost && r.URL.Path == "/auth/token":
		s.logins++
		_, _ = io.WriteString(w, `{"access_token":"session-jwt","token_type":"bearer"}`)
	case r.Method == http.MethodPost && r.URL.Path == "/api/v2/apiTokens":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		s.creates = append(s.creates, body)
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"id":"tok-1","name":"ci-x","scopes":["dexaflow:read","dexaflow:deploy"],`+
			`"created_at":"2026-10-13T12:00:00Z","expires_at":"2027-01-11T12:00:00Z","token":"dxf_secret"}`)
	case r.Method == http.MethodGet && r.URL.Path == "/api/v2/apiTokens":
		_, _ = io.WriteString(w, `{"api_tokens":[{"id":"tok-1","name":"ci-x","scopes":["dexaflow:read","dexaflow:deploy"],`+
			`"owner_email":"ci@acme.com","created_at":"2026-10-13T12:00:00Z","expires_at":"2027-01-11T12:00:00Z","last_used_at":null}],"total_entries":1}`)
	case r.Method == http.MethodDelete && strings.HasPrefix(r.URL.Path, "/api/v2/apiTokens/"):
		s.revokes = append(s.revokes, strings.TrimPrefix(r.URL.Path, "/api/v2/apiTokens/"))
		if strings.HasSuffix(r.URL.Path, "/missing") {
			w.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(w, `{"detail":"not found"}`)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.NotFound(w, r)
	}
}

func isolatedHome(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("DEXAFLOW_TOKEN", "")
	t.Setenv("LEOFLOW_TOKEN", "")
	t.Setenv("DEXAFLOW_SERVER_URL", "")
	t.Setenv("LEOFLOW_SERVER_URL", "")
}

// TestCreateDeployTokenWithASession: with --scope the command creates an API
// token with the session it finds, prints only the token on stdout, and says
// on stderr that it is shown once.
func TestCreateDeployTokenWithASession(t *testing.T) {
	isolatedHome(t)
	fake := &tokensServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, errOut, err := run(t, "auth", "create-token", "--server", srv.URL, "--token", "session-jwt",
		"--scope", "deploy", "--name", "ci-x", "--ttl", "90d")
	if err != nil {
		t.Fatalf("create-token: %v (%s)", err, errOut)
	}
	if out != "dxf_secret\n" {
		t.Errorf("stdout = %q, want only the token", out)
	}
	if !strings.Contains(errOut, "ci-x") || !strings.Contains(errOut, "not shown again") {
		t.Errorf("stderr = %q, want the name and a one-time warning", errOut)
	}
	if fake.logins != 0 || len(fake.creates) != 1 {
		t.Fatalf("logins %d creates %v, want one create and no login", fake.logins, fake.creates)
	}
	got := fake.creates[0]
	if got["name"] != "ci-x" || got["scope"] != "deploy" || got["expires_in_seconds"] != float64(90*24*3600) {
		t.Errorf("create body = %v", got)
	}
	if fake.auths[0] != "Bearer session-jwt" {
		t.Errorf("Authorization = %q", fake.auths[0])
	}
}

// TestCreateDeployTokenWithAPassword: given a username, the command signs in
// first and creates the token with that session.
func TestCreateDeployTokenWithAPassword(t *testing.T) {
	isolatedHome(t)
	fake := &tokensServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "auth", "create-token", "--server", srv.URL, "--username", "ci@acme.com",
		"--password", "pw-12345678", "--scope", "deploy", "--name", "ci-x")
	if err != nil {
		t.Fatalf("create-token: %v", err)
	}
	if out != "dxf_secret\n" || fake.logins != 1 || len(fake.creates) != 1 {
		t.Fatalf("out %q logins %d creates %v", out, fake.logins, fake.creates)
	}
	if _, set := fake.creates[0]["expires_in_seconds"]; set {
		t.Error("no --ttl should leave the lifetime to the server default")
	}
	if fake.auths[1] != "Bearer session-jwt" {
		t.Errorf("create Authorization = %q, want the session from the login", fake.auths[1])
	}
}

// TestCreateTokenWithoutScopeKeepsTheJWT: the command without the new flags
// is unchanged: it prints the JWT from a login.
func TestCreateTokenWithoutScopeKeepsTheJWT(t *testing.T) {
	isolatedHome(t)
	fake := &tokensServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	out, _, err := run(t, "auth", "create-token", "--server", srv.URL, "--username", "a@b.c", "--password", "pw")
	if err != nil || out != "session-jwt\n" || len(fake.creates) != 0 {
		t.Errorf("create-token = %q, %v (creates %v), want the JWT only", out, err, fake.creates)
	}
}

func TestCreateTokenFlagErrors(t *testing.T) {
	isolatedHome(t)
	fake := &tokensServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	cases := map[string][]string{
		"name without scope": {"--name", "ci"},
		"ttl without scope":  {"--ttl", "30d"},
		"scope without name": {"--scope", "deploy"},
		"bad ttl":            {"--scope", "deploy", "--name", "ci", "--ttl", "soon"},
		"zero ttl":           {"--scope", "deploy", "--name", "ci", "--ttl", "0d"},
		"negative ttl":       {"--scope", "deploy", "--name", "ci", "--ttl", "-5h"},
	}
	for name, extra := range cases {
		args := append([]string{"auth", "create-token", "--server", srv.URL, "--token", "t"}, extra...)
		if _, _, err := run(t, args...); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	if len(fake.creates) != 0 {
		t.Errorf("a refused command reached the server: %v", fake.creates)
	}
}

func TestParseTokenTTL(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]time.Duration{
		"90d": 90 * 24 * time.Hour, "1d": 24 * time.Hour, "12h": 12 * time.Hour, "1h30m": 90 * time.Minute,
	} {
		got, err := parseTokenTTL(in)
		if err != nil || got != want {
			t.Errorf("parseTokenTTL(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, bad := range []string{"", "d", "1.5d", "-1d", "0h", "90 d", "9999999999999d"} {
		if _, err := parseTokenTTL(bad); err == nil {
			t.Errorf("parseTokenTTL(%q) accepted", bad)
		}
	}
}

func TestTokensListAndRevoke(t *testing.T) {
	isolatedHome(t)
	fake := &tokensServer{}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	out, _, err := run(t, "auth", "tokens", "list", "--server", srv.URL, "--token", "session-jwt")
	if err != nil {
		t.Fatalf("tokens list: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want a header and one row, got:\n%s", out)
	}
	for _, want := range []string{"ID", "NAME", "SCOPES", "OWNER", "EXPIRES", "LAST USED"} {
		if !strings.Contains(lines[0], want) {
			t.Errorf("header %q misses %s", lines[0], want)
		}
	}
	for _, want := range []string{"tok-1", "ci-x", "dexaflow:deploy", "ci@acme.com", "2027-01-11", "never"} {
		if !strings.Contains(lines[1], want) {
			t.Errorf("row %q misses %s", lines[1], want)
		}
	}

	out, _, err = run(t, "auth", "tokens", "revoke", "tok-1", "--server", srv.URL, "--token", "session-jwt")
	if err != nil || !strings.Contains(out, "tok-1") || len(fake.revokes) != 1 || fake.revokes[0] != "tok-1" {
		t.Errorf("revoke = %q, %v (revokes %v)", out, err, fake.revokes)
	}
	if _, _, err := run(t, "auth", "tokens", "revoke", "missing", "--server", srv.URL, "--token", "session-jwt"); err == nil {
		t.Error("revoking an unknown token succeeded")
	}
}

// TestTokenFlagHelpPointsAwayFromArgv: the commands that manage API tokens
// steer a session JWT to DEXAFLOW_TOKEN or the saved session, since a value in
// argv is visible in ps and shell history.
func TestTokenFlagHelpPointsAwayFromArgv(t *testing.T) {
	for _, args := range [][]string{{"auth", "create-token"}, {"auth", "tokens", "list"}, {"auth", "tokens", "revoke"}} {
		cmd, _, err := NewRootCommand().Find(args)
		if err != nil {
			t.Fatalf("%v: %v", args, err)
		}
		usage := cmd.Flags().Lookup("token").Usage
		for _, want := range []string{"DEXAFLOW_TOKEN", "saved session", "shell history"} {
			if !strings.Contains(usage, want) {
				t.Errorf("%v --token help %q does not mention %q", args, usage, want)
			}
		}
	}
}
