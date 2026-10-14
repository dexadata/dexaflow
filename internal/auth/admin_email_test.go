package auth

import (
	"context"
	"errors"
	"testing"
)

// existsIn returns a UserExistsFunc backed by a fixed set of emails.
func existsIn(emails ...string) UserExistsFunc {
	set := map[string]bool{}
	for _, e := range emails {
		set[e] = true
	}
	return func(_ context.Context, email string) (bool, error) { return set[email], nil }
}

func TestResolveAdminEmail(t *testing.T) {
	cases := []struct {
		name       string
		configured string
		existing   []string
		want       string
	}{
		{"fresh install, nothing configured", "", nil, DefaultAdminEmail},
		{"fresh install, new default configured", DefaultAdminEmail, nil, DefaultAdminEmail},
		{"old install keeps its admin", "", []string{LegacyAdminEmail}, LegacyAdminEmail},
		{"reinstall over an old database keeps its admin", DefaultAdminEmail, []string{LegacyAdminEmail}, LegacyAdminEmail},
		{"the new admin wins when both exist", DefaultAdminEmail, []string{LegacyAdminEmail, DefaultAdminEmail}, DefaultAdminEmail},
		{"an explicit old address is honored", LegacyAdminEmail, nil, LegacyAdminEmail},
		{"a custom address is never rewritten", "ops@corp.example", []string{LegacyAdminEmail}, "ops@corp.example"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAdminEmail(t.Context(), tc.configured, existsIn(tc.existing...))
			if err != nil {
				t.Fatalf("ResolveAdminEmail: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveAdminEmail(%q) = %q, want %q", tc.configured, got, tc.want)
			}
		})
	}
}

func TestResolveAdminEmailPropagatesLookupErrors(t *testing.T) {
	boom := errors.New("db down")
	_, err := ResolveAdminEmail(t.Context(), "", func(context.Context, string) (bool, error) { return false, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestResolveAdminEmailSkipsLookupForCustomAddress(t *testing.T) {
	called := false
	got, err := ResolveAdminEmail(t.Context(), "ops@corp.example", func(context.Context, string) (bool, error) {
		called = true
		return false, nil
	})
	if err != nil || got != "ops@corp.example" || called {
		t.Fatalf("got %q, err %v, lookup called %v; want the address back with no lookup", got, err, called)
	}
}
