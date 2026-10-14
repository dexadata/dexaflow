package config

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deploy.defaults (#1704): what the cluster tells `dexaflow deploy`. Unset by
// default, so an upgrade advertises nothing.

func TestDeployDefaultsUnsetByDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Deploy.Defaults.IsEmpty() {
		t.Errorf("deploy.defaults = %+v, want empty", c.Deploy.Defaults)
	}
}

func TestDeployDefaultsEnvBinds(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_REGISTRY", "ghcr.io/acme")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_PLATFORMS", "linux/amd64,linux/arm64")
			t.Setenv(prefix+"DEPLOY_DEFAULTS_BASE_IMAGE", "ghcr.io/acme/base:1")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			got := c.Deploy.Defaults
			if got.Registry != "ghcr.io/acme" || got.BaseImage != "ghcr.io/acme/base:1" ||
				!slices.Equal(got.Platforms, []string{"linux/amd64", "linux/arm64"}) {
				t.Errorf("deploy.defaults = %+v, want the values from %s variables", got, prefix)
			}
		})
	}
}

func TestDeployDefaultsFromConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	body := "deploy:\n  defaults:\n    registry: ghcr.io/acme\n    platforms: [linux/arm64]\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Deploy.Defaults.Registry != "ghcr.io/acme" || !slices.Equal(c.Deploy.Defaults.Platforms, []string{"linux/arm64"}) {
		t.Errorf("deploy.defaults = %+v, want the file's values", c.Deploy.Defaults)
	}
}

// An invalid default fails at boot, naming the key, instead of reaching every
// deploy of the cluster.
func TestValidateDeployDefaults(t *testing.T) {
	cases := map[string]struct {
		d       domain.DeployDefaults
		wantErr string
	}{
		"unset":           {domain.DeployDefaults{}, ""},
		"all set":         {domain.DeployDefaults{Registry: "ghcr.io/acme", Platforms: []string{"linux/amd64"}, BaseImage: "python:3.12-slim"}, ""},
		"registry scheme": {domain.DeployDefaults{Registry: "https://ghcr.io/acme"}, "deploy.defaults.registry"},
		"bad platform":    {domain.DeployDefaults{Platforms: []string{"x86"}}, "deploy.defaults.platforms"},
		"bad base image":  {domain.DeployDefaults{BaseImage: "a b"}, "deploy.defaults.base_image"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := validWarmConfig()
			c.Deploy.Defaults = tc.d
			err := c.Validate()
			switch {
			case tc.wantErr == "" && err != nil:
				t.Errorf("Validate() = %v, want nil", err)
			case tc.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tc.wantErr)):
				t.Errorf("Validate() = %v, want an error naming %q", err, tc.wantErr)
			}
		})
	}
}
