package cli

import (
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

func TestResolveAdminEmail(t *testing.T) {
	cases := []struct {
		flag string
		cfg  *config.Config
		want string
	}{
		{"me@x.io", &config.Config{AdminEmail: "cfg@x.io"}, "me@x.io"}, // flag wins
		{"", &config.Config{AdminEmail: "cfg@x.io"}, "cfg@x.io"},       // config admin
		{"", &config.Config{}, "admin@leoflow.local"},                  // default
		{"", nil, "admin@leoflow.local"},                               // no config
	}
	for _, c := range cases {
		if got := resolveAdminEmail(c.flag, c.cfg); got != c.want {
			t.Errorf("resolveAdminEmail(%q,%v) = %q, want %q", c.flag, c.cfg, got, c.want)
		}
	}
}

func TestLoadUserConfig(t *testing.T) {
	if loadUserConfig("") != nil {
		t.Error("empty home should yield nil config")
	}
	home := t.TempDir()
	if loadUserConfig(home) != nil {
		t.Error("missing config.yaml should yield nil")
	}
	dir := filepath.Join(home, ".leoflow")
	if err := os.MkdirAll(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"),
		[]byte("admin_email: \"x@y.io\"\nlite_port: 9090\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c := loadUserConfig(home)
	if c == nil || c.AdminEmail != "x@y.io" || c.LitePort != 9090 {
		t.Errorf("loadUserConfig = %+v, want admin_email/lite_port parsed", c)
	}
}

func TestInvokingUserHome(t *testing.T) {
	t.Run("no SUDO_USER falls back to the process home", func(t *testing.T) {
		t.Setenv("SUDO_USER", "")
		want, _ := os.UserHomeDir()
		if got := invokingUserHome(); got != want {
			t.Errorf("invokingUserHome() = %q, want %q", got, want)
		}
	})
	t.Run("SUDO_USER resolves that user's home", func(t *testing.T) {
		u, err := user.Current()
		if err != nil {
			t.Skip("cannot resolve current user")
		}
		t.Setenv("SUDO_USER", u.Username)
		if got := invokingUserHome(); got != u.HomeDir {
			t.Errorf("invokingUserHome() = %q, want %q", got, u.HomeDir)
		}
	})
}

func TestResetPasswordDoesNotRequireRoot(t *testing.T) {
	// Lite is a per-user install, so reset-password must run as the normal user —
	// never demand root (the old `sudo` requirement was a catch-22: without sudo it
	// refused, and under sudo HOME became /root and it missed the user's config).
	// With no Postgres in a unit test it fails on the DB connection — but it must
	// NEVER fail with a "must run as root" refusal.
	cmd := newResetPasswordCommand()
	cmd.SetArgs([]string{})
	cmd.SilenceUsage, cmd.SilenceErrors = true, true
	if err := cmd.Execute(); err != nil && strings.Contains(err.Error(), "must run as root") {
		t.Fatalf("reset-password must not require root anymore; got: %v", err)
	}
}
