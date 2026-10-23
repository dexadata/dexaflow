package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/crypto/bcrypt"
)

// The bootstrap admin and the insecure-secrets escape hatch were read straight
// from LEOFLOW_* before #1952. They default to off and bind from both names.
func TestBootstrapAndAgentDefaultsAreOff(t *testing.T) {
	c := validServerConfig(t)
	if c.Bootstrap != (BootstrapSection{}) || c.Bootstrap.Enabled() {
		t.Errorf("bootstrap defaults = %+v, want empty", c.Bootstrap)
	}
	if c.Agent.AllowInsecureSecrets {
		t.Error("agent.allow_insecure_secrets is on by default")
	}
}

func TestBootstrapAndAgentBindBothPrefixes(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv(prefix+"BOOTSTRAP_EMAIL", "ops@corp.example")
			t.Setenv(prefix+"BOOTSTRAP_PASSWORD", "pw")
			t.Setenv(prefix+"BOOTSTRAP_PASSWORD_HASH", testBcryptHash(t))
			t.Setenv(prefix+"AGENT_ALLOW_INSECURE_SECRETS", "true")
			c := validServerConfig(t)
			if c.Bootstrap.Email != "ops@corp.example" || c.Bootstrap.Password != "pw" || c.Bootstrap.PasswordHash == "" {
				t.Errorf("bootstrap = %v, want every field bound from %s*", c.Bootstrap, prefix)
			}
			if !c.Agent.AllowInsecureSecrets {
				t.Errorf("%sAGENT_ALLOW_INSECURE_SECRETS=true did not bind", prefix)
			}
		})
	}
}

func TestBootstrapNewPrefixWinsOverLegacy(t *testing.T) {
	t.Setenv("DEXAFLOW_BOOTSTRAP_EMAIL", "new@corp.example")
	t.Setenv("LEOFLOW_BOOTSTRAP_EMAIL", "old@corp.example")
	t.Setenv("DEXAFLOW_AGENT_ALLOW_INSECURE_SECRETS", "false")
	t.Setenv("LEOFLOW_AGENT_ALLOW_INSECURE_SECRETS", "true")
	c := validServerConfig(t)
	if c.Bootstrap.Email != "new@corp.example" {
		t.Errorf("bootstrap.email = %q, want the DEXAFLOW_ value", c.Bootstrap.Email)
	}
	if c.Agent.AllowInsecureSecrets {
		t.Error("agent.allow_insecure_secrets took the LEOFLOW_ value over DEXAFLOW_")
	}
}

// Before #1952 only the exact value "true" turned the escape hatch on. A value
// that meant off then still means off: "1" or "TRUE" decoded as a bool would
// turn it on.
func TestAllowInsecureSecretsKeepsItsLegacyMeaning(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW_", "LEOFLOW_"} {
		for val, want := range map[string]bool{
			"": false, "false": false, "true": true,
			"1": false, "TRUE": false, "True": false, "yes": false, "on": false, "t": false,
		} {
			t.Setenv(prefix+"AGENT_ALLOW_INSECURE_SECRETS", val)
			c := validServerConfig(t)
			if c.Agent.AllowInsecureSecrets != want {
				t.Errorf("%sAGENT_ALLOW_INSECURE_SECRETS=%q read as %v, want %v", prefix, val, c.Agent.AllowInsecureSecrets, want)
			}
		}
		t.Setenv(prefix+"AGENT_ALLOW_INSECURE_SECRETS", "")
	}
}

// A YAML bool in the config file decodes as itself.
func TestAllowInsecureSecretsFromTheConfigFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("agent:\n  allow_insecure_secrets: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := LoadServer(path, nil)
	if err != nil {
		t.Fatalf("LoadServer: %v", err)
	}
	if !c.Agent.AllowInsecureSecrets {
		t.Error("allow_insecure_secrets: true in the config file did not bind")
	}
}

func TestValidateBootstrap(t *testing.T) {
	hash := testBcryptHash(t)
	cases := []struct {
		name    string
		b       BootstrapSection
		wantErr string
	}{
		{"unset", BootstrapSection{}, ""},
		{"password", BootstrapSection{Email: "ops@corp.example", Password: "pw"}, ""},
		{"hash", BootstrapSection{PasswordHash: hash}, ""},
		{"hash wins over a long password", BootstrapSection{PasswordHash: hash, Password: strings.Repeat("p", 73)}, ""},
		{"not a bcrypt hash", BootstrapSection{PasswordHash: "plaintext"}, "bootstrap.password_hash"},
		{"password too long for bcrypt", BootstrapSection{Password: strings.Repeat("p", 73)}, "bootstrap.password"},
		{"email with a space", BootstrapSection{Email: " ops@corp.example", Password: "pw"}, "bootstrap.email"},
		{"no credential, nothing checked", BootstrapSection{Email: "not an address"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			c := validServerConfig(t)
			c.Bootstrap = tc.b
			err := c.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("Validate = %v, want nil", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("Validate = %v, want an error naming %s", err, tc.wantErr)
			}
			if tc.b.Password != "" && strings.Contains(err.Error(), tc.b.Password) {
				t.Errorf("the error carries the password: %v", err)
			}
		})
	}
}

// The bootstrap credentials never reach a log line or a formatted config.
func TestBootstrapCredentialsAreRedacted(t *testing.T) {
	c := ServerConfig{Bootstrap: BootstrapSection{Email: "ops@corp.example", Password: "s3cret-pw", PasswordHash: "$2a$10$s3cret-hash"}}
	var logs bytes.Buffer
	slog.New(slog.NewTextHandler(&logs, nil)).Info("config", "bootstrap", c.Bootstrap)
	for name, out := range map[string]string{
		"%v":   fmt.Sprintf("%v", c),
		"%+v":  fmt.Sprintf("%+v", c),
		"%#v":  fmt.Sprintf("%#v", c.Bootstrap),
		"slog": logs.String(),
	} {
		if strings.Contains(out, "s3cret") {
			t.Errorf("%s shows a bootstrap credential: %s", name, out)
		}
		if !strings.Contains(out, "ops@corp.example") {
			t.Errorf("%s drops the bootstrap email: %s", name, out)
		}
	}
}

func testBcryptHash(t *testing.T) string {
	t.Helper()
	h, err := bcrypt.GenerateFromPassword([]byte("pw"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	return string(h)
}
