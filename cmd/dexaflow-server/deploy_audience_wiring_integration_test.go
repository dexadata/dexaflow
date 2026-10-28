//go:build integration

package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// deployIssuerKeys publishes one RSA key as the trusted issuer's JWKS and
// signs its tokens.
type deployIssuerKeys struct {
	key *rsa.PrivateKey
	url string
}

func newDeployIssuerKeys(t *testing.T) *deployIssuerKeys {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]any{{
			"kty": "RSA", "use": "sig", "alg": "RS256", "kid": "k1",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(srv.Close)
	return &deployIssuerKeys{key: key, url: srv.URL}
}

// bearer signs a bearer for subject in tenant default with aud and scope.
// An empty aud signs the mixed audience [dexaflow-mcp, dexaflow-deploy].
func (k *deployIssuerKeys) bearer(t *testing.T, subject, aud, scope string) string {
	t.Helper()
	now := time.Now()
	var audClaim any = aud
	if aud == "" {
		audClaim = []string{"dexaflow-mcp", "dexaflow-deploy"}
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodRS256, jwt.MapClaims{
		"iss": "https://portal.example.com", "aud": audClaim, "sub": subject, "tenant_id": "default",
		"scope": scope, "iat": now.Add(-10 * time.Second).Unix(), "exp": now.Add(5 * time.Minute).Unix(),
	})
	tok.Header["kid"] = "k1"
	s, err := tok.SignedString(k.key)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestDeployAudienceWiringAgainstPostgres locks the wiring of #1773 through
// the API server the boot path builds: with auth.trusted_issuer.deploy_audience
// set, a password login token, the session cookie, an API token with the
// deploy scope and a bearer of another bearer audience are refused on every
// deploy route, and the issuer's bearer for the deploy audience with
// dexaflow:deploy registers two versions and makes the first current again.
func TestDeployAudienceWiringAgainstPostgres(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	repo := storage.NewRepository(pg)
	suffix := time.Now().UnixNano()
	email := fmt.Sprintf("deploy_aud_%d@example.com", suffix)
	const password = "deployer-password-1"
	if _, err := repo.CreateUser(ctx, "default", email, password, []string{"admin"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	subject := fmt.Sprintf("cd-%d", suffix)
	if _, err := repo.CreateOIDCUser(ctx, "default", "cd_"+email, "issuer:portal", subject, []string{"admin"}); err != nil {
		t.Fatalf("CreateOIDCUser: %v", err)
	}

	keys := newDeployIssuerKeys(t)
	cfg := &config.ServerConfig{}
	cfg.Auth.JWT.Secret = "wiring-secret"
	cfg.Auth.JWT.TokenTTLSeconds = 3600
	cfg.Auth.TrustedIssuer = config.TrustedIssuerSection{
		Name: "portal", Issuer: "https://portal.example.com", JWKSURL: keys.url,
		Audience: "dexaflow-engine", TenantClaim: "tenant_id", AllowedTenants: []string{"default"},
		AllowedOrigins: []string{"https://portal.example.com"}, BearerAudiences: []string{"dexaflow-mcp"},
		DeployAudience: "dexaflow-deploy",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	reg := prometheus.NewRegistry()
	tel := &observability.Telemetry{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  observability.NewMetrics(reg),
		Registry: reg,
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Hour)
	trusted := newTrustedIssuer(ctx, cfg)
	apiSrv, buildErr := buildAPIServer(cfg, tel, authn, pg, repo, nil, nil, nil, nil, api.ExecutorInfo{}, nil, nil, trusted)
	if buildErr != nil {
		t.Fatalf("buildAPIServer: %v", buildErr)
	}
	handler := apiSrv.Handler

	call := func(method, path, bearer, cookie, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		if cookie != "" {
			req.AddCookie(&http.Cookie{Name: "_token", Value: cookie})
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	login := call(http.MethodPost, "/auth/token", "", "", fmt.Sprintf(`{"username":%q,"password":%q}`, email, password))
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	created := call(http.MethodPost, "/api/v2/apiTokens", session.AccessToken, "", `{"name":"ci-deploy-aud","scope":"deploy","expires_in_seconds":86400}`)
	var apiTok struct {
		Token string `json:"token"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &apiTok) != nil {
		t.Fatalf("create token = %d %s", created.Code, created.Body.String())
	}

	dagID := fmt.Sprintf("deploy_aud_%d", suffix)
	spec := func(v string) string {
		return fmt.Sprintf(`{"schema_version":"1.0","dag_id":%q,"dag_version":%q,"image":"img:%s",`+
			`"tasks":[{"task_id":"a","type":"python","entrypoint":"dag:a"}]}`, dagID, v, v)
	}
	versions := "/api/v2/dags/" + dagID + "/versions"
	routes := []struct{ method, path, body string }{
		{http.MethodPost, versions, spec("v1")},
		{http.MethodPost, versions + "/dry_run", spec("v1")},
		{http.MethodPut, "/api/v2/dags/" + dagID + "/currentVersion", `{"version":"v1"}`},
	}
	others := []struct{ name, bearer, cookie string }{
		{"password token", session.AccessToken, ""},
		{"session cookie", "", session.AccessToken},
		{"deploy API token", apiTok.Token, ""},
		{"other bearer audience", keys.bearer(t, subject, "dexaflow-mcp", "dexaflow:read dexaflow:deploy"), ""},
		{"mixed audience", keys.bearer(t, subject, "", "dexaflow:deploy"), ""},
	}
	for _, o := range others {
		for _, r := range routes {
			rec := call(r.method, r.path, o.bearer, o.cookie, r.body)
			if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "deploys go through the trusted issuer") {
				t.Errorf("%s: %s %s = %d %s, want 403 saying deploys go through the trusted issuer", o.name, r.method, r.path, rec.Code, rec.Body.String())
			}
		}
	}

	deploy := keys.bearer(t, subject, "dexaflow-deploy", "dexaflow:deploy")
	for _, v := range []string{"v1", "v2"} {
		if rec := call(http.MethodPost, versions, deploy, "", spec(v)); rec.Code != http.StatusCreated {
			t.Fatalf("register %s with the deploy audience = %d %s, want 201", v, rec.Code, rec.Body.String())
		}
	}
	rec := call(http.MethodPut, "/api/v2/dags/"+dagID+"/currentVersion", deploy, "", `{"version":"v1"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"changed":true`) {
		t.Errorf("set current version with the deploy audience = %d %s, want 200 changed", rec.Code, rec.Body.String())
	}
	// Reads and run control are unchanged: the password token still reads.
	if rec := call(http.MethodGet, "/api/v2/dags/"+dagID, session.AccessToken, "", ""); rec.Code != http.StatusOK {
		t.Errorf("read the DAG with a password token = %d %s, want 200", rec.Code, rec.Body.String())
	}
}
