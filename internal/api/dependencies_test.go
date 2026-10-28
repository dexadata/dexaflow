package api

import (
	"errors"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/issuer"
)

// TestNewServerRejectsMissingDependencies: NewServer builds nothing, and
// names every missing field, when a required dependency is left out (#1950).
func TestNewServerRejectsMissingDependencies(t *testing.T) {
	for name, tc := range map[string]struct {
		deps Dependencies
		want []string
	}{
		"empty": {
			deps: Dependencies{},
			want: []string{"Observability.Logger", "Auth.Authenticator", "Auth.RateLimiter"},
		},
		"dev no auth still needs the token route's": {
			deps: Dependencies{Observability: ObservabilityDeps{Logger: discardLogger()}, Auth: AuthDeps{DevNoAuth: true}},
			want: []string{"Auth.Authenticator", "Auth.RateLimiter"},
		},
		"trusted issuer": {
			deps: withTestDefaults(Dependencies{Issuers: IssuerDeps{TrustedIssuer: fakeTrustedIssuer{issuer.Identity{}}}}),
			want: []string{"Issuers.TrustedIssuerUsers", "Auth.JWTSecret"},
		},
		"trusted issuer bearer": {
			deps: withTestDefaults(Dependencies{Issuers: IssuerDeps{TrustedIssuerBearer: deployIssuer{}}}),
			want: []string{"Issuers.TrustedIssuerUsers"},
		},
		"service token": {
			deps: withTestDefaults(Dependencies{Issuers: IssuerDeps{ServiceToken: testServiceToken}}),
			want: []string{"Issuers.ServiceTenants"},
		},
		"ci oidc exchange": {
			deps: withTestDefaults(Dependencies{Issuers: IssuerDeps{CIOIDC: &fakeCIVerifier{}, CIOIDCRoles: fakeCIRoles{}}}),
			want: []string{"Auth.JWTSecret"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			srv, err := NewServer(tc.deps)
			if srv != nil {
				t.Error("NewServer built a server from incomplete dependencies")
			}
			if !errors.Is(err, ErrMissingDependency) {
				t.Fatalf("NewServer error = %v, want ErrMissingDependency", err)
			}
			for _, field := range tc.want {
				if !strings.Contains(err.Error(), field) {
					t.Errorf("error %q does not name %s", err, field)
				}
			}
			if got := strings.Count(err.Error(), "("); got != len(tc.want) {
				t.Errorf("error %q names %d fields, want %d", err, got, len(tc.want))
			}
		})
	}
}

// TestNewServerAcceptsRequiredDependencies: the three dependencies every
// server needs are enough; everything else is optional.
func TestNewServerAcceptsRequiredDependencies(t *testing.T) {
	srv, err := NewServer(withTestDefaults(Dependencies{}))
	if err != nil || srv == nil {
		t.Fatalf("NewServer = %v, %v; want a server", srv, err)
	}
}
