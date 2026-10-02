package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTokenServerHint covers #1102: the config file stores a server_url and a
// token TOGETHER (internal/config/persist.go writes both in one call), so the
// CLI always knew which server it minted a token for. It just never said so.
// Pointing a command at a different control plane with --server carried the
// persisted token along, the other server rejected it, and the user got a bare
// 401 with no way to tell an expired token from a token for somewhere else.
func TestTokenServerHint(t *testing.T) {
	unauth := errors.New("server returned 401: {\"title\":\"unauthorized\"}")

	t.Run("names both servers when the token came from config", func(t *testing.T) {
		got := tokenServerHint(unauth, "http://prod.example:8080", "http://127.0.0.1:8088", true)
		msg := got.Error()
		for _, want := range []string{"http://prod.example:8080", "http://127.0.0.1:8088", loginCommandPath} {
			if !strings.Contains(msg, want) {
				t.Errorf("hint must contain %q; got %q", want, msg)
			}
		}
		if !errors.Is(got, unauth) {
			t.Error("the original error must stay in the chain")
		}
	})

	t.Run("the login command names the server actually being called", func(t *testing.T) {
		// Printing the OTHER server's login command would send the user to fix
		// the wrong side — the whole point is a one-line way out.
		msg := tokenServerHint(unauth, "http://prod.example:8080", "http://127.0.0.1:8088", true).Error()
		i := strings.Index(msg, loginCommandPath)
		if i < 0 {
			t.Fatalf("no login command in %q", msg)
		}
		if !strings.Contains(msg[i:], "http://prod.example:8080") {
			t.Errorf("the login hint must target the server being called; got %q", msg[i:])
		}
	})

	t.Run("silent when the servers agree", func(t *testing.T) {
		// Same server: the token is simply expired or wrong, and inventing a
		// mismatch would send the user chasing something that is not happening.
		got := tokenServerHint(unauth, "http://127.0.0.1:8088", "http://127.0.0.1:8088", true)
		if got.Error() != unauth.Error() {
			t.Errorf("expected the error untouched; got %q", got.Error())
		}
	})

	t.Run("silent when the token did not come from config", func(t *testing.T) {
		// An explicit --token or LEOFLOW_TOKEN is the user's own choice; the
		// config's server_url says nothing about where that token is valid.
		got := tokenServerHint(unauth, "http://prod.example:8080", "http://127.0.0.1:8088", false)
		if got.Error() != unauth.Error() {
			t.Errorf("expected the error untouched; got %q", got.Error())
		}
	})

	t.Run("silent for a non-401", func(t *testing.T) {
		other := errors.New("server returned 500: boom")
		if got := tokenServerHint(other, "http://a", "http://b", true); got.Error() != other.Error() {
			t.Errorf("only a 401 gets the hint; got %q", got.Error())
		}
	})

	t.Run("silent when there is no config server to compare against", func(t *testing.T) {
		got := tokenServerHint(unauth, "http://prod.example:8080", "", true)
		if got.Error() != unauth.Error() {
			t.Errorf("nothing to compare; got %q", got.Error())
		}
	})

	t.Run("a trailing slash is not a mismatch", func(t *testing.T) {
		// http://h:8088 and http://h:8088/ are the same server, and claiming a
		// mismatch there would be worse than saying nothing.
		got := tokenServerHint(unauth, "http://127.0.0.1:8088/", "http://127.0.0.1:8088", true)
		if got.Error() != unauth.Error() {
			t.Errorf("a trailing slash must not read as a different server; got %q", got.Error())
		}
	})

	t.Run("nil stays nil", func(t *testing.T) {
		if got := tokenServerHint(nil, "http://a", "http://b", true); got != nil {
			t.Errorf("nil error must stay nil; got %v", got)
		}
	})
}

