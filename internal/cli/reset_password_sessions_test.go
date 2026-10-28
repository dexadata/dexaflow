package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
)

// fakeResetStore stands in for the Lite datastore so the whole reset-password
// flow, config rewrite and report included, runs without Postgres.
type fakeResetStore struct {
	found      bool
	setErr     error
	running    bool
	runningErr error
	gotEmail   string
	users      map[string]auth.AdminState // states AdminUserState reports; nil reports none
}

func (f *fakeResetStore) AdminUserState(_ context.Context, _, email string) (auth.AdminState, error) {
	return f.users[email], nil
}

func (f *fakeResetStore) SetUserPassword(_ context.Context, _, email, _ string) (bool, error) {
	f.gotEmail = email
	return f.found, f.setErr
}

func (f *fakeResetStore) ServerRunning(context.Context) (bool, error) {
	return f.running, f.runningErr
}

// open hands the fake to resetPassword the way runResetPassword hands it a
// connected datastore.
func (f *fakeResetStore) open(context.Context) (resetPasswordStore, func(), error) {
	return f, func() {}, nil
}

const resetTestConfig = "admin_email: \"admin@leoflow.local\"\n" +
	"lite_port: 8080\n" +
	"jwt_secret: \"old-jwt-secret\"\n" +
	"secret_key: \"per-install-key\"\n"

