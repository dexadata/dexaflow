package auth

import (
	"context"
	"errors"
	"testing"
	"time"
)

// A principal trusted from its signed claims (the dev-token subject, or any
// token when no store is bound) must name a tenant. Without one it cannot be
// scoped, so it is an invalid token everywhere it is checked: request
// authentication and renewal alike.

func TestAuthenticateRejectsATenantlessDevToken(t *testing.T) {
	const secret = "minted-secret"
	tok, err := MintUserToken(secret, time.Hour, User{ID: DevTokenSubject, Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	for name, a := range map[string]*JWTAuthenticator{
		"dev subject, no row": NewJWTAuthenticator(&fakeStore{byIDErr: ErrUserNotFound}, secret, time.Hour),
		"no store":            NewJWTAuthenticator(nil, secret, time.Hour),
	} {
		_, err := a.Authenticate(context.Background(), tok)
		if !errors.Is(err, ErrInvalidToken) || !errors.Is(err, ErrTenantlessToken) {
			t.Errorf("%s: err = %v, want ErrInvalidToken and ErrTenantlessToken", name, err)
		}
	}
}

func TestRenewUserTokenRefusesATenantlessToken(t *testing.T) {
	a := NewJWTAuthenticator(nil, "secret", time.Hour)
	tok, err := MintUserToken("secret", time.Hour, User{ID: DevTokenSubject, Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	renewed, ok, err := a.RenewUserToken(context.Background(), tok, time.Hour, 24*time.Hour)
	if ok || renewed != "" {
		t.Errorf("renewed a tenantless token: ok=%v err=%v", ok, err)
	}
}

func TestAuthenticateKeepsATenantedDevToken(t *testing.T) {
	const secret = "minted-secret"
	tok, _ := MintUserToken(secret, time.Hour, User{ID: DevTokenSubject, TenantID: "default", Roles: []string{"admin"}})
	a := NewJWTAuthenticator(&fakeStore{byIDErr: ErrUserNotFound}, secret, time.Hour)
	if u, err := a.Authenticate(context.Background(), tok); err != nil || u.TenantID != "default" {
		t.Errorf("tenanted dev token: %+v, %v", u, err)
	}
}
