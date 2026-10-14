package auth

import (
	"context"
	"errors"
	"testing"
)

// lookupIn returns an AdminLookup backed by a fixed map of states.
func lookupIn(states map[string]AdminState) AdminLookup {
	return func(_ context.Context, email string) (AdminState, error) { return states[email], nil }
}

func TestResolveAdminEmail(t *testing.T) {
	const (
		cur = DefaultAdminEmail
		old = LegacyAdminEmail
	)
	cases := []struct {
		name       string
		configured string
		states     map[string]AdminState
		want       AdminResolution
	}{
		{"fresh install, nothing configured", "", nil, AdminResolution{Email: cur}},
		{"fresh install, new default configured", cur, nil, AdminResolution{Email: cur}},
		{"old install keeps its admin", "", map[string]AdminState{old: UserIsAdmin}, AdminResolution{Email: old, KeptLegacy: true}},
		{"reinstall over an old database keeps its admin", cur, map[string]AdminState{old: UserIsAdmin}, AdminResolution{Email: old, KeptLegacy: true}},
		{"a legacy user without the role is still the install's user", cur, map[string]AdminState{old: UserWithoutAdminRole}, AdminResolution{Email: old, KeptLegacy: true}},
		{"both admins: the new one wins", cur, map[string]AdminState{old: UserIsAdmin, cur: UserIsAdmin}, AdminResolution{Email: cur, BothExist: true}},
		{"a non-admin at the new address never takes the legacy admin's place", cur, map[string]AdminState{old: UserIsAdmin, cur: UserWithoutAdminRole}, AdminResolution{Email: old, KeptLegacy: true, BothExist: true}},
		{"the new admin wins over a legacy non-admin", cur, map[string]AdminState{old: UserWithoutAdminRole, cur: UserIsAdmin}, AdminResolution{Email: cur, BothExist: true}},
		{"no admin anywhere: the new user wins", cur, map[string]AdminState{old: UserWithoutAdminRole, cur: UserWithoutAdminRole}, AdminResolution{Email: cur, BothExist: true}},
		{"an explicit old address is honored", old, nil, AdminResolution{Email: old}},
		{"a custom address is never rewritten", "ops@corp.example", map[string]AdminState{old: UserIsAdmin}, AdminResolution{Email: "ops@corp.example"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := ResolveAdminEmail(t.Context(), tc.configured, lookupIn(tc.states))
			if err != nil {
				t.Fatalf("ResolveAdminEmail: %v", err)
			}
			if got != tc.want {
				t.Errorf("ResolveAdminEmail(%q) = %+v, want %+v", tc.configured, got, tc.want)
			}
		})
	}
}

func TestResolveAdminEmailPropagatesLookupErrors(t *testing.T) {
	boom := errors.New("db down")
	_, err := ResolveAdminEmail(t.Context(), "", func(context.Context, string) (AdminState, error) { return UserAbsent, boom })
	if !errors.Is(err, boom) {
		t.Fatalf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestResolveAdminEmailSkipsLookupForCustomAddress(t *testing.T) {
	called := false
	got, err := ResolveAdminEmail(t.Context(), "ops@corp.example", func(context.Context, string) (AdminState, error) {
		called = true
		return UserAbsent, nil
	})
	if err != nil || got.Email != "ops@corp.example" || called {
		t.Fatalf("got %+v, err %v, lookup called %v; want the address back with no lookup", got, err, called)
	}
}
