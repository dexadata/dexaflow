package api

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
)

// TestHostPatternTenantMatchesConfig keeps the placeholder the binding
// replaces equal to the one config validates host_pattern against.
func TestHostPatternTenantMatchesConfig(t *testing.T) {
	if hostPatternTenant != config.HostPatternTenant {
		t.Fatalf("hostPatternTenant = %q, config.HostPatternTenant = %q", hostPatternTenant, config.HostPatternTenant)
	}
}
