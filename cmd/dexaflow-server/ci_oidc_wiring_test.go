package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/api/apitest"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
)

// ciProvider is a CI provider on a loopback server: a discovery document
// and one RSA key, the way GitHub Actions publishes them.
func ciProvider(t *testing.T) (*httptest.Server, *rsa.PrivateKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	var srv *httptest.Server
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/jwks"}) //nolint:errcheck // test server
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{ //nolint:errcheck // test server
			"kty": "RSA", "alg": "RS256", "use": "sig", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, key
}

// ciRoles is the role store of the test: acme's deployer.
type ciRoles struct{}

func (ciRoles) RolePermissions(_ context.Context, tenant, role string) ([]auth.Permission, error) {
	if tenant == "acme" && role == "deployer" {
		return []auth.Permission{{Action: "read", Resource: "dag"}, {Action: "deploy", Resource: "dag"}}, nil
	}
	return nil, auth.ErrRoleNotFound
}

// TestCIOIDCWiringFromConfigToSession drives the documented path: the
// issuers come from DEXAFLOW_AUTH_CI_OIDC_ISSUERS, withCIOIDC wires them
// into the API, a job's token is exchanged, and the session reads the
// deploy defaults as acme, although the token claims another tenant.
func TestCIOIDCWiringFromConfigToSession(t *testing.T) {
	provider, key := ciProvider(t)
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("s", 40))
	t.Setenv("DEXAFLOW_AUTH_CI_OIDC_ISSUERS", `[{"name":"github-acme","provider":"github","issuer":"`+provider.URL+
		`","audience":"dexaflow-acme","tenant":"acme","rules":[{"claims":{"repository":"acme/dags"},"role":"deployer"}]}]`)
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	jwtAuthn := auth.NewJWTAuthenticator(nil, cfg.Auth.JWT.Secret, time.Hour)
	deps := withCIOIDC(api.Dependencies{
		Observability: api.ObservabilityDeps{
			Logger: logger,
		},
		Auth: api.AuthDeps{
			Authenticator: jwtAuthn,
			RateLimiter:   auth.NewRateLimiter(5, time.Minute),
			JWTSecret:     cfg.Auth.JWT.Secret,
		},
		Execution: api.ExecutionDeps{
			DeployDefaults: domain.DeployDefaults{Registry: "ghcr.io/acme"},
		},
	}, cfg, ciRoles{}, nil)
	srv := apitest.NewServer(deps)

	now := time.Now()
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": provider.URL, "aud": "dexaflow-acme", "sub": "repo:acme/dags:ref:refs/heads/main",
		"repository": "acme/dags", "tenant_id": "globex",
		"iat": now.Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	tok.Header["kid"] = "k1"
	raw, err := tok.SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/api/v2/auth/ciToken", strings.NewReader(`{"token":"`+raw+`"}`))
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("exchange = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var session struct {
		AccessToken string `json:"access_token"`
		Tenant      string `json:"tenant"`
	}
	if err = json.Unmarshal(rec.Body.Bytes(), &session); err != nil {
		t.Fatal(err)
	}
	if session.Tenant != "acme" {
		t.Errorf("session tenant = %q, want acme (the entry's, not the token's tenant_id)", session.Tenant)
	}

	req = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/deployDefaults", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)
	rec = httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"name":"github-acme"`) {
		t.Errorf("deployDefaults with the session = %d %s, want 200 listing github-acme", rec.Code, rec.Body.String())
	}
	// The other credentials still go through: a user JWT authenticates.
	userTok, err := auth.MintUserToken(cfg.Auth.JWT.Secret, time.Hour, auth.User{ID: auth.DevTokenSubject, TenantID: "acme", Roles: []string{"admin"}})
	if err != nil {
		t.Fatal(err)
	}
	if u, err := deps.Auth.Authenticator.Authenticate(context.Background(), userTok); err != nil || u.TenantID != "acme" {
		t.Errorf("a user token through the wrapped authenticator: %+v, %v", u, err)
	}
}

// TestCIOIDCWiringIsOffByDefault: without issuers nothing changes.
func TestCIOIDCWiringIsOffByDefault(t *testing.T) {
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("s", 40))
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	authn := auth.NewJWTAuthenticator(nil, cfg.Auth.JWT.Secret, time.Hour)
	deps := withCIOIDC(api.Dependencies{Auth: api.AuthDeps{Authenticator: authn}}, cfg, ciRoles{}, nil)
	if deps.Issuers.CIOIDC != nil || deps.Issuers.CIOIDCRoles != nil || deps.Auth.Authenticator != auth.Authenticator(authn) {
		t.Errorf("deps = %+v, want unchanged", deps)
	}
}
