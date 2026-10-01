package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	apiclient "github.com/dexadata/dexaflow/pkg/client"
)

// runWithStdin runs the root command with the given stdin, capturing stdout and
// stderr — the stdin-fed counterpart to run(), for --password-stdin/--value-stdin.
func runWithStdin(t *testing.T, stdin string, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	root := NewRootCommand()
	var out, errb bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errb)
	root.SetIn(strings.NewReader(stdin))
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errb.String(), err
}

func TestSetConnectionReqSendsBodyAndReturnsConnection(t *testing.T) {
	var gotMethod, gotPath, gotBody, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		// The server masks: extra's token becomes ***, password is omitted.
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","extra":"{\"token\":\"***\"}"}`)
	}))
	defer srv.Close()

	host, login, pw, extra := "db", "u", "PWSECRET", `{"token":"TOPSECRET"}`
	body := apiclient.ConnectionBody{
		ConnectionId: strp("pg"),
		ConnType:     "postgres",
		Host:         &host,
		Login:        &login,
		Password:     &pw,
		Extra:        &extra,
	}
	conn, err := setConnectionReq(context.Background(), srv.URL, "admin-jwt", body)
	if err != nil {
		t.Fatalf("setConnectionReq: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/api/v2/connections" {
		t.Errorf("request = %s %s, want POST /api/v2/connections", gotMethod, gotPath)
	}
	if gotAuth != "Bearer admin-jwt" {
		t.Errorf("Authorization = %q, want the bearer", gotAuth)
	}
	// The password AND the extra secret must be sent to the server on set.
	if !strings.Contains(gotBody, "PWSECRET") {
		t.Errorf("request body missing the password: %s", gotBody)
	}
	if !strings.Contains(gotBody, "TOPSECRET") {
		t.Errorf("request body missing the extra: %s", gotBody)
	}
	if conn.ConnectionId != "pg" || conn.ConnType != "postgres" {
		t.Errorf("unexpected connection: %+v", conn)
	}
}

// TestSetConnectionCommandNeverPrintsSecrets is the core safety pin: `set` sends
// the password/extra but must never echo either back to the terminal.
func TestSetConnectionCommandNeverPrintsSecrets(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","extra":"{\"token\":\"***\"}"}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "connections", "set", "pg",
		"--config", cfg, "--conn-type", "postgres",
		"--host", "db", "--login", "u",
		"--password", "PWSECRET", "--extra", `{"token":"TOPSECRET"}`)
	if err != nil {
		t.Fatalf("connections set: %v", err)
	}
	if strings.Contains(out, "PWSECRET") || strings.Contains(out, "TOPSECRET") {
		t.Errorf("output leaked a secret: %q", out)
	}
	if !strings.Contains(out, "pg") || !strings.Contains(out, "postgres") {
		t.Errorf("output = %q, want a confirmation naming the connection", out)
	}
}

func TestSetConnectionCommandPasswordStdin(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres"}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := runWithStdin(t, "STDIN-PW\n", "connections", "set", "pg",
		"--config", cfg, "--conn-type", "postgres", "--password-stdin")
	if err != nil {
		t.Fatalf("connections set --password-stdin: %v", err)
	}
	var sent apiclient.ConnectionBody
	if uerr := json.Unmarshal([]byte(gotBody), &sent); uerr != nil {
		t.Fatalf("request body not JSON: %v (%s)", uerr, gotBody)
	}
	if sent.Password == nil || *sent.Password != "STDIN-PW" {
		t.Errorf("password from stdin not sent: %s", gotBody)
	}
	if strings.Contains(out, "STDIN-PW") {
		t.Errorf("output leaked the stdin password: %q", out)
	}
}

// TestSetConnectionCommandPasswordClearVsOmit locks the tri-state password flag
// (#887): an explicit empty --password sends an empty string (clear), while
// omitting --password sends no password field at all (nil, so the control plane
// preserves the stored, unreadable password). Gating on non-empty would make an
// empty --password a silent no-op, inconsistent with an empty --login clearing.
func TestSetConnectionCommandPasswordClearVsOmit(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres"}`)
	}))
	defer srv.Close()
	cfg := seedSessionConfig(t, srv.URL, "tok")

	// --password '' → an empty (clearing) password is sent.
	if _, _, err := run(t, "connections", "set", "pg",
		"--config", cfg, "--conn-type", "postgres", "--password", ""); err != nil {
		t.Fatalf("connections set --password '': %v", err)
	}
	var sent apiclient.ConnectionBody
	if uerr := json.Unmarshal([]byte(gotBody), &sent); uerr != nil {
		t.Fatalf("request body not JSON: %v (%s)", uerr, gotBody)
	}
	if sent.Password == nil {
		t.Errorf("--password '' must send an (empty) password to clear it: %s", gotBody)
	} else if *sent.Password != "" {
		t.Errorf("--password '' should send an empty string, got %q", *sent.Password)
	}

	// Omitting --password → no password field is sent (preserve).
	gotBody = ""
	if _, _, err := run(t, "connections", "set", "pg",
		"--config", cfg, "--conn-type", "postgres", "--host", "db2"); err != nil {
		t.Fatalf("connections set (no --password): %v", err)
	}
	sent = apiclient.ConnectionBody{}
	if uerr := json.Unmarshal([]byte(gotBody), &sent); uerr != nil {
		t.Fatalf("request body not JSON: %v (%s)", uerr, gotBody)
	}
	if sent.Password != nil {
		t.Errorf("omitting --password must not send a password field: %s", gotBody)
	}
}

