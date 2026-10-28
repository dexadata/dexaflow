package main

import (
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// fakeAdminStore records the bootstrap calls and answers AdminUserState from a
// fixed map of states.
type fakeAdminStore struct {
	users    map[string]auth.AdminState
	hashFor  string // email BootstrapAdminHash was called with
	pwFor    string // email BootstrapAdmin was called with
	lookedUp int
}

func (f *fakeAdminStore) AdminUserState(_ context.Context, tenant, email string) (auth.AdminState, error) {
	f.lookedUp++
	if tenant != "default" {
		return auth.UserAbsent, nil
	}
	return f.users[email], nil
}

func (f *fakeAdminStore) BootstrapAdminHash(_ context.Context, _, email, _ string) (bool, error) {
	f.hashFor = email
	return f.users[email] == auth.UserAbsent, nil
}

func (f *fakeAdminStore) BootstrapAdmin(_ context.Context, _, email, _ string) (bool, error) {
	f.pwFor = email
	return f.users[email] == auth.UserAbsent, nil
}

// TestBootstrapAdminEmail drives bootstrapAdmin with the bootstrap settings the
// chart, docker-compose and Lite set, and checks which account it reconciles
// and what it logs about the choice.
func TestBootstrapAdminEmail(t *testing.T) {
	const kept = "bootstrap admin kept its pre-rename address"
	const both = "users exist under both bootstrap admin addresses"
	cases := []struct {
		name     string
		email    string // bootstrap.email
		hash     bool   // bootstrap.password_hash instead of the plaintext
		existing map[string]auth.AdminState
		want     string
		logs     []string
		noLogs   []string
	}{
		{"fresh server install", "", false, nil, auth.DefaultAdminEmail, []string{"bootstrapped admin user"}, []string{kept, both}},
		{"existing server install keeps its admin", "", false,
			map[string]auth.AdminState{auth.LegacyAdminEmail: auth.UserIsAdmin}, auth.LegacyAdminEmail,
			[]string{kept, "email=" + auth.LegacyAdminEmail}, []string{both}},
		{"fresh Lite database", auth.DefaultAdminEmail, true, nil, auth.DefaultAdminEmail, nil, []string{kept}},
		{"Lite reinstalled over a kept database", auth.DefaultAdminEmail, true,
			map[string]auth.AdminState{auth.LegacyAdminEmail: auth.UserIsAdmin}, auth.LegacyAdminEmail, []string{kept}, nil},
		{"both are admins: the new one, with a warning", "", false,
			map[string]auth.AdminState{auth.LegacyAdminEmail: auth.UserIsAdmin, auth.DefaultAdminEmail: auth.UserIsAdmin},
			auth.DefaultAdminEmail, []string{"level=WARN", both, "legacy_email=" + auth.LegacyAdminEmail}, []string{kept}},
		{"a non-admin at the new address is never given the bootstrap password", "", false,
			map[string]auth.AdminState{auth.LegacyAdminEmail: auth.UserIsAdmin, auth.DefaultAdminEmail: auth.UserWithoutAdminRole},
			auth.LegacyAdminEmail, []string{kept, both}, nil},
		{"old Lite config", auth.LegacyAdminEmail, true,
			map[string]auth.AdminState{auth.LegacyAdminEmail: auth.UserIsAdmin}, auth.LegacyAdminEmail, nil, []string{kept}},
		{"explicit address", "ops@corp.example", false,
			map[string]auth.AdminState{auth.LegacyAdminEmail: auth.UserIsAdmin}, "ops@corp.example", nil, []string{kept}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.BootstrapSection{Email: tc.email}
			if tc.hash {
				cfg.PasswordHash = "$2a$12$hash"
			} else {
				cfg.Password = "pw"
			}
			store := &fakeAdminStore{users: tc.existing}
			var logs bytes.Buffer
			if err := bootstrapAdmin(t.Context(), store, cfg, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
				t.Fatalf("bootstrapAdmin: %v", err)
			}
			got := store.pwFor
			if tc.hash {
				got = store.hashFor
			}
			if got != tc.want {
				t.Errorf("bootstrapped %q, want %q", got, tc.want)
			}
			for _, want := range tc.logs {
				if !strings.Contains(logs.String(), want) {
					t.Errorf("logs miss %q:\n%s", want, logs.String())
				}
			}
			for _, unwanted := range tc.noLogs {
				if strings.Contains(logs.String(), unwanted) {
					t.Errorf("logs carry %q:\n%s", unwanted, logs.String())
				}
			}
		})
	}
}

// TestBootstrapAdminWithoutPasswordTouchesNothing: no bootstrap credential
// means no bootstrap, and no database lookup either.
func TestBootstrapAdminWithoutPasswordTouchesNothing(t *testing.T) {
	store := &fakeAdminStore{}
	var logs bytes.Buffer
	if err := bootstrapAdmin(t.Context(), store, config.BootstrapSection{Email: "ops@corp.example"}, slog.New(slog.NewTextHandler(&logs, nil))); err != nil {
		t.Fatalf("bootstrapAdmin: %v", err)
	}
	if store.lookedUp != 0 || store.hashFor != "" || store.pwFor != "" {
		t.Errorf("store touched without a bootstrap credential: %+v", store)
	}
}
