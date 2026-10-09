package config

import "testing"

// TestLoadServerAlertEgressDefaultsOff: the alert egress guard is opt-in, so an
// existing install that posts alerts to an in-cluster endpoint keeps working.
func TestLoadServerAlertEgressDefaultsOff(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Scheduler.Alerts.BlockPrivateDestinations {
		t.Error("scheduler.alerts.block_private_destinations defaults to true, want false")
	}
	if len(c.Scheduler.Alerts.AllowedCIDRs) != 0 {
		t.Errorf("scheduler.alerts.allowed_cidrs = %v, want empty", c.Scheduler.Alerts.AllowedCIDRs)
	}
}

// TestLoadServerBindsAlertEgressFromEnv: env is the only override path a Helm
// install has, and the CIDR list arrives comma-joined.
func TestLoadServerBindsAlertEgressFromEnv(t *testing.T) {
	t.Setenv("DEXAFLOW_SCHEDULER_ALERTS_BLOCK_PRIVATE_DESTINATIONS", "true")
	t.Setenv("DEXAFLOW_SCHEDULER_ALERTS_ALLOWED_CIDRS", "10.20.0.0/16,192.168.5.7")
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Scheduler.Alerts.BlockPrivateDestinations {
		t.Error("block_private_destinations did not bind from env")
	}
	want := []string{"10.20.0.0/16", "192.168.5.7"}
	got := c.Scheduler.Alerts.AllowedCIDRs
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("allowed_cidrs = %#v, want %#v", got, want)
	}
}

// TestServerConfigValidateRejectsABadAlertCIDR: a typo in the allow list fails
// at startup rather than silently allowing nothing.
func TestServerConfigValidateRejectsABadAlertCIDR(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Auth.JWT.Secret = "set"
	c.Scheduler.Alerts.AllowedCIDRs = []string{"10.0.0.0/33"}
	if err := c.Validate(); err == nil {
		t.Error("Validate() = nil with an invalid scheduler.alerts.allowed_cidrs entry, want error")
	}
	c.Scheduler.Alerts.AllowedCIDRs = []string{"10.0.0.0/8"}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() = %v with a valid CIDR, want nil", err)
	}
}
