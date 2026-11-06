package config

import (
	"strings"
	"testing"
	"time"
)

// executor.pending_timeout_seconds is off by default, so an unschedulable task
// pod waits for a node as before (#1609).
func TestPendingTimeoutDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if c.Executor.PendingTimeoutSeconds != 0 || c.Executor.PendingTimeout() != 0 {
		t.Errorf("pending_timeout_seconds = %d, want 0", c.Executor.PendingTimeoutSeconds)
	}
}

// Both prefixes bind it, the new one first.
func TestPendingTimeoutReadsBothPrefixes(t *testing.T) {
	t.Setenv("LEOFLOW_EXECUTOR_PENDING_TIMEOUT_SECONDS", "600")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if got := c.Executor.PendingTimeout(); got != 10*time.Minute {
		t.Errorf("legacy prefix: PendingTimeout() = %s, want 10m", got)
	}
	t.Setenv("DEXAFLOW_EXECUTOR_PENDING_TIMEOUT_SECONDS", "900")
	c, err = LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	if got := c.Executor.PendingTimeout(); got != 15*time.Minute {
		t.Errorf("new prefix: PendingTimeout() = %s, want 15m", got)
	}
}

// A negative value is refused at boot with a message that names the key.
func TestPendingTimeoutNegativeRefused(t *testing.T) {
	t.Setenv("DEXAFLOW_EXECUTOR_PENDING_TIMEOUT_SECONDS", "-1")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer() error = %v", err)
	}
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "executor.pending_timeout_seconds") {
		t.Fatalf("Validate() = %v, want a refusal naming executor.pending_timeout_seconds", err)
	}
}
