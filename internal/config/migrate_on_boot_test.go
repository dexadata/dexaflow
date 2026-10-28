package config

import (
	"strings"
	"testing"
)

// database.migrate_on_boot is off unless an operator turns it on, and binds
// from the environment the chart renders (ADR 0090 decision 4).
func TestMigrateOnBootDefaultsOffAndBinds(t *testing.T) {
	c := validServerConfig(t)
	if c.Database.MigrateOnBoot {
		t.Fatal("database.migrate_on_boot is on by default")
	}
	t.Setenv("LEOFLOW_DATABASE_MIGRATE_ON_BOOT", "true")
	c = validServerConfig(t)
	if !c.Database.MigrateOnBoot {
		t.Fatal("LEOFLOW_DATABASE_MIGRATE_ON_BOOT=true did not bind")
	}
}

// The api role never migrates, whatever its DSN can do: a configuration that
// asks it to is refused at boot rather than ignored.
func TestMigrateOnBootRefusedOnTheAPIRole(t *testing.T) {
	c := validServerConfig(t)
	c.Database.MigrateOnBoot = true
	for _, role := range []string{"", RoleAll, RoleScheduler} {
		c.Server.Role = role
		if err := c.Validate(); err != nil {
			t.Errorf("role %q with migrate_on_boot: %v", role, err)
		}
	}
	c.Server.Role = RoleAPI
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "database.migrate_on_boot") || !strings.Contains(err.Error(), "api") {
		t.Fatalf("Validate = %v, want a refusal naming database.migrate_on_boot and the api role", err)
	}
}
