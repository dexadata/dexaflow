package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"
)

// TestLoadLiteAdminDefaultsToTheDexaflowAdmin: a config.yaml with a hash and
// no admin_email boots the server with the new default address, which the
// server resolves to a legacy admin when that is the one the database has.
func TestLoadLiteAdminDefaultsToTheDexaflowAdmin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("admin_password_hash: \"$2a$12$hash\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := &cobra.Command{}
	cmd.Flags().String("config", path, "")
	if got := loadLiteAdmin(cmd).adminEmail; got != "admin@dexaflow.local" {
		t.Errorf("adminEmail = %q, want admin@dexaflow.local", got)
	}
}

// TestLiteLoginEmail: the ready banner names the account the server actually
// bootstrapped, so a reinstall over a kept pre-rename database tells the user
// to log in as admin@leoflow.local.
func TestLiteLoginEmail(t *testing.T) {
	legacyOnly := func(_ context.Context, email string) (bool, error) { return email == "admin@leoflow.local", nil }
	broken := func(context.Context, string) (bool, error) { return false, errors.New("db down") }
	cases := []struct {
		name, configured, want string
		exists                 func(context.Context, string) (bool, error)
	}{
		{"legacy admin in the database", "admin@dexaflow.local", "admin@leoflow.local", legacyOnly},
		{"custom address untouched", "me@corp.example", "me@corp.example", legacyOnly},
		{"no-auth stays no-auth", "", "", legacyOnly},
		{"lookup failure keeps the configured address", "admin@dexaflow.local", "admin@dexaflow.local", broken},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := liteLoginEmail(t.Context(), tc.configured, tc.exists); got != tc.want {
				t.Errorf("liteLoginEmail(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}