// seedLiteHome writes a config.yaml under home/<dir> and returns its path.
func seedLiteHome(t *testing.T, home, dir string) string {
	t.Helper()
	state := filepath.Join(home, dir)
	if err := os.MkdirAll(state, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(state, "config.yaml")
	if err := os.WriteFile(path, []byte(resetTestConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestResetPasswordRotatesTheSessionSecret: a password reset must end the
// browser sessions signed before it (#413). Lite sessions are JWTs checked by
// signature only, so the per-install signing secret is rotated; the connection
// encryption key is carried forward untouched. Both the current ~/.dexaflow and
// a pre-rename ~/.leoflow install are covered.
func TestResetPasswordRotatesTheSessionSecret(t *testing.T) {
	for _, dir := range []string{".dexaflow", ".leoflow"} {
		t.Run(dir, func(t *testing.T) {
			for _, k := range []string{"DEXAFLOW_JWT_SECRET", "LEOFLOW_JWT_SECRET", "DEXAFLOW_SECRET_KEY", "LEOFLOW_SECRET_KEY"} {
				t.Setenv(k, "")
			}
			home := t.TempDir()
			path := seedLiteHome(t, home, dir)
			store := &fakeResetStore{found: true}
			var out bytes.Buffer
			if err := resetPassword(context.Background(), &out, home, "", store.open); err != nil {
				t.Fatalf("resetPassword: %v", err)
			}
			sec := configFileSecrets(path)
			if sec.jwtSecret == "" || sec.jwtSecret == "old-jwt-secret" {
				t.Errorf("jwt_secret = %q, want a freshly generated secret", sec.jwtSecret)
			}
			if len(sec.jwtSecret) != 64 {
				t.Errorf("jwt_secret has %d chars, want 64 hex chars", len(sec.jwtSecret))
			}
			if sec.secretKey != "per-install-key" {
				t.Errorf("secret_key = %q, the reset must keep the encryption key", sec.secretKey)
			}
			if store.gotEmail != "admin@leoflow.local" {
				t.Errorf("reset %q, want the config admin", store.gotEmail)
			}
			if !strings.Contains(out.String(), "signed out") {
				t.Errorf("output does not say sessions were signed out:\n%s", out.String())
			}
		})
	}
}

// TestResetPasswordTellsARunningServerToRestart: a running server keeps the
// secret it booted with, so sessions it signed stay valid until it restarts.
// The command must say so rather than claim they are already signed out. An
// error while checking is treated like a running server.
func TestResetPasswordTellsARunningServerToRestart(t *testing.T) {
	t.Parallel()
	cases := map[string]*fakeResetStore{
		"running":      {found: true, running: true},
		"check failed": {found: true, runningErr: errors.New("boom")},
	}
	for name, store := range cases {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			seedLiteHome(t, home, ".dexaflow")
			var out bytes.Buffer
			if err := resetPassword(context.Background(), &out, home, "", store.open); err != nil {
				t.Fatalf("resetPassword: %v", err)
			}
			if !strings.Contains(out.String(), "restart `dexaflow lite`") {
				t.Errorf("output does not ask for a restart:\n%s", out.String())
			}
			if strings.Contains(out.String(), "are signed out") {
				t.Errorf("output claims sessions are already signed out:\n%s", out.String())
			}
		})
	}
}

// TestResetPasswordWithoutConfigSaysSessionsSurvive: with no config.yaml there
// is no per-install secret to rotate, so the command must not claim it signed
// anyone out.
func TestResetPasswordWithoutConfigSaysSessionsSurvive(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	var out bytes.Buffer
	if err := resetPassword(context.Background(), &out, home, "", (&fakeResetStore{found: true}).open); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	if !strings.Contains(out.String(), "NOT signed out") {
		t.Errorf("output does not say sessions survive:\n%s", out.String())
	}
}

// TestResetPasswordLeavesTheSecretWhenNoAdminMatched: a reset that changed no
// password must not sign everyone out either.
func TestResetPasswordLeavesTheSecretWhenNoAdminMatched(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	path := seedLiteHome(t, home, ".dexaflow")
	err := resetPassword(context.Background(), &bytes.Buffer{}, home, "nobody@x.io", (&fakeResetStore{found: false}).open)
	if err == nil {
		t.Fatal("resetPassword succeeded for an unknown admin")
	}
	if got := configFileSecrets(path).jwtSecret; got != "old-jwt-secret" {
		t.Errorf("jwt_secret = %q after a failed reset, want it unchanged", got)
	}
}

// TestResetPasswordFindsTheLegacyAdmin: an install created before the rename
// keeps its admin@leoflow.local. With no admin recorded, or with the new default
// recorded by a reinstall over the kept database, reset-password resets that
// admin, and records it in config.yaml so the next boot reconciles the same
// account.
func TestResetPasswordFindsTheLegacyAdmin(t *testing.T) {
	cases := map[string]string{
		"reinstall over the kept database": "admin_email: \"admin@dexaflow.local\"\n",
		"no admin recorded":                "",
	}
	for name, adminLine := range cases {
		t.Run(name, func(t *testing.T) {
			for _, k := range []string{"DEXAFLOW_JWT_SECRET", "LEOFLOW_JWT_SECRET", "DEXAFLOW_SECRET_KEY", "LEOFLOW_SECRET_KEY"} {
				t.Setenv(k, "")
			}
			home := t.TempDir()
			path := seedLiteHome(t, home, ".dexaflow")
			if err := os.WriteFile(path, []byte(adminLine+"lite_port: 8080\njwt_secret: \"old\"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			store := &fakeResetStore{found: true, users: map[string]auth.AdminState{"admin@leoflow.local": auth.UserIsAdmin}}
			var out bytes.Buffer
			if err := resetPassword(context.Background(), &out, home, "", store.open); err != nil {
				t.Fatalf("resetPassword: %v", err)
			}
			if store.gotEmail != "admin@leoflow.local" {
				t.Errorf("reset %q, want the existing admin@leoflow.local", store.gotEmail)
			}
			if !strings.Contains(out.String(), "password reset for admin@leoflow.local") {
				t.Errorf("output does not name the reset admin:\n%s", out.String())
			}
			cfg, _ := os.ReadFile(path)
			if !strings.Contains(string(cfg), "admin_email: \"admin@leoflow.local\"") {
				t.Errorf("config.yaml should record the reset admin:\n%s", cfg)
			}
		})
	}
}

// TestResetPasswordDefaultsToTheDexaflowAdmin: a fresh install resets the new
// default admin.
func TestResetPasswordDefaultsToTheDexaflowAdmin(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	store := &fakeResetStore{found: true, users: map[string]auth.AdminState{"admin@dexaflow.local": auth.UserIsAdmin}}
	var out bytes.Buffer
	if err := resetPassword(context.Background(), &out, home, "", store.open); err != nil {
		t.Fatalf("resetPassword: %v", err)
	}
	if store.gotEmail != "admin@dexaflow.local" {
		t.Errorf("reset %q, want admin@dexaflow.local", store.gotEmail)
	}
}
