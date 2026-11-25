package config

import (
	"strings"
	"testing"
)

// TestValidateOIDCLogoutModes is #1165: auth.oidc.logout takes idp or local,
// empty reads as the idp default, and anything else fails boot naming the key.
func TestValidateOIDCLogoutModes(t *testing.T) {
	for _, mode := range []string{"", OIDCLogoutIDP, OIDCLogoutLocal} {
		c := validOIDCConfig()
		c.Auth.OIDC.Logout = mode
		if err := c.Validate(); err != nil {
			t.Errorf("logout %q: %v", mode, err)
		}
	}
	c := validOIDCConfig()
	c.Auth.OIDC.Logout = "global"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "auth.oidc.logout") {
		t.Errorf("logout global: err = %v, want one naming auth.oidc.logout", err)
	}
}

// TestValidateOIDCPostLogoutRedirectURL rejects a value the IdP could never
// redirect to, since a relative or schemeless URL fails only at sign-out.
func TestValidateOIDCPostLogoutRedirectURL(t *testing.T) {
	c := validOIDCConfig()
	c.Auth.OIDC.PostLogoutRedirectURL = "https://app.example.com/api/v2/auth/login"
	if err := c.Validate(); err != nil {
		t.Fatalf("absolute URL: %v", err)
	}
	c.Auth.OIDC.PostLogoutRedirectURL = "/api/v2/auth/login"
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "auth.oidc.post_logout_redirect_url") {
		t.Errorf("relative URL: err = %v, want one naming the key", err)
	}
}

// TestOIDCLogoutDefaultsToIdP pins the default the loader applies.
func TestOIDCLogoutDefaultsToIdP(t *testing.T) {
	if got := serverDefaults["auth.oidc.logout"]; got != OIDCLogoutIDP {
		t.Errorf("auth.oidc.logout default = %v, want %q", got, OIDCLogoutIDP)
	}
}

// TestSessionCookieNameCannotTakeTheIDTokenCookie keeps the two cookies apart:
// a session cookie of the same name would be overwritten at every SSO login.
func TestSessionCookieNameCannotTakeTheIDTokenCookie(t *testing.T) {
	if err := validateSessionCookieName("_oidc_idt", false); err == nil {
		t.Error("_oidc_idt accepted as the session cookie name")
	}
}
