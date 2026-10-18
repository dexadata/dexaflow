package auth

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const ciTestSecret = "ci-session-test-secret-0123456789abcdef"

// fakeCIIssuers answers the fingerprint of each configured entry.
type fakeCIIssuers map[string]string

func (f fakeCIIssuers) Fingerprint(name string) (string, bool) {
	fp, ok := f[name]
	return fp, ok
}

// fakeRoles holds the grants of each tenant's roles, keyed "tenant/role".
type fakeRoles struct {
	grants map[string][]Permission
	err    error
	calls  int
}

func (f *fakeRoles) RolePermissions(_ context.Context, tenant, role string) ([]Permission, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	p, ok := f.grants[tenant+"/"+role]
	if !ok {
		return nil, ErrRoleNotFound
	}
	return p, nil
}

// refuseAll is a next authenticator that refuses every token, so a test sees
// whether the CI authenticator handled one itself.
type refuseAll struct{ seen []string }

func (r *refuseAll) Authenticate(_ context.Context, token string) (*User, error) {
	r.seen = append(r.seen, token)
	return nil, ErrInvalidToken
}

func (r *refuseAll) IssueToken(context.Context, Credentials) (string, error) { return "", nil }

var deployerGrants = []Permission{{Action: "read", Resource: "dag"}, {Action: "deploy", Resource: "dag"}}

func ciSession() CISession {
	return CISession{
		Issuer: "github-acme", Fingerprint: "fp1", Tenant: "acme",
		Subject: "repo:acme/dags:environment:production", Role: "deployer", Scopes: []string{ScopeDeploy},
	}
}

func newCIAuth(next Authenticator, roles *fakeRoles, now time.Time) *CISessionAuthenticator {
	a := NewCISessionAuthenticator(next, ciTestSecret, fakeCIIssuers{"github-acme": "fp1"}, roles)
	a.now = func() time.Time { return now }
	return a
}

func mintAt(t *testing.T, now time.Time, ttl time.Duration, s CISession) string {
	t.Helper()
	tok, exp, err := MintCISession(ciTestSecret, ttl, now, s)
	if err != nil {
		t.Fatal(err)
	}
	if !exp.Equal(now.Add(ttl).Truncate(time.Second)) {
		t.Errorf("expiry = %v, want %v", exp, now.Add(ttl))
	}
	if !strings.HasPrefix(tok, CISessionPrefix) || !IsCISession(tok) {
		t.Fatalf("token %q does not start with %s", tok, CISessionPrefix)
	}
	return tok
}

var ciNow = time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)

func TestCISessionAuthenticatesAsTheRoleOfItsTenant(t *testing.T) {
	roles := &fakeRoles{grants: map[string][]Permission{"acme/deployer": deployerGrants}}
	a := newCIAuth(&refuseAll{}, roles, ciNow)

	u, err := a.Authenticate(context.Background(), mintAt(t, ciNow, time.Hour, ciSession()))

	if err != nil {
		t.Fatalf("Authenticate: %v", err)
	}
	want := User{
		ID: "ci:github-acme", TenantID: "acme", Email: "ci:github-acme:repo:acme/dags:environment:production",
		Roles: []string{"deployer"}, Permissions: deployerGrants, Scoped: true, Scopes: []string{ScopeDeploy},
	}
	if !reflect.DeepEqual(*u, want) {
		t.Errorf("principal = %+v\nwant        %+v", *u, want)
	}
}

// TestCISessionReloadsTheRoleOnEveryRequest: a grant removed from the role,
// or the role deleted, takes effect on the next request.
func TestCISessionReloadsTheRoleOnEveryRequest(t *testing.T) {
	roles := &fakeRoles{grants: map[string][]Permission{"acme/deployer": deployerGrants}}
	a := newCIAuth(&refuseAll{}, roles, ciNow)
	tok := mintAt(t, ciNow, time.Hour, ciSession())

	roles.grants["acme/deployer"] = deployerGrants[:1]
	u, err := a.Authenticate(context.Background(), tok)
	if err != nil || len(u.Permissions) != 1 {
		t.Fatalf("after removing deploy:dag: %+v, %v; want read:dag alone", u, err)
	}
	delete(roles.grants, "acme/deployer")
	if _, err := a.Authenticate(context.Background(), tok); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("after deleting the role: err = %v, want ErrInvalidToken", err)
	}
	if roles.calls != 2 {
		t.Errorf("role lookups = %d, want one per request", roles.calls)
	}
}

