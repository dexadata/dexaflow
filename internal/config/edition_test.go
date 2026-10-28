package config

import "testing"

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
