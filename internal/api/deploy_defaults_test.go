package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// deployDefaultsServer is the full API with the given cluster deploy defaults
// and the given signed-in user.
func deployDefaultsServer(user *auth.User, d domain.DeployDefaults) http.Handler {
	return NewServer(Dependencies{
		Logger:         discardLogger(),
		Authenticator:  &fakeAuthn{user: user},
		RateLimiter:    auth.NewRateLimiter(100, time.Minute),
		TokenTTLSecs:   900,
		DeployDefaults: d,
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
	if got, want := rec.Body.String(), `{"registry":"","platforms":[],"base_image":""}`; got != want {
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