func TestCISessionRefusals(t *testing.T) {
	roles := &fakeRoles{grants: map[string][]Permission{"acme/deployer": deployerGrants}}
	cases := []struct {
		name  string
		token func(t *testing.T) string
	}{
		{"expired", func(t *testing.T) string { return mintAt(t, ciNow.Add(-2*time.Hour), time.Hour, ciSession()) }},
		{"entry changed", func(t *testing.T) string {
			s := ciSession()
			s.Fingerprint = "fp0"
			return mintAt(t, ciNow, time.Hour, s)
		}},
		{"entry removed", func(t *testing.T) string {
			s := ciSession()
			s.Issuer = "gitlab-acme"
			return mintAt(t, ciNow, time.Hour, s)
		}},
		{"another secret", func(t *testing.T) string {
			tok, _, err := MintCISession("another-secret-0123456789abcdef0123", time.Hour, ciNow, ciSession())
			if err != nil {
				t.Fatal(err)
			}
			return tok
		}},
		{"a user token behind the prefix", func(t *testing.T) string {
			tok, err := MintUserToken(ciTestSecret, time.Hour, User{ID: "u1", TenantID: "acme", Roles: []string{"admin"}})
			if err != nil {
				t.Fatal(err)
			}
			return CISessionPrefix + tok
		}},
		{"not a JWT", func(*testing.T) string { return CISessionPrefix + "garbage" }},
		{"the user audience", func(t *testing.T) string {
			c := ciClaims{TenantID: "acme", CIIssuer: "github-acme", Fingerprint: "fp1", Role: "deployer",
				RegisteredClaims: jwt.RegisteredClaims{Issuer: tokenIssuer, Audience: jwt.ClaimStrings{audienceUser},
					ExpiresAt: jwt.NewNumericDate(ciNow.Add(time.Hour))}}
			tok, err := jwt.NewWithClaims(jwt.SigningMethodHS256, c).SignedString([]byte(ciTestSecret))
			if err != nil {
				t.Fatal(err)
			}
			return CISessionPrefix + tok
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			next := &refuseAll{}
			a := newCIAuth(next, roles, ciNow)

			_, err := a.Authenticate(context.Background(), tc.token(t))

			if !errors.Is(err, ErrInvalidToken) {
				t.Errorf("err = %v, want ErrInvalidToken", err)
			}
			if len(next.seen) != 0 {
				t.Errorf("a %s token reached the next authenticator", CISessionPrefix)
			}
		})
	}
}

// TestCISessionWithoutTenantIsRefused: a session that names no tenant is
// refused as tenantless, never served the default tenant.
func TestCISessionWithoutTenantIsRefused(t *testing.T) {
	roles := &fakeRoles{grants: map[string][]Permission{"/deployer": deployerGrants}}
	a := newCIAuth(&refuseAll{}, roles, ciNow)
	s := ciSession()
	s.Tenant = ""

	_, err := a.Authenticate(context.Background(), mintAt(t, ciNow, time.Hour, s))

	if !errors.Is(err, ErrInvalidToken) || !errors.Is(err, ErrTenantlessToken) {
		t.Errorf("err = %v, want ErrInvalidToken and ErrTenantlessToken", err)
	}
}

// TestCISessionStoreFailureIsNotARefusal: a role lookup that fails says
// nothing about the token, so it is not ErrInvalidToken (the API answers
// 503).
func TestCISessionStoreFailureIsNotARefusal(t *testing.T) {
	roles := &fakeRoles{err: errors.New("database down")}
	a := newCIAuth(&refuseAll{}, roles, ciNow)

	_, err := a.Authenticate(context.Background(), mintAt(t, ciNow, time.Hour, ciSession()))

	if err == nil || errors.Is(err, ErrInvalidToken) {
		t.Errorf("err = %v, want a store error that is not ErrInvalidToken", err)
	}
}

// TestCISessionDelegatesOtherTokens: anything without the prefix goes to the
// next authenticator, and the session's JWT without its prefix is not a user
// token there (another audience).
func TestCISessionDelegatesOtherTokens(t *testing.T) {
	next := &refuseAll{}
	a := newCIAuth(next, &fakeRoles{}, ciNow)
	if _, err := a.Authenticate(context.Background(), "dxf_abc"); !errors.Is(err, ErrInvalidToken) || len(next.seen) != 1 {
		t.Errorf("an API token was not handed on: err %v, seen %v", err, next.seen)
	}

	jwtAuth := NewJWTAuthenticator(nil, ciTestSecret, time.Hour)
	bare := strings.TrimPrefix(mintAt(t, time.Now(), time.Hour, ciSession()), CISessionPrefix)
	if _, err := jwtAuth.Authenticate(context.Background(), bare); !errors.Is(err, ErrInvalidToken) {
		t.Errorf("a CI session without its prefix authenticated as a user token: err = %v", err)
	}
}
