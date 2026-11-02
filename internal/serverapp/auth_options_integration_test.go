//go:build integration

package serverapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/issuer"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// directoryAuthn stands for an operator's own identity system: it signs in
// one user with one password and recognizes the token it hands out.
type directoryAuthn struct {
	user     *auth.User
	password string
}

func (d directoryAuthn) Authenticate(_ context.Context, token string) (*auth.User, error) {
	if token != "directory:"+d.user.Email {
		return nil, auth.ErrInvalidToken
	}
	return d.user, nil
}

func (d directoryAuthn) IssueToken(_ context.Context, c auth.Credentials) (string, error) {
	if c.Username != d.user.Email || c.Password != d.password {
		return "", auth.ErrInvalidCredentials
	}
	return "directory:" + d.user.Email, nil
}

// TestAuthenticatorOptionWiringAgainstPostgres locks ADR 0099 §3 and §6
// through the API server the boot path builds: password sign-in goes to the
// authenticator from options, its token reads the API, an unknown token is
// refused, and a session the engine minted itself keeps working.
func TestAuthenticatorOptionWiringAgainstPostgres(t *testing.T) {
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
	email := fmt.Sprintf("dir_%d@example.com", time.Now().UnixNano())
	const enginePassword = "engine-password-1"
	if _, err := repo.CreateUser(ctx, "default", email, enginePassword, []string{"admin"}); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	known, _, err := repo.FindUserByLogin(ctx, "default", email)
	if err != nil {
		t.Fatalf("FindUserByLogin: %v", err)
	}

	cfg := &config.ServerConfig{}
	cfg.Auth.JWT.Secret = "wiring-secret"
	cfg.Auth.JWT.TokenTTLSeconds = 3600
	reg := prometheus.NewRegistry()
	tel := &observability.Telemetry{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  observability.NewMetrics(reg),
		Registry: reg,
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}
	engine := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Hour)
	engineSession, err := engine.IssueToken(ctx, auth.Credentials{Tenant: "default", Username: email, Password: enginePassword})
	if err != nil {
		t.Fatalf("engine IssueToken: %v", err)
	}
	dir := directoryAuthn{user: &auth.User{ID: known.ID, TenantID: known.TenantID, Email: email, Roles: []string{"admin"}}, password: "directory-password"}
	apiSrv, buildErr := buildAPIServer(cfg, tel, engine, pg, repo, nil, nil, nil, nil, api.ExecutorInfo{}, nil, nil, apiExtras{authenticator: dir})
	if buildErr != nil {
		t.Fatalf("buildAPIServer: %v", buildErr)
	}
	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		apiSrv.Handler.ServeHTTP(rec, req)
		return rec
	}

	if rec := call(http.MethodPost, "/auth/token", "", fmt.Sprintf(`{"username":%q,"password":%q}`, email, enginePassword)); rec.Code != http.StatusUnauthorized {
		t.Errorf("sign-in with the engine's password = %d %s; password sign-in must go to the authenticator from options", rec.Code, rec.Body.String())
	}
	login := call(http.MethodPost, "/auth/token", "", fmt.Sprintf(`{"username":%q,"password":"directory-password"}`, email))
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil || session.AccessToken != "directory:"+email {
		t.Fatalf("sign-in through the directory = %d %s", login.Code, login.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v2/dags", session.AccessToken, ""); rec.Code != http.StatusOK {
		t.Errorf("list dags with the directory's token = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v2/dags", "directory:someone-else", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("unknown token = %d %s, want 401", rec.Code, rec.Body.String())
	}
	if rec := call(http.MethodGet, "/api/v2/dags", engineSession, ""); rec.Code != http.StatusOK {
		t.Errorf("a session the engine minted = %d %s, want 200", rec.Code, rec.Body.String())
	}
}

// staticIssuer accepts one handoff code for one identity.
type staticIssuer struct {
	code string
	id   auth.User
}

func (staticIssuer) Provider() string { return "portal" }

func (s staticIssuer) Verify(_ context.Context, raw string) (*issuer.Identity, error) {
	if raw != s.code {
		return nil, errors.New("unknown code")
	}
	return &issuer.Identity{Subject: "portal-subject-none", Email: s.id.Email, Tenant: s.id.TenantID}, nil
}

// TestTrustedIssuerOptionWiringAgainstPostgres locks the handoff route of an
// issuer passed as an option: it exists only with the option, takes a post
// only from the option's origins, refuses a code the issuer refuses, and
// never opens a session for an identity no user is linked to.
func TestTrustedIssuerOptionWiringAgainstPostgres(t *testing.T) {
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
	cfg := &config.ServerConfig{}
	cfg.Auth.JWT.Secret = "wiring-secret"
	cfg.Auth.JWT.TokenTTLSeconds = 3600
	reg := prometheus.NewRegistry()
	tel := &observability.Telemetry{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  observability.NewMetrics(reg),
		Registry: reg,
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}
	engine := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Hour)
	const origin = "https://portal.example.com"
	post := func(extras apiExtras, from, code string) *httptest.ResponseRecorder {
		srv, buildErr := buildAPIServer(cfg, tel, engine, pg, repo, nil, nil, nil, nil, api.ExecutorInfo{}, nil, nil, extras)
		if buildErr != nil {
			t.Fatalf("buildAPIServer: %v", buildErr)
		}
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v2/auth/session", strings.NewReader("token="+code))
		req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
		req.Header.Set("Accept", "application/json")
		req.Header.Set("Origin", from)
		rec := httptest.NewRecorder()
		srv.Handler.ServeHTTP(rec, req)
		return rec
	}
	ti := staticIssuer{code: "c-1", id: auth.User{Email: "nobody@example.com", TenantID: "default"}}
	withIssuer := apiExtras{issuer: ti, issuerOrigins: []string{origin}}

	if rec := post(apiExtras{}, origin, "c-1"); rec.Code != http.StatusNotFound {
		t.Errorf("without the option the handoff answered %d %s; want 404: the route must not be served", rec.Code, rec.Body.String())
	}
	if rec := post(withIssuer, "https://elsewhere.example.com", "c-1"); rec.Code != http.StatusForbidden {
		t.Errorf("handoff from another origin = %d %s, want 403", rec.Code, rec.Body.String())
	}
	if rec := post(withIssuer, origin, "c-2"); rec.Code != http.StatusUnauthorized {
		t.Errorf("handoff with a refused code = %d %s, want 401", rec.Code, rec.Body.String())
	}
	rec := post(withIssuer, origin, "c-1")
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "no active account") {
		t.Errorf("handoff for an unlinked identity = %d %s, want 403 with no session", rec.Code, rec.Body.String())
	}
	if len(rec.Result().Cookies()) != 0 {
		t.Errorf("a refused handoff set cookies: %v", rec.Result().Cookies())
	}
}