func TestSetConnectionCommandRequiresConnType(t *testing.T) {
	cfg := seedSessionConfig(t, "http://127.0.0.1:0", "tok")
	if _, _, err := run(t, "connections", "set", "pg", "--config", cfg); err == nil {
		t.Error("expected an error when --conn-type is missing")
	}
}

// TestSetConnectionExtraFile: --extra-file reads the provider-secret JSON from a
// file so it never lands on argv/shell history; the file's contents must reach
// the server as the connection's extra.
func TestSetConnectionExtraFile(t *testing.T) {
	var gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		gotBody = string(b)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres"}`)
	}))
	defer srv.Close()

	extraPath := filepath.Join(t.TempDir(), "extra.json")
	extraJSON := `{"token":"FROM-FILE-SECRET"}`
	// Write with a trailing newline (as an editor would) to prove it is trimmed
	// so the stored blob matches an inline --extra byte-for-byte.
	if werr := os.WriteFile(extraPath, []byte(extraJSON+"\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "connections", "set", "pg", "--config", cfg,
		"--conn-type", "postgres", "--extra-file", extraPath)
	if err != nil {
		t.Fatalf("connections set --extra-file: %v", err)
	}
	var sent apiclient.ConnectionBody
	if uerr := json.Unmarshal([]byte(gotBody), &sent); uerr != nil {
		t.Fatalf("request body not JSON: %v (%s)", uerr, gotBody)
	}
	if sent.Extra == nil || *sent.Extra != extraJSON {
		t.Errorf("extra from file not sent verbatim: %s", gotBody)
	}
	if strings.Contains(out, "FROM-FILE-SECRET") {
		t.Errorf("output leaked the extra secret: %q", out)
	}
}

// TestSetConnectionExtraFlagsMutuallyExclusive: --extra and --extra-file name the
// same field from two sources, so supplying both is a user error rather than a
// silent pick-one.
func TestSetConnectionExtraFlagsMutuallyExclusive(t *testing.T) {
	extraPath := filepath.Join(t.TempDir(), "extra.json")
	if werr := os.WriteFile(extraPath, []byte(`{"a":"b"}`), 0o600); werr != nil {
		t.Fatal(werr)
	}
	cfg := seedSessionConfig(t, "http://127.0.0.1:0", "tok")
	_, _, err := run(t, "connections", "set", "pg", "--config", cfg,
		"--conn-type", "postgres", "--extra", `{"a":"b"}`, "--extra-file", extraPath)
	if err == nil {
		t.Error("expected an error when both --extra and --extra-file are given")
	}
}

