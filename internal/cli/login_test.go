package cli

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func TestLoginPersistsTokenAndServer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/auth/token" {
			t.Errorf("login posted to %q, want /auth/token", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"jwt-login"}`)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	out, _, err := run(t, "auth", "login", "--server", srv.URL,
		"--username", "admin", "--password", "pw", "--config", cfgPath)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	if !strings.Contains(strings.ToLower(out), "logged in") {
		t.Errorf("output = %q, want a 'logged in' confirmation", out)
	}

	cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Token != "jwt-login" {
		t.Errorf("persisted Token = %q, want jwt-login", cfg.Token)
	}
	if cfg.ServerURL != srv.URL {
		t.Errorf("persisted ServerURL = %q, want %q", cfg.ServerURL, srv.URL)
	}
}

func TestLoginUsesConfigServerWhenFlagOmitted(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"access_token":"jwt-from-config"}`)
	}))
	defer srv.Close()

	// Seed a config file whose server_url points at the fake server; login must
	// fall back to it when --server is not given.
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(cfgPath, []byte("server_url: "+srv.URL+"\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}

	if _, _, err := run(t, "auth", "login", "--username", "a", "--password", "b", "--config", cfgPath); err != nil {
		t.Fatalf("login with server from config: %v", err)
	}
	cfg, err := config.Load(cfgPath, nil)
	if err != nil {
		t.Fatalf("reload config: %v", err)
	}
	if cfg.Token != "jwt-from-config" {
		t.Errorf("Token = %q, want jwt-from-config", cfg.Token)
	}
}

func TestResolveCredentialsKeepsProvided(t *testing.T) {
	// Both given as flags/env: no prompt, returned as-is (the CI path).
	cmd := newAuthCommand()
	u, p, err := resolveCredentials(cmd, "admin", "pw")
	if err != nil || u != "admin" || p != "pw" {
		t.Fatalf("resolveCredentials = (%q,%q,%v), want (admin,pw,nil)", u, p, err)
	}
}

func TestResolveCredentialsNonInteractiveMissingErrors(t *testing.T) {
	// Non-interactive (test stdin is a buffer, not a TTY): a missing value is a
	// loud error, never a hang on a prompt.
	cmd := newAuthCommand()
	if _, _, err := resolveCredentials(cmd, "", "pw"); err == nil {
		t.Error("expected an error for a missing username in a non-interactive session")
	}
	if _, _, err := resolveCredentials(cmd, "admin", ""); err == nil {
		t.Error("expected an error for a missing password in a non-interactive session")
	}
}

func TestPromptLineReadsTrimmedInput(t *testing.T) {
	var out strings.Builder
	got, err := promptValue(strings.NewReader("admin\n"), &out, "Username: ")
	if err != nil {
		t.Fatalf("promptValue: %v", err)
	}
	if got != "admin" {
		t.Errorf("line = %q, want admin", got)
	}
	if !strings.Contains(out.String(), "Username:") {
		t.Errorf("label = %q, want the prompt", out.String())
	}
}

func TestSessionConfigPathFallsBackToDefault(t *testing.T) {
	// A bare login command has no --config flag, so the write target is the
	// default ~/.leoflow/config.yaml (resolved even when it does not yet exist).
	p, err := sessionConfigPath(newLoginCommand())
	if err != nil {
		t.Fatalf("sessionConfigPath: %v", err)
	}
	if !strings.HasSuffix(p, "config.yaml") {
		t.Errorf("path = %q, want the default config.yaml", p)
	}
}

func TestPromptValueHandlesEOFWithoutNewline(t *testing.T) {
	var out strings.Builder
	got, err := promptValue(strings.NewReader("admin"), &out, "Username: ") // no trailing \n
	if err != nil {
		t.Fatalf("promptValue at EOF: %v", err)
	}
	if got != "admin" {
		t.Errorf("line = %q, want admin (EOF is not an error)", got)
	}
}

func TestPromptPasswordFallsBackToPlainReadOffTTY(t *testing.T) {
	// A piped (non-terminal) reader falls back to a plain line read, so a
	// password can be supplied non-interactively (echo pw | dexaflow auth login).
	var out strings.Builder
	got, err := promptPassword(strings.NewReader("s3cret\n"), &out)
	if err != nil {
		t.Fatalf("promptPassword off a pipe: %v", err)
	}
	if got != "s3cret" {
		t.Errorf("password = %q, want s3cret", got)
	}
}

func TestLoginCommandErrorsWhenCredsMissingNonInteractive(t *testing.T) {
	// No flags, no env, non-interactive (test stdin is a pipe): the command must
	// fail loudly via resolveCredentials rather than hang on a prompt.
	t.Setenv("LEOFLOW_USERNAME", "")
	t.Setenv("LEOFLOW_PASSWORD", "")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, _, err := run(t, "auth", "login", "--server", "http://127.0.0.1:0", "--config", cfgPath); err == nil {
		t.Error("expected login to fail when credentials are missing in a non-interactive session")
	}
}

func TestLoginFailsLoudlyOnBadCredentials(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer srv.Close()

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	if _, _, err := run(t, "auth", "login", "--server", srv.URL,
		"--username", "admin", "--password", "wrong", "--config", cfgPath); err == nil {
		t.Error("expected login to fail on 401, got nil error")
	}
}
