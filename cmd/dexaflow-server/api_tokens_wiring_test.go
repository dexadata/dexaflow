package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/api/apitest"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// tokenRepo is the API token store of the test: one live token of an active
// user of acme, so any refusal comes from the switch, not from the token.
type tokenRepo struct{ plain string }

func (r tokenRepo) FindAPITokenByHash(_ context.Context, hash string) (*auth.APIToken, error) {
	if hash != auth.HashAPIToken(r.plain) {
		return nil, auth.ErrAPITokenNotFound
	}
	return &auth.APIToken{ID: "t1", TenantID: "acme", UserID: "u1", Scopes: []string{auth.ScopeRead}, ExpiresAt: time.Now().Add(time.Hour)}, nil
}

func (tokenRepo) TouchAPIToken(context.Context, string, time.Time) (bool, error) { return true, nil }

func (tokenRepo) FindUserByLogin(context.Context, string, string) (*auth.User, string, error) {
	return nil, "", auth.ErrUserNotFound
}

func (tokenRepo) FindUserByID(context.Context, string) (*auth.User, bool, error) {
	return &auth.User{ID: "u1", TenantID: "acme", Roles: []string{"viewer"}}, true, nil
}

func (tokenRepo) CreateAPIToken(_ context.Context, tenant, userID, name, _ string, scopes []string, expiresAt time.Time) (auth.APIToken, error) {
	return auth.APIToken{ID: "t2", TenantID: tenant, UserID: userID, Name: name, Scopes: scopes, ExpiresAt: expiresAt}, nil
}

func (tokenRepo) ListAPITokens(context.Context, string, string, int, int) ([]auth.APIToken, int, error) {
	return nil, 0, nil
}

func (tokenRepo) RevokeAPIToken(context.Context, string, string, string) (auth.APIToken, error) {
	return auth.APIToken{}, errors.New("not used")
}

func loadWithTokens(t *testing.T, disabled string) *config.ServerConfig {
	t.Helper()
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("s", 40))
	if disabled != "" {
		t.Setenv("DEXAFLOW_AUTH_API_TOKENS_DISABLED", disabled)
	}
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

// createStatus posts a token creation as a signed-in admin of acme and
// returns the status.
func createStatus(t *testing.T, deps api.Dependencies, secret string) int {
	t.Helper()
	userTok, err := auth.MintUserToken(secret, time.Hour, auth.User{ID: auth.DevTokenSubject, TenantID: "acme", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	deps.Observability.Logger = slog.New(slog.DiscardHandler)
	srv := httptest.NewServer(apitest.NewServer(deps))
	defer srv.Close()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+"/api/v2/apiTokens", strings.NewReader(`{"name":"ci","scope":"read"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+userTok)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

// TestAPITokensAreOnByDefault: an install that never mentions the switch
// keeps ADR 0074's tokens, checked before the JWT authenticator.
func TestAPITokensAreOnByDefault(t *testing.T) {
	cfg := loadWithTokens(t, "")
	plain, _, err := auth.NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	repo := tokenRepo{plain: plain}
	authn := auth.NewJWTAuthenticator(nil, cfg.Auth.JWT.Secret, time.Hour)

	deps := withAPITokens(api.Dependencies{Auth: api.AuthDeps{Authenticator: authn}}, cfg, repo)

	if deps.Stores.APITokens == nil {
		t.Fatal("the token routes are off by default")
	}
	u, err := deps.Auth.Authenticator.Authenticate(context.Background(), plain)
	if err != nil || u.TenantID != "acme" || !u.Scoped {
		t.Fatalf("a live API token: %+v, %v; want acme's scoped user", u, err)
	}
	if got := createStatus(t, deps, cfg.Auth.JWT.Secret); got != http.StatusCreated {
		t.Errorf("POST /api/v2/apiTokens by default: %d, want 201", got)
	}
}

// TestAPITokensOnForAZeroConfig: a ServerConfig built in code, which sets
// nothing, keeps the tokens, as every caller of buildAPIServer before the
// setting existed expects.
func TestAPITokensOnForAZeroConfig(t *testing.T) {
	repo := tokenRepo{}
	authn := auth.NewJWTAuthenticator(nil, strings.Repeat("s", 40), time.Hour)

	deps := withAPITokens(api.Dependencies{Auth: api.AuthDeps{Authenticator: authn}}, &config.ServerConfig{}, repo)

	if deps.Stores.APITokens == nil || deps.Auth.Authenticator == auth.Authenticator(authn) {
		t.Error("a zero ServerConfig turned the API tokens off")
	}
}

// TestAPITokensOffServeNoRouteAndAcceptNoToken: with the switch off the token
// routes and their screen are not served, and a token
// minted while they were on is refused like any unknown bearer; user JWTs
// still authenticate.
func TestAPITokensOffServeNoRouteAndAcceptNoToken(t *testing.T) {
	cfg := loadWithTokens(t, "true")
	plain, _, err := auth.NewAPIToken()
	if err != nil {
		t.Fatal(err)
	}
	repo := tokenRepo{plain: plain}
	authn := auth.NewJWTAuthenticator(nil, cfg.Auth.JWT.Secret, time.Hour)

	deps := withAPITokens(api.Dependencies{Auth: api.AuthDeps{Authenticator: authn}, Stores: api.StoreDeps{APITokens: repo}}, cfg, repo)

	if deps.Stores.APITokens != nil {
		t.Error("the token routes are still registered")
	}
	if deps.Auth.Authenticator != auth.Authenticator(authn) {
		t.Error("the authenticator still checks API tokens")
	}
	if _, authErr := deps.Auth.Authenticator.Authenticate(context.Background(), plain); !errors.Is(authErr, auth.ErrInvalidToken) {
		t.Errorf("a live API token with the switch off: %v, want ErrInvalidToken", authErr)
	}
	userTok, err := auth.MintUserToken(cfg.Auth.JWT.Secret, time.Hour, auth.User{ID: auth.DevTokenSubject, TenantID: "acme", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if u, err := deps.Auth.Authenticator.Authenticate(context.Background(), userTok); err != nil || u.TenantID != "acme" {
		t.Errorf("a user token with the switch off: %+v, %v", u, err)
	}

	if got := createStatus(t, deps, cfg.Auth.JWT.Secret); got != http.StatusNotFound {
		t.Errorf("POST /api/v2/apiTokens with the switch off: %d, want 404 (not served)", got)
	}
}
