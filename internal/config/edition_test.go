package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIsClusterEditionAcceptsBothNames(t *testing.T) {
	tests := []struct {
		edition string
		want    bool
	}{
		{edition: "cluster", want: true},
		{edition: "pro", want: true},
		{edition: "lite", want: false},
		{edition: "demo", want: false},
		{edition: "", want: false},
		{edition: "Cluster", want: false},
	}
	for _, tc := range tests {
		t.Run(tc.edition, func(t *testing.T) {
			// Arrange
			c := &ServerConfig{UI: UISection{Edition: tc.edition}}

			// Act
			got, gotMethod := IsClusterEdition(tc.edition), c.ClusterEdition()

			// Assert
			if got != tc.want || gotMethod != tc.want {
				t.Errorf("IsClusterEdition(%q) = %v, ClusterEdition() = %v, want %v", tc.edition, got, gotMethod, tc.want)
			}
		})
	}
}

func TestServerConfigValidateOIDCAcceptsClusterEdition(t *testing.T) {
	// Arrange
	c := validOIDCConfig()
	c.UI.Edition = EditionCluster

	// Act
	err := c.Validate()

	// Assert
	if err != nil {
		t.Errorf("Validate() = %v for a complete OIDC config on the cluster edition, want nil", err)
	}
}

func TestPoolsEnabledFollowsTheEditionUntilSet(t *testing.T) {
	tests := []struct {
		name    string
		edition string
		env     string
		file    string
		want    bool
	}{
		{name: "cluster unset", edition: "cluster", want: true},
		{name: "pro unset", edition: "pro", want: true},
		{name: "lite unset", edition: "lite", want: false},
		{name: "empty unset", edition: "", want: false},
		{name: "cluster turned off by env", edition: "cluster", env: "false", want: false},
		{name: "lite turned on by env", edition: "lite", env: "true", want: true},
		{name: "cluster turned off by file", edition: "cluster", file: "scheduler:\n  pools:\n    enabled: false\n", want: false},
		{name: "lite turned on by file", edition: "lite", file: "scheduler:\n  pools:\n    enabled: true\n", want: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			t.Setenv("DEXAFLOW_UI_EDITION", tc.edition)
			if tc.env != "" {
				t.Setenv("DEXAFLOW_SCHEDULER_POOLS_ENABLED", tc.env)
			}
			path := ""
			if tc.file != "" {
				path = filepath.Join(t.TempDir(), "server.yaml")
				if err := os.WriteFile(path, []byte(tc.file), 0o600); err != nil {
					t.Fatal(err)
				}
			}

			// Act
			c, err := LoadServer(path, nil)

			// Assert
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if got := c.PoolsEnabled(); got != tc.want {
				t.Errorf("PoolsEnabled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPoolsEnabledRefusesAValueThatIsNotABool(t *testing.T) {
	// Arrange
	t.Setenv("DEXAFLOW_SCHEDULER_POOLS_ENABLED", "maybe")

	// Act
	_, err := LoadServer("", nil)

	// Assert
	if err == nil {
		t.Error("LoadServer accepted scheduler.pools.enabled=maybe, want an error")
	}
}

func TestPoolsEnabledReadsTheLegacyVariable(t *testing.T) {
	// Arrange
	t.Setenv("DEXAFLOW_UI_EDITION", "cluster")
	t.Setenv("LEOFLOW_SCHEDULER_POOLS_ENABLED", "false")

	// Act
	c, err := LoadServer("", nil)

	// Assert
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.PoolsEnabled() {
		t.Error("PoolsEnabled() = true with LEOFLOW_SCHEDULER_POOLS_ENABLED=false, want false")
	}
}
