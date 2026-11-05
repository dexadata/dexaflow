package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// A deactivation stamps the user's session cutoff. A token whose session began
// at or before it stays refused once the user is active again, for request
// authentication and renewal alike; a session that began after it works.
func TestSessionCutoffRefusesSessionsFromBeforeDeactivation(t *testing.T) {
	const secret = "cutoff-secret"
	tok, err := MintUserToken(secret, time.Hour, User{ID: "u1", TenantID: "default", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cases := []struct {
		name   string
		cutoff time.Time
		ok     bool
	}{
		{"never deactivated", time.Time{}, true},
		{"deactivated before the login", now.Add(-time.Hour), true},
		{"deactivated after the login", now.Add(time.Minute), false},
		{"deactivated in the login's second", now, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := &fakeStore{user: &User{ID: "u1", TenantID: "default", Roles: []string{"admin"}, SessionsValidAfter: tc.cutoff}}
			a := NewJWTAuthenticator(store, secret, time.Hour)
			_, err := a.Authenticate(context.Background(), tok)
			if tc.ok && err != nil {
				t.Errorf("Authenticate: got %v, want the token accepted", err)
			}
			if !tc.ok && !errors.Is(err, ErrInvalidToken) {
				t.Errorf("Authenticate: got %v, want ErrInvalidToken", err)
			}
			renewed, ok, err := a.RenewUserToken(context.Background(), tok, time.Hour, 24*time.Hour)
			if tc.ok && (err != nil || !ok || renewed == "") {
				t.Errorf("RenewUserToken: got ok=%v err=%v, want a renewal", ok, err)
			}
			if !tc.ok && (!errors.Is(err, ErrInvalidToken) || renewed != "") {
				t.Errorf("RenewUserToken: got %q, ok=%v, err=%v, want ErrInvalidToken", renewed, ok, err)
			}
		})
	}
}

// The origin of a renewed token is its first login (oiat), so renewing a
// session before the deactivation does not move it past the cutoff.
func TestSessionCutoffUsesTheSessionOrigin(t *testing.T) {
	login := time.Now().Add(-30 * time.Minute)
	c := &jwtClaims{RegisteredClaims: jwt.RegisteredClaims{IssuedAt: jwt.NewNumericDate(time.Now())}, OriginIssuedAt: jwt.NewNumericDate(login)}
	u := &User{SessionsValidAfter: time.Now().Add(-10 * time.Minute)}
	if !sessionPredatesCutoff(c, u) {
		t.Error("a session that began before the cutoff must be refused even when renewed after it")
	}
	if !sessionPredatesCutoff(&jwtClaims{}, u) {
		t.Error("a token with no origin must be refused when a cutoff exists")
	}
}
