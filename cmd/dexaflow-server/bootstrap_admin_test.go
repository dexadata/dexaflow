package main

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
)

// fakeAdminStore records the bootstrap calls and answers UserExists from a
// fixed set of emails.
type fakeAdminStore struct {
	users    map[string]bool
	hashFor  string // email BootstrapAdminHash was called with
	pwFor    string // email BootstrapAdmin was called with
	lookedUp int
}

func (f *fakeAdminStore) UserExists(_ context.Context, tenant, email string) (bool, error) {
	f.lookedUp++
	return tenant == "default" && f.users[email], nil
}

func (f *fakeAdminStore) BootstrapAdminHash(_ context.Context, _, email, _ string) (bool, error) {
	f.hashFor = email
	return !f.users[email], nil
}

func (f *fakeAdminStore) BootstrapAdmin(_ context.Context, _, email, _ string) (bool, error) {
	f.pwFor = email
	return !f.users[email], nil
}

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestBootstrapAdminEmail drives bootstrapAdmin through the environment the
// chart, docker-compose and Lite set, and checks which account it reconciles.
func TestBootstrapAdminEmail(t *testing.T) {
	cases := []struct {
		name     string
		email    string // LEOFLOW_BOOTSTRAP_EMAIL
		hash     bool   // LEOFLOW_BOOTSTRAP_PASSWORD_HASH instead of the plaintext
		existing []string
		want     string
	}{
		{"fresh server install", "", false, nil, auth.DefaultAdminEmail},
		{"existing server install keeps its admin", "", false, []string{auth.LegacyAdminEmail}, auth.LegacyAdminEmail},
		{"fresh Lite database", auth.DefaultAdminEmail, true, nil, auth.DefaultAdminEmail},
		{"Lite reinstalled over a kept database", auth.DefaultAdminEmail, true, []string{auth.LegacyAdminEmail}, auth.LegacyAdminEmail},
		{"old Lite config", auth.LegacyAdminEmail, true, []string{auth.LegacyAdminEmail}, auth.LegacyAdminEmail},
		{"explicit address", "ops@corp.example", false, []string{auth.LegacyAdminEmail}, "ops@corp.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("LEOFLOW_BOOTSTRAP_EMAIL", tc.email)
			t.Setenv("LEOFLOW_BOOTSTRAP_PASSWORD_HASH", "")
			t.Setenv("LEOFLOW_BOOTSTRAP_PASSWORD", "")
			if tc.hash {
				t.Setenv("LEOFLOW_BOOTSTRAP_PASSWORD_HASH", "$2a$12$hash")
			} else {
				t.Setenv("LEOFLOW_BOOTSTRAP_PASSWORD", "pw")
			}
			store := &fakeAdminStore{users: map[string]bool{}}
			for _, e := range tc.existing {
				store.users[e] = true
			}
			if err := bootstrapAdmin(t.Context(), store, quietLogger()); err != nil {
				t.Fatalf("bootstrapAdmin: %v", err)
			}
			got := store.pwFor
			if tc.hash {
				got = store.hashFor
			}
			if got != tc.want {
				t.Errorf("bootstrapped %q, want %q", got, tc.want)
			}
		})
	}
}

// TestBootstrapAdminWithoutPasswordTouchesNothing: no bootstrap credential
// means no bootstrap, and no database lookup either.
func TestBootstrapAdminWithoutPasswordTouchesNothing(t *testing.T) {
	t.Setenv("LEOFLOW_BOOTSTRAP_EMAIL", "")
	t.Setenv("LEOFLOW_BOOTSTRAP_PASSWORD_HASH", "")
	t.Setenv("LEOFLOW_BOOTSTRAP_PASSWORD", "")
	store := &fakeAdminStore{users: map[string]bool{}}
	if err := bootstrapAdmin(t.Context(), store, quietLogger()); err != nil {
		t.Fatalf("bootstrapAdmin: %v", err)
	}
	if store.lookedUp != 0 || store.hashFor != "" || store.pwFor != "" {
		t.Errorf("store touched without a bootstrap credential: %+v", store)
	}
}
