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
			c.Deploy.Defaults.DeployDefaults = tc.d
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

// runtimeImage is a source-mode runtime image pinned by a full digest.
const runtimeImage = "ghcr.io/dexadata/dexaflow-runtime:0.5.3@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// TestDeployDefaultsSourceModeEnvBinds (#1772): deploy.defaults.source_mode
// is off by default and binds from both variable prefixes.
func TestDeployDefaultsSourceModeEnvBinds(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Deploy.Defaults.SourceMode {
		t.Error("deploy.defaults.source_mode is on by default")
	}
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_SOURCE_MODE", "true")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if !c.Deploy.Defaults.SourceMode {
				t.Errorf("deploy.defaults.source_mode not bound from %sDEPLOY_DEFAULTS_SOURCE_MODE", prefix)
			}
		})
	}
}

// TestAdvertisedDeployDefaults: the cluster advertises its runtime image for
// source mode only when the operator asks for it and the mode is on, and the
// value is execution.source_mode.image itself, never a copy to keep in step.
func TestAdvertisedDeployDefaults(t *testing.T) {
	cases := map[string]struct {
		advertise, enabled bool
		want               string
	}{
		"advertised":          {advertise: true, enabled: true, want: runtimeImage},
		"not asked":           {advertise: false, enabled: true, want: ""},
		"asked, mode off":     {advertise: true, enabled: false, want: ""},
		"mode off, not asked": {advertise: false, enabled: false, want: ""},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := &ServerConfig{}
			c.Deploy.Defaults.Registry = "ghcr.io/acme"
			c.Deploy.Defaults.SourceMode = tc.advertise
			c.Execution.SourceMode = SourceModeSection{Enabled: tc.enabled, Image: runtimeImage}
			got := c.AdvertisedDeployDefaults()
			if got.SourceModeImage != tc.want || got.Registry != "ghcr.io/acme" {
				t.Errorf("AdvertisedDeployDefaults() = %+v, want source_mode_image %q and the registry kept", got, tc.want)
			}
		})
	}
}

// TestValidateDeployDefaultsSourceModeNeedsTheMode: advertising source mode
// while execution.source_mode is off would send every deploy to an image
// the server runs as a plain image, so it fails at boot.
func TestValidateDeployDefaultsSourceModeNeedsTheMode(t *testing.T) {
	c := validWarmConfig()
	c.Deploy.Defaults.SourceMode = true
	err := c.Validate()
	if err == nil || !strings.Contains(err.Error(), "deploy.defaults.source_mode") || !strings.Contains(err.Error(), "execution.source_mode.enabled") {
		t.Errorf("Validate() = %v, want an error naming deploy.defaults.source_mode and execution.source_mode.enabled", err)
	}
	c.Execution.SourceMode = SourceModeSection{Enabled: true, Image: runtimeImage}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate() with the mode on = %v, want nil", err)
	}
}

// TestLoadServerRefusesASourceModeImage: the advertised runtime image is
// always execution.source_mode.image (deploy.defaults.source_mode turns it
// on), so a deploy.defaults.source_mode_image, which nothing reads, is
// refused at boot rather than ignored.
func TestLoadServerRefusesASourceModeImage(t *testing.T) {
	check := func(t *testing.T, err error) {
		t.Helper()
		if err == nil || !strings.Contains(err.Error(), "deploy.defaults.source_mode_image") || !strings.Contains(err.Error(), "deploy.defaults.source_mode") ||
			!strings.Contains(err.Error(), "execution.source_mode.image") {
			t.Errorf("LoadServer() = %v, want a refusal naming deploy.defaults.source_mode_image and what to set instead", err)
		}
	}
	t.Run("file", func(t *testing.T) {
		p := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(p, []byte("deploy:\n  defaults:\n    source_mode_image: "+runtimeImage+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, err := LoadServer(p, nil)
		check(t, err)
	})
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_DEFAULTS_SOURCE_MODE_IMAGE", runtimeImage)
			_, err := LoadServer("", nil)
			check(t, err)
		})
	}
}
