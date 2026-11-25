package serverapp

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestOIDCLogoutMode reports what sign-out actually ends (#1165).
func TestOIDCLogoutMode(t *testing.T) {
	for _, tc := range []struct {
		mode, endpoint, want string
	}{
		{"", "https://idp/logout", "idp"},
		{"idp", "https://idp/logout", "idp"},
		{"idp", "", "local"},
		{"local", "https://idp/logout", "local"},
	} {
		if got := oidcLogoutMode(config.OIDCSection{Logout: tc.mode}, tc.endpoint); got != tc.want {
			t.Errorf("mode %q endpoint %q = %q, want %q", tc.mode, tc.endpoint, got, tc.want)
		}
	}
}

// TestWarnLocalOnlyLogout warns only when the IdP sign-out was asked for and
// the issuer cannot give it.
func TestWarnLocalOnlyLogout(t *testing.T) {
	for _, tc := range []struct {
		mode, endpoint string
		warn           bool
	}{
		{"", "", true},
		{"idp", "", true},
		{"idp", "https://idp/logout", false},
		{"local", "", false},
	} {
		var buf bytes.Buffer
		warnLocalOnlyLogout(slog.New(slog.NewTextHandler(&buf, nil)), config.OIDCSection{Logout: tc.mode, Issuer: "https://accounts.google.com"}, tc.endpoint)
		if got := strings.Contains(buf.String(), "end_session_endpoint"); got != tc.warn {
			t.Errorf("mode %q endpoint %q: warned = %v, want %v (%s)", tc.mode, tc.endpoint, got, tc.warn, buf.String())
		}
	}
}
