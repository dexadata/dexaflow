//go:build integration

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.opentelemetry.io/otel/trace/noop"

	"github.com/dexadata/dexaflow/internal/api"
	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/observability"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestDeployDefaultsWiringAgainstPostgres locks the wiring (#1704): the
// deploy.defaults the server loads from its environment reach
// GET /api/v2/deployDefaults through the API server the boot path builds,
// where a deploy-scoped API token reads them and an anonymous call is refused.
func TestDeployDefaultsWiringAgainstPostgres(t *testing.T) {
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
	email := fmt.Sprintf("dd_%d@example.com", time.Now().UnixNano())
	const password = "deploy-defaults-pw-1"
	if _, cerr := repo.CreateUser(ctx, "default", email, password, []string{"deployer"}); cerr != nil {
		t.Fatalf("CreateUser: %v", cerr)
	}

	t.Setenv("DEXAFLOW_DEPLOY_DEFAULTS_REGISTRY", "ghcr.io/acme")
	t.Setenv("DEXAFLOW_DEPLOY_DEFAULTS_PLATFORMS", "linux/amd64,linux/arm64")
	t.Setenv("DEXAFLOW_DEPLOY_DEFAULTS_BASE_IMAGE", "ghcr.io/acme/base:1")
	// Source mode advertised (#1772): the served image is the server's own
	// execution.source_mode.image.
	const runtime = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	t.Setenv("DEXAFLOW_EXECUTION_SOURCE_MODE_ENABLED", "true")
	t.Setenv("DEXAFLOW_EXECUTION_SOURCE_MODE_IMAGE", runtime)
	t.Setenv("DEXAFLOW_DEPLOY_DEFAULTS_SOURCE_MODE", "true")
	cfg, err := config.LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	cfg.Auth.JWT.Secret = "wiring-secret"
	cfg.Auth.JWT.TokenTTLSeconds = 3600
	reg := prometheus.NewRegistry()
	tel := &observability.Telemetry{
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
		Metrics:  observability.NewMetrics(reg),
		Registry: reg,
		Tracer:   noop.NewTracerProvider().Tracer("test"),
	}
	authn := auth.NewJWTAuthenticator(repo, cfg.Auth.JWT.Secret, time.Hour)
	handler := buildAPIServer(cfg, tel, authn, pg, repo, nil, nil, nil, nil, api.ExecutorInfo{}, nil, nil, nil).Handler

	call := func(method, path, bearer, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequestWithContext(ctx, method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		rec := httptest.NewRecorder()
		handler.ServeHTTP(rec, req)
		return rec
	}

	login := call(http.MethodPost, "/auth/token", "", fmt.Sprintf(`{"username":%q,"password":%q}`, email, password))
	var session struct {
		AccessToken string `json:"access_token"`
	}
	if login.Code != http.StatusOK || json.Unmarshal(login.Body.Bytes(), &session) != nil {
		t.Fatalf("login = %d %s", login.Code, login.Body.String())
	}
	created := call(http.MethodPost, "/api/v2/apiTokens", session.AccessToken, `{"name":"dd-wiring","scope":"deploy","expires_in_seconds":86400}`)
	var tok struct {
		Token string `json:"token"`
	}
	if created.Code != http.StatusCreated || json.Unmarshal(created.Body.Bytes(), &tok) != nil {
		t.Fatalf("create token = %d %s", created.Code, created.Body.String())
	}

	rec := call(http.MethodGet, "/api/v2/deployDefaults", tok.Token, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deployDefaults with a deploy token = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var got domain.DeployDefaults
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if got.Registry != "ghcr.io/acme" || got.BaseImage != "ghcr.io/acme/base:1" ||
		!slices.Equal(got.Platforms, []string{"linux/amd64", "linux/arm64"}) {
		t.Errorf("deployDefaults = %+v, want the values from the environment", got)
	}
	if got.SourceModeImage != runtime {
		t.Errorf("deployDefaults source_mode_image = %q, want the server's runtime image %q", got.SourceModeImage, runtime)
	}
	if rec := call(http.MethodGet, "/api/v2/deployDefaults", "", ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("anonymous deployDefaults = %d %s, want 401", rec.Code, rec.Body.String())
	}
}
