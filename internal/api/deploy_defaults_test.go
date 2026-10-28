package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// deployDefaultsServer is the full API with the given cluster deploy defaults
// and the given signed-in user.
func deployDefaultsServer(user *auth.User, d domain.DeployDefaults) http.Handler {
	return newTestServer(Dependencies{
		Observability: ObservabilityDeps{
			Logger: discardLogger(),
		},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: user},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
			TokenTTLSecs:  900,
		},
		Execution: ExecutionDeps{
			DeployDefaults: d,
		},
	})
}

func getDeployDefaults(srv http.Handler, bearer string) *httptest.ResponseRecorder {
	req := httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/api/v2/deployDefaults", http.NoBody)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	srv.ServeHTTP(rec, req)
	return rec
}

// TestDeployDefaultsServesTheConfiguredValues: a user who may read DAGs gets
// the cluster's deploy defaults, through the server NewServer builds.
func TestDeployDefaultsServesTheConfiguredValues(t *testing.T) {
	want := domain.DeployDefaults{Registry: "ghcr.io/acme", Platforms: []string{"linux/amd64", "linux/arm64"}, BaseImage: "ghcr.io/acme/base:1"}
	viewer := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"viewer"}, Permissions: []auth.Permission{{Action: "read", Resource: "dag"}}}
	rec := getDeployDefaults(deployDefaultsServer(viewer, want), "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	var got domain.DeployDefaults
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if got.Registry != want.Registry || got.BaseImage != want.BaseImage || !slices.Equal(got.Platforms, want.Platforms) {
		t.Errorf("body = %+v, want %+v", got, want)
	}
}

// TestDeployDefaultsUnsetServesEmptyFields: a cluster that sets none answers
// every field empty, platforms an empty list rather than null, so a client
// reads "nothing advertised" without special cases.
func TestDeployDefaultsUnsetServesEmptyFields(t *testing.T) {
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	rec := getDeployDefaults(deployDefaultsServer(admin, domain.DeployDefaults{}), "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	if got, want := rec.Body.String(), `{"registry":"","platforms":[],"base_image":"","source_mode_image":"",`+
		`"remote_builder":null,"task_service_account":"","warm_pools":null,"ci_oidc":null}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// TestDeployDefaultsNeedsSignInAndReadDag: the route is not public, and a
// user whose roles grant no read on DAGs is refused.
func TestDeployDefaultsNeedsSignInAndReadDag(t *testing.T) {
	d := domain.DeployDefaults{Registry: "ghcr.io/acme"}
	if rec := getDeployDefaults(deployDefaultsServer(&auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}, d), ""); rec.Code != http.StatusUnauthorized {
		t.Errorf("no bearer = %d %s, want 401", rec.Code, rec.Body.String())
	}
	noRoles := &auth.User{ID: "u2", TenantID: "default"}
	if rec := getDeployDefaults(deployDefaultsServer(noRoles, d), "tok"); rec.Code != http.StatusForbidden {
		t.Errorf("user without read:dag = %d %s, want 403", rec.Code, rec.Body.String())
	}
}

// TestDeployDefaultsServesTheSourceModeImage (#1772): the runtime image the
// cluster advertises for source mode is served as source_mode_image.
func TestDeployDefaultsServesTheSourceModeImage(t *testing.T) {
	const runtime = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	rec := getDeployDefaults(deployDefaultsServer(admin, domain.DeployDefaults{SourceModeImage: runtime}), "tok")
	var got map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	if got["source_mode_image"] != runtime {
		t.Errorf("source_mode_image = %v, want %q (body %s)", got["source_mode_image"], runtime, rec.Body.String())
	}
}

// TestDeployDefaultsServesBuilderServiceAccountAndWarmPools (#1890): the
// remote builder, the task service account and the warm pools the cluster
// advertises are served in the documented shape, next to the older fields.
func TestDeployDefaultsServesBuilderServiceAccountAndWarmPools(t *testing.T) {
	d := domain.DeployDefaults{
		Registry:           "ghcr.io/acme",
		RemoteBuilder:      &domain.RemoteBuilderConfig{CloudBuild: &domain.CloudBuildConfig{Project: "acme-ci", Region: "us-central1"}},
		TaskServiceAccount: "dexaflow-task",
		WarmPools:          &domain.WarmPoolDefaults{Enabled: true, MinIdleWorkers: 1, MaxPoolSize: 8},
	}
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	rec := getDeployDefaults(deployDefaultsServer(admin, d), "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	want := `{"registry":"ghcr.io/acme","platforms":[],"base_image":"","source_mode_image":"",` +
		`"remote_builder":{"cloud_build":{"project":"acme-ci","region":"us-central1"}},` +
		`"task_service_account":"dexaflow-task","warm_pools":{"enabled":true,"min_idle_workers":1,"max_pool_size":8},"ci_oidc":null}`
	if got := rec.Body.String(); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

// TestDeployDefaultsServesTheInClusterBuilder (#1883): the in-cluster
// builder is served under remote_builder.buildkit with its namespace and pod
// selector, and never a kubeconfig context, which only a client can name.
func TestDeployDefaultsServesTheInClusterBuilder(t *testing.T) {
	d := domain.DeployDefaults{RemoteBuilder: &domain.RemoteBuilderConfig{BuildKit: &domain.BuildKitConfig{
		Namespace: "dexaflow-builder", PodSelector: "app.kubernetes.io/instance=df,app.kubernetes.io/component=builder"}}}
	admin := &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}
	rec := getDeployDefaults(deployDefaultsServer(admin, d), "tok")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d %s, want 200", rec.Code, rec.Body.String())
	}
	want := `"remote_builder":{"buildkit":{"namespace":"dexaflow-builder","pod_selector":"app.kubernetes.io/instance=df,app.kubernetes.io/component=builder"}}`
	if got := rec.Body.String(); !strings.Contains(got, want) || strings.Contains(got, "context") {
		t.Errorf("body = %s, want %s and no context", got, want)
	}
}
