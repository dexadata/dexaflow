package config

import "testing"

// TestLoadServerBodyLimitsDefaultOff pins the ADR 0062 gate for #1613: both
// body limits default to 0, so an upgrade keeps request bodies unlimited.
func TestLoadServerBodyLimitsDefaultOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.MaxRequestBodyBytes != 0 || c.Server.MaxUploadBodyBytes != 0 {
		t.Errorf("defaults = %d and %d, want 0 and 0", c.Server.MaxRequestBodyBytes, c.Server.MaxUploadBodyBytes)
	}
}

// TestLoadServerBindsBodyLimitsUnderBothPrefixes pins that the limits bind
// from DEXAFLOW_* and, as the fallback, LEOFLOW_*.
func TestLoadServerBindsBodyLimitsUnderBothPrefixes(t *testing.T) {
	t.Setenv("DEXAFLOW_SERVER_MAX_REQUEST_BODY_BYTES", "1048576")
	t.Setenv("LEOFLOW_SERVER_MAX_UPLOAD_BODY_BYTES", "67108864")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.MaxRequestBodyBytes != 1<<20 || c.Server.MaxUploadBodyBytes != 64<<20 {
		t.Errorf("limits = %d and %d, want %d and %d", c.Server.MaxRequestBodyBytes, c.Server.MaxUploadBodyBytes, 1<<20, 64<<20)
	}
}

// TestServerConfigValidateRefusesNegativeBodyLimits pins that a negative limit
// fails boot instead of silently meaning "no limit".
func TestServerConfigValidateRefusesNegativeBodyLimits(t *testing.T) {
	for _, set := range []func(*ServerConfig){
		func(c *ServerConfig) { c.Server.MaxRequestBodyBytes = -1 },
		func(c *ServerConfig) { c.Server.MaxUploadBodyBytes = -1 },
	} {
		c, err := LoadServer("", nil)
		if err != nil {
			t.Fatal(err)
		}
		c.Auth.JWT.Secret = "set"
		set(c)
		if err := c.Validate(); err == nil {
			t.Error("Validate() = nil with a negative body limit, want error")
		}
	}
}