// TestResolveServerTokenRecordsTheTarget locks the WIRING, not the helper. The
// hint is useless if resolveServerToken stops recording what it decided, and a
// test of tokenServerHint alone would stay green through exactly that
// regression — the shape of defect this repository keeps paying for.
func TestResolveServerTokenRecordsTheTarget(t *testing.T) {
	t.Run("a config token called against another server is recorded as such", func(t *testing.T) {
		resolvedTarget = struct {
			server     string
			configured string
			fromConfig bool
		}{}
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte("server_url: http://127.0.0.1:8088\ntoken: lite-tok\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := newDeployCommand()
		cmd.Flags().String("config", cfgPath, "")
		if err := cmd.Flags().Set("config", cfgPath); err != nil {
			t.Fatal(err)
		}
		// --server points elsewhere; the token still comes from the file.
		if _, _, err := resolveServerToken(cmd, "https://prod.example", ""); err != nil {
			t.Fatalf("resolveServerToken: %v", err)
		}
		if resolvedTarget.server != "https://prod.example" {
			t.Errorf("server = %q, want the server being called", resolvedTarget.server)
		}
		if resolvedTarget.configured != "http://127.0.0.1:8088" {
			t.Errorf("configured = %q, want the config's server_url", resolvedTarget.configured)
		}
		if !resolvedTarget.fromConfig {
			t.Error("fromConfig = false, but the token was read from the config file")
		}
		// And the end-to-end consequence: a 401 from that call explains itself.
		msg := apiStatusError(401, []byte(`{"title":"unauthorized"}`)).Error()
		if !strings.Contains(msg, "127.0.0.1:8088") || !strings.Contains(msg, "prod.example") {
			t.Errorf("a 401 must name both servers; got %q", msg)
		}
	})

	t.Run("an env token is not recorded as coming from config", func(t *testing.T) {
		// config.Load surfaces LEOFLOW_TOKEN as cfg.Token, so without this the
		// hint would fire for a token the user supplied deliberately.
		resolvedTarget = struct {
			server     string
			configured string
			fromConfig bool
		}{}
		t.Setenv("LEOFLOW_TOKEN", "env-tok")
		dir := t.TempDir()
		cfgPath := filepath.Join(dir, "config.yaml")
		if err := os.WriteFile(cfgPath, []byte("server_url: http://127.0.0.1:8088\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		cmd := newDeployCommand()
		cmd.Flags().String("config", cfgPath, "")
		if err := cmd.Flags().Set("config", cfgPath); err != nil {
			t.Fatal(err)
		}
		if _, _, err := resolveServerToken(cmd, "https://prod.example", ""); err != nil {
			t.Fatalf("resolveServerToken: %v", err)
		}
		if resolvedTarget.fromConfig {
			t.Error("an env-supplied token must not be reported as coming from config")
		}
		if strings.Contains(apiStatusError(401, []byte("{}")).Error(), "hint:") {
			t.Error("no hint is owed for a token the user supplied explicitly")
		}
	})
}

// TestLoginHintNamesACommandThatExists is the guard that was missing. The hint
// shipped saying `leoflow login`, which is not a command — login lives under
// `auth` — so the one-line way out of a wrong-server 401 could not be pasted.
// The original test asserted the literal "leoflow login", so it did not catch
// the defect; it locked it in, and would have gone red on the correct fix.
//
// Asserting a string against another string can only ever check that two
// authors typed the same thing. This resolves the suggested command against the
// REAL command tree, so moving or renaming `auth login` fails here instead of
// in a user's terminal.
func TestLoginHintNamesACommandThatExists(t *testing.T) {
	path := strings.TrimPrefix(loginCommandPath, "dexaflow ")
	if path == loginCommandPath {
		t.Fatalf("loginCommandPath %q must start with the binary name", loginCommandPath)
	}
	args := strings.Fields(path)
	cmd, _, err := NewRootCommand().Find(args)
	if err != nil {
		t.Fatalf("the hint tells the user to run %q, which does not resolve: %v", loginCommandPath, err)
	}
	// Find returns the deepest command it matched, so a bad leaf silently
	// resolves to its parent — assert we landed on the leaf itself.
	if cmd.Name() != args[len(args)-1] {
		t.Errorf("the hint tells the user to run %q, but that resolves to %q", loginCommandPath, cmd.CommandPath())
	}
	if !cmd.Runnable() {
		t.Errorf("%q resolves to %q, which is not runnable", loginCommandPath, cmd.CommandPath())
	}
	// The hint passes --server; a suggestion carrying a flag the command does
	// not accept is the same defect one level down.
	if cmd.Flags().Lookup("server") == nil && cmd.InheritedFlags().Lookup("server") == nil {
		t.Errorf("%q does not accept --server, which the hint passes", cmd.CommandPath())
	}
}
