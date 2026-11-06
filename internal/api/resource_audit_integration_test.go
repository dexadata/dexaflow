//go:build integration

package api

import (
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// TestResourceWritesReachTheEventLogIntegration drives the real handlers over
// the real Repository: a variable write and a pool resize land in the tenant's
// event log with the acting admin as owner and without the variable's value
// (#2228), and the other tenant's event log stays without them.
func TestResourceWritesReachTheEventLogIntegration(t *testing.T) {
	f := newCrossTenantFixture(t)
	if _, err := f.repo.EnsureTenant(f.ctx, f.tenantB, f.tenantB, 0, domain.TenantLimitsUpdate{}); err != nil {
		t.Fatalf("seeding B's roles: %v", err)
	}
	admin, err := f.repo.CreateUser(f.ctx, f.tenantB, "audit_"+f.suffix+"@example.com", "a-Strong-pass-123", []string{"admin"})
	if err != nil {
		t.Fatalf("creating the admin: %v", err)
	}
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Server:        ServerDeps{Pools: true},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: admin.ID, TenantID: f.tenantB, Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
			TokenTTLSecs:  3600,
		},
		Stores: StoreDeps{Variables: f.repo, Pools: f.repo, AuditLog: f.repo, Audit: f.repo},
	})
	key := "audit_var_" + f.suffix
	pool := "audit_pool_" + f.suffix
	xtExpect(t, srv, http.MethodPost, "/api/v2/variables", fmt.Sprintf(`{"key":%q,"value":"value-s3cr3t"}`, key), http.StatusCreated)
	xtExpect(t, srv, http.MethodPost, "/api/v2/pools", fmt.Sprintf(`{"name":%q,"slots":2}`, pool), http.StatusCreated)
	xtExpect(t, srv, http.MethodPatch, "/api/v2/pools/"+pool, `{"slots":4}`, http.StatusOK)

	body := xtExpect(t, srv, http.MethodGet, "/api/v2/eventLogs?limit=1000", "", http.StatusOK)
	for _, want := range []string{"variable.create", "pool.create", "pool.update", key, pool, `\"previous_slots\":\"2\"`, admin.Email} {
		if !strings.Contains(body, want) {
			t.Errorf("event log misses %s: %s", want, clipAudit(body))
		}
	}
	if strings.Contains(body, "s3cr3t") {
		t.Errorf("event log carries the variable's value: %s", clipAudit(body))
	}
	if other := xtExpect(t, f.a, http.MethodGet, "/api/v2/eventLogs?limit=1000", "", http.StatusOK); strings.Contains(other, key) {
		t.Errorf("the other tenant's event log names %s", key)
	}
}

func clipAudit(s string) string {
	if len(s) > 1500 {
		return s[:1500] + "..."
	}
	return s
}
