package config

import (
	"testing"
	"time"
)

// TestHTTPTimeoutsDefaultOff pins the gate's default (ADR 0062): no read or
// idle timeout unless an operator sets one, exactly as before.
func TestHTTPTimeoutsDefaultOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Server.ReadTimeout != 0 || c.Server.IdleTimeout != 0 {
		t.Fatalf("read_timeout=%v idle_timeout=%v, want 0 and 0", c.Server.ReadTimeout, c.Server.IdleTimeout)
	}
}

// TestHTTPTimeoutsBindBothPrefixes pins the env-only Helm path under the
// current and the legacy prefix, parsed as durations.
func TestHTTPTimeoutsBindBothPrefixes(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"SERVER_READ_TIMEOUT", "2m")
			t.Setenv(prefix+"SERVER_IDLE_TIMEOUT", "90s")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer() error = %v", err)
			}
			if c.Server.ReadTimeout != 2*time.Minute || c.Server.IdleTimeout != 90*time.Second {
				t.Fatalf("read_timeout=%v idle_timeout=%v, want 2m0s and 1m30s", c.Server.ReadTimeout, c.Server.IdleTimeout)
			}
		})
	}
}

// TestHTTPTimeoutsRejectNegative pins boot validation: a negative duration is
// a typo, not "off".
func TestHTTPTimeoutsRejectNegative(t *testing.T) {
	c := validServerConfig(t)
	c.Server.ReadTimeout = -time.Second
	if err := c.Validate(); err == nil {
		t.Error("negative server.read_timeout passed validation")
	}
	c = validServerConfig(t)
	c.Server.IdleTimeout = -time.Second
	if err := c.Validate(); err == nil {
		t.Error("negative server.idle_timeout passed validation")
	}
}

func validServerConfig(t *testing.T) *ServerConfig {
	t.Helper()
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", "a-test-secret-that-is-long-enough-123")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("baseline config invalid: %v", err)
	}
	return c
}
