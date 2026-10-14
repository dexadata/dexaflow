package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

// deploy.require_digest (#1703) is off unless an operator sets it, so an
// upgrade registers exactly what it registered before.
func TestRequireDigestOffByDefault(t *testing.T) {
	c, err := LoadServer("", nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Deploy.RequireDigest != domain.DigestRequireOff {
		t.Errorf("deploy.require_digest = %q, want %q", c.Deploy.RequireDigest, domain.DigestRequireOff)
	}
}

func TestRequireDigestEnvBindsBothPrefixes(t *testing.T) {
	for prefix, want := range map[string]domain.DigestRequirement{
		"DEXAFLOW_": domain.DigestRequireEnforce, "LEOFLOW_": domain.DigestRequireWarn,
	} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"DEPLOY_REQUIRE_DIGEST", string(want))
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatalf("LoadServer: %v", err)
			}
			if c.Deploy.RequireDigest != want {
				t.Errorf("deploy.require_digest = %q, want %q from %sDEPLOY_REQUIRE_DIGEST", c.Deploy.RequireDigest, want, prefix)
			}
		})
	}
}

// A YAML 1.2 file reads a bare off as the string, which is what the docs show.
func TestRequireDigestFromConfigFile(t *testing.T) {
	for _, mode := range []string{"off", "warn", "enforce"} {
		path := filepath.Join(t.TempDir(), "server.yaml")
		if err := os.WriteFile(path, []byte("deploy:\n  require_digest: "+mode+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		c, err := LoadServer(path, nil)
		if err != nil {
			t.Fatalf("LoadServer(%s): %v", mode, err)
		}
		if string(c.Deploy.RequireDigest) != mode {
			t.Errorf("deploy.require_digest = %q, want %q from the file", c.Deploy.RequireDigest, mode)
		}
	}
}

// Any other value fails at boot, naming the key and what it accepts, instead
// of silently registering unpinned images.
func TestValidateRequireDigest(t *testing.T) {
	for value, ok := range map[domain.DigestRequirement]bool{
		"": true, "off": true, "warn": true, "enforce": true, "refuse": false, "on": false, "true": false,
	} {
		c := validWarmConfig()
		c.Deploy.RequireDigest = value
		err := c.Validate()
		if ok && err != nil {
			t.Errorf("Validate(require_digest=%q) = %v, want nil", value, err)
		}
		if !ok && (err == nil || !strings.Contains(err.Error(), "deploy.require_digest") || !strings.Contains(err.Error(), "enforce")) {
			t.Errorf("Validate(require_digest=%q) = %v, want an error naming deploy.require_digest and its values", value, err)
		}
	}
}

// The process boots through LoadServer then Validate: a typo in the variable's
// value fails the boot.
func TestRequireDigestInvalidEnvFailsBoot(t *testing.T) {
	t.Setenv("DEXAFLOW_DEPLOY_REQUIRE_DIGEST", "strict")
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", "set")
	c, err := LoadServer("", nil)
	if err == nil {
		err = c.Validate()
	}
	if err == nil || !strings.Contains(err.Error(), "deploy.require_digest") {
		t.Errorf("boot with deploy.require_digest=strict = %v, want an error naming the key", err)
	}
}

// A bare false in a config file is YAML for no, which reads as off, as the
// chart reads it; true is refused at boot like any other value.
func TestRequireDigestYAMLBoolInConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(path, []byte("deploy:\n  require_digest: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if c.Deploy.RequireDigest != domain.DigestRequireOff {
		t.Errorf("require_digest: false = %q, want off", c.Deploy.RequireDigest)
	}
	if werr := os.WriteFile(path, []byte("deploy:\n  require_digest: true\n"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	c, err = LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	c.Auth.JWT.Secret = "set"
	if verr := c.Validate(); verr == nil || !strings.Contains(verr.Error(), `deploy.require_digest "true"`) {
		t.Errorf("require_digest: true Validate = %v, want a refusal naming the value true", verr)
	}
}
