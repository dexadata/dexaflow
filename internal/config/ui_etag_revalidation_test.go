package config

import "testing"

// TestUIETagRevalidationDefaultsOff pins the gate's default (ADR 0062): a config
// that never mentions ui.etag_revalidation keeps no-store on every UI route.
func TestUIETagRevalidationDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.UI.ETagRevalidation {
		t.Fatal("ui.etag_revalidation defaults to true, want false")
	}
}

// TestUIETagRevalidationBindsBothPrefixes pins that the gate is reachable from
// the env-only Helm path under the current and the legacy prefix.
func TestUIETagRevalidationBindsBothPrefixes(t *testing.T) {
	for _, env := range []string{"DEXAFLOW_UI_ETAG_REVALIDATION", "LEOFLOW_UI_ETAG_REVALIDATION"} {
		t.Run(env, func(t *testing.T) {
			t.Setenv(env, "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if !c.UI.ETagRevalidation {
				t.Fatalf("%s=true did not enable ui.etag_revalidation", env)
			}
		})
	}
}
