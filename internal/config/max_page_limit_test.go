package config

import "testing"

// TestMaxPageLimitDefaultsToUncapped pins the gate's default (ADR 0062): no cap
// on a list endpoint's limit unless an operator sets one.
func TestMaxPageLimitDefaultsToUncapped(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.MaxPageLimit != 0 {
		t.Fatalf("server.max_page_limit = %d, want 0 (uncapped)", c.Server.MaxPageLimit)
	}
}

// TestMaxPageLimitBindsBothPrefixes pins the env-only Helm path under the
// current and the legacy prefix.
func TestMaxPageLimitBindsBothPrefixes(t *testing.T) {
	for _, env := range []string{"DEXAFLOW_SERVER_MAX_PAGE_LIMIT", "LEOFLOW_SERVER_MAX_PAGE_LIMIT"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "250")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if c.Server.MaxPageLimit != 250 {
				t.Fatalf("%s=250 gave server.max_page_limit = %d", env, c.Server.MaxPageLimit)
			}
		})
	}
}
