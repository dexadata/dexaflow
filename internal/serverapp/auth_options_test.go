package serverapp

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/issuer"
)

// stubAuthn answers every token with the same user or error, and counts calls.
type stubAuthn struct {
	user   *auth.User
	err    error
	token  string
	issErr error
	calls  int
}

func (s *stubAuthn) Authenticate(context.Context, string) (*auth.User, error) {
	s.calls++
	return s.user, s.err
}

func (s *stubAuthn) IssueToken(context.Context, auth.Credentials) (string, error) {
	return s.token, s.issErr
}

func TestOptionAuthenticatorTriesTheEngineFirst(t *testing.T) {
	engineUser := &auth.User{ID: "u-engine", TenantID: "t-1", Scoped: true, Scopes: []string{auth.ScopeRead}}
	engine := &stubAuthn{user: engineUser}
	custom := &stubAuthn{user: &auth.User{ID: "u-custom", TenantID: "t-1"}}
	a := optionAuthenticator{engine: engine, custom: custom}

	got, err := a.Authenticate(context.Background(), "tok")
	if err != nil || got != engineUser {
		t.Fatalf("Authenticate = %v, %v; want the engine's user untouched", got, err)
	}
	if custom.calls != 0 {
		t.Fatal("the option's authenticator was asked about a token the engine accepted")
	}
}

func TestOptionAuthenticatorFallsBackOnlyOnAnInvalidToken(t *testing.T) {
	outage := errors.New("database down")
	engine := &stubAuthn{err: outage}
	custom := &stubAuthn{user: &auth.User{ID: "u-custom", TenantID: "t-1"}}
	a := optionAuthenticator{engine: engine, custom: custom}

	if _, err := a.Authenticate(context.Background(), "tok"); !errors.Is(err, outage) {
		t.Fatalf("err = %v; an engine outage must be answered as one, not handed to the option", err)
	}
	if custom.calls != 0 {
		t.Fatal("the option's authenticator was asked while the engine was down")
	}

	engine.err = fmt.Errorf("bad signature: %w", auth.ErrInvalidToken)
	got, err := a.Authenticate(context.Background(), "tok")
	if err != nil || got.ID != "u-custom" {
		t.Fatalf("Authenticate = %v, %v; want the option's user", got, err)
	}
}

func TestOptionAuthenticatorClearsWhatOnlyTheServerGrants(t *testing.T) {
	returned := &auth.User{
		ID: "u-custom", TenantID: "t-1", Roles: []string{"viewer"},
		Permissions:    []auth.Permission{{Action: "read", Resource: "dag"}},
		Scoped:         true,
		Scopes:         []string{auth.ScopeDeploy},
		DeployAudience: true,
	}
	a := optionAuthenticator{
		engine: &stubAuthn{err: auth.ErrInvalidToken},
		custom: &stubAuthn{user: returned},
	}
	got, err := a.Authenticate(context.Background(), "tok")
	if err != nil {
		t.Fatal(err)
	}
	if got.Scoped || got.Scopes != nil || got.DeployAudience {
		t.Fatalf("user = %+v; Scoped, Scopes and DeployAudience must be cleared (ADR 0099 §6)", got)
	}
	if got.ID != "u-custom" || got.TenantID != "t-1" || len(got.Roles) != 1 || len(got.Permissions) != 1 {
		t.Fatalf("user = %+v; identity, tenant, roles and permissions must be kept", got)
	}
	returned.Roles[0] = "admin"
	if got.Roles[0] != "viewer" {
		t.Fatal("the principal shares its roles with the authenticator that returned it")
	}
	if !returned.DeployAudience {
		t.Fatal("the authenticator's own user was changed; the server must clear a copy")
	}
}

func TestOptionAuthenticatorRefusesANilUser(t *testing.T) {
	a := optionAuthenticator{
		engine: &stubAuthn{err: auth.ErrInvalidToken},
		custom: &stubAuthn{},
	}
	if _, err := a.Authenticate(context.Background(), "tok"); !errors.Is(err, auth.ErrInvalidToken) {
		t.Fatalf("err = %v; a nil user with no error must be an invalid token", err)
	}
}

func TestOptionAuthenticatorIssuesWithTheOption(t *testing.T) {
	a := optionAuthenticator{
		engine: &stubAuthn{token: "engine", issErr: errors.New("must not be used")},
		custom: &stubAuthn{token: "custom"},
	}
	tok, err := a.IssueToken(context.Background(), auth.Credentials{Tenant: "default", Username: "ada", Password: "x"})
	if err != nil || tok != "custom" {
		t.Fatalf("IssueToken = %q, %v; want the option's token", tok, err)
	}
}

func TestRequestAuthenticatorWithoutOptionIsTheEngine(t *testing.T) {
	engine := auth.NewJWTAuthenticator(nil, "secret-secret-secret-secret-secret", 0)
	if got := requestAuthenticator(engine, nil, discardLogger()); got != auth.Authenticator(engine) {
		t.Fatalf("authenticator = %T; want the engine's own", got)
	}
}

func TestValidateAPIOptionsGuardsTheTrustedIssuer(t *testing.T) {
	ti := issuer.New(context.Background(), issuer.Config{Name: "portal"})
	empty := &config.ServerConfig{}
	configured := &config.ServerConfig{}
	configured.Auth.TrustedIssuer.Issuer = "https://id.example.com"

	cases := []struct {
		name    string
		cfg     *config.ServerConfig
		origins []string
		want    error
	}{
		{"no issuer from options", configured, nil, nil},
		{"issuer with an origin", empty, []string{"https://portal.example.com"}, nil},
		{"issuer without origins", empty, nil, errIssuerOrigins},
		{"issuer with an origin that has a path", empty, []string{"https://portal.example.com/x"}, errIssuerOrigins},
		{"issuer next to the configured one", configured, []string{"https://portal.example.com"}, errIssuerTwice},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var opt *issuer.Verifier
			if tc.name != "no issuer from options" {
				opt = ti
			}
			var err error
			if opt == nil {
				err = validateAPIOptions(tc.cfg, nil, tc.origins)
			} else {
				err = validateAPIOptions(tc.cfg, opt, tc.origins)
			}
			if !errors.Is(err, tc.want) {
				t.Fatalf("validateAPIOptions = %v, want %v", err, tc.want)
			}
		})
	}
}
