package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/cobra"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestLoadLiteAdminDefaultsToTheDexaflowAdmin: a config.yaml with a hash and
// no admin_email boots the server with the new default address, which the
// server resolves to a legacy admin when that is the one the database has.
func TestLoadLiteAdminDefaultsToTheDexaflowAdmin(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	legacyOnly := func(_ context.Context, email string) (auth.AdminState, error) {
		if email == "admin@leoflow.local" {
			return auth.UserIsAdmin, nil
		}
		return auth.UserAbsent, nil
	}
	broken := func(context.Context, string) (auth.AdminState, error) { return auth.UserAbsent, errors.New("db down") }
	cases := []struct {
		name, configured, want string
		exists                 auth.AdminLookup
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

// TestDevTokenEmail: the in-process dev token names the admin the banner
// names, and the default address under no-auth.
func TestDevTokenEmail(t *testing.T) {
	t.Parallel()
	if got := devTokenEmail("admin@leoflow.local"); got != "admin@leoflow.local" {
		t.Errorf("devTokenEmail(legacy) = %q, want the resolved admin", got)
	}
	if got := devTokenEmail(""); got != "admin@dexaflow.local" {
		t.Errorf("devTokenEmail(no-auth) = %q, want admin@dexaflow.local", got)
	}
}