// TestConnectionsGetCommand exercises the full `connections get` path (request +
// single-connection printer): it shows the metadata and the server-masked extra,
// and never prints a password.
func TestConnectionsGetCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","schema":"public","port":5432,"extra":"{\"token\":\"***\"}"}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "connections", "get", "pg", "--config", cfg)
	if err != nil {
		t.Fatalf("connections get: %v", err)
	}
	for _, want := range []string{"pg", "postgres", "db", "5432", "***"} {
		if !strings.Contains(out, want) {
			t.Errorf("get output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "password") {
		t.Errorf("get output should not mention a password field:\n%s", out)
	}
}

// TestConnectionsListCommand exercises the full `connections list` path (request +
// table printer): the row is present and no secret column is rendered.
func TestConnectionsListCommand(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"connections":[{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","schema":"public","extra":"{\"token\":\"***\"}"}],"total_entries":1}`)
	}))
	defer srv.Close()

	cfg := seedSessionConfig(t, srv.URL, "tok")
	out, _, err := run(t, "connections", "list", "--config", cfg)
	if err != nil {
		t.Fatalf("connections list: %v", err)
	}
	if !strings.Contains(out, "pg") || !strings.Contains(out, "postgres") {
		t.Errorf("list output missing the connection:\n%s", out)
	}
	if strings.Contains(out, "token") || strings.Contains(out, "extra") {
		t.Errorf("list output must not render the extra column:\n%s", out)
	}
}

func TestSetConnectionSurfaces503(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"title":"encryption unavailable","detail":"set LEOFLOW_SECRET_KEY to manage connections"}`)
	}))
	defer srv.Close()

	_, err := setConnectionReq(context.Background(), srv.URL, "t",
		apiclient.ConnectionBody{ConnectionId: strp("pg"), ConnType: "postgres"})
	if err == nil {
		t.Fatal("expected an error on 503")
	}
	if !strings.Contains(err.Error(), "LEOFLOW_SECRET_KEY") {
		t.Errorf("error should carry the server's message, got: %v", err)
	}
}

func TestListConnectionsReq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v2/connections" {
			t.Errorf("request = %s %s, want GET /api/v2/connections", r.Method, r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"connections":[{"connection_id":"pg","conn_type":"postgres","host":"db","login":"u","schema":"public"}],"total_entries":1}`)
	}))
	defer srv.Close()

	coll, err := listConnectionsReq(context.Background(), srv.URL, "t")
	if err != nil {
		t.Fatalf("listConnectionsReq: %v", err)
	}
	if coll == nil || coll.Connections == nil || len(*coll.Connections) != 1 {
		t.Fatalf("unexpected collection: %+v", coll)
	}
}

func TestGetConnectionReq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/connections/pg" {
			t.Errorf("path = %s, want /api/v2/connections/pg", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"connection_id":"pg","conn_type":"postgres"}`)
	}))
	defer srv.Close()

	conn, err := getConnectionReq(context.Background(), srv.URL, "t", "pg")
	if err != nil {
		t.Fatalf("getConnectionReq: %v", err)
	}
	if conn.ConnectionId != "pg" {
		t.Errorf("connection = %+v", conn)
	}
}

func TestDeleteConnectionReq(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete || r.URL.Path != "/api/v2/connections/pg" {
			t.Errorf("request = %s %s, want DELETE /api/v2/connections/pg", r.Method, r.URL.Path)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	if err := deleteConnectionReq(context.Background(), srv.URL, "t", "pg"); err != nil {
		t.Fatalf("deleteConnectionReq: %v", err)
	}
}

func TestDeleteConnectionReqNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if err := deleteConnectionReq(context.Background(), srv.URL, "t", "ghost"); err == nil {
		t.Error("expected an error deleting a missing connection")
	}
}

func TestPrintConnectionListNeverShowsSecrets(t *testing.T) {
	extra := `{"token":"***"}`
	host, login, schema := "db", "u", "public"
	coll := &apiclient.ConnectionCollection{Connections: &[]apiclient.Connection{
		{ConnectionId: "pg", ConnType: "postgres", Host: &host, Login: &login, Schema: &schema, Extra: &extra},
	}}
	var buf bytes.Buffer
	if err := printConnectionList(&buf, coll); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"CONNECTION_ID", "TYPE", "HOST", "pg", "postgres", "db"} {
		if !strings.Contains(out, want) {
			t.Errorf("table missing %q; got:\n%s", want, out)
		}
	}
	// The extra column must not be rendered at all — no secrets in the table.
	if strings.Contains(out, "token") {
		t.Errorf("table leaked the extra field: %q", out)
	}
}

func TestPrintConnectionListEmpty(t *testing.T) {
	var buf bytes.Buffer
	if err := printConnectionList(&buf, nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "No connections") {
		t.Errorf("empty = %q, want the friendly line", buf.String())
	}
}

// strp is a local string-pointer helper for the connection/variable tests.
func strp(s string) *string { return &s }
