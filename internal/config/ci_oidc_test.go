package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/cioidc"
)

const ciOIDCYAML = `auth:
  ci_oidc:
    session_ttl_seconds: 1800
    issuers:
      - name: github-acme
        provider: github
        issuer: https://token.actions.githubusercontent.com
        audience: https://dexaflow.acme.example
        tenant: acme
        max_lifetime_seconds: 600
        rules:
          - claims:
              repository: acme/dags
              ref: refs/heads/main
              https://example.com/claims.team: data
            role: deployer
            scopes: [dexaflow:deploy, dexaflow:run]
`

// wantCIOIDCIssuer is what ciOIDCYAML (and its JSON form) configures.
var wantCIOIDCIssuer = cioidc.Issuer{
	Name: "github-acme", Provider: "github", Issuer: "https://token.actions.githubusercontent.com",
	Audience: "https://dexaflow.acme.example", Tenant: "acme", MaxLifetime: 10 * time.Minute,
	Rules: []cioidc.Rule{{
		Claims: map[string]string{"repository": "acme/dags", "ref": "refs/heads/main", "https://example.com/claims.team": "data"},
		Role:   "deployer", Scopes: []string{"dexaflow:deploy", "dexaflow:run"},
	}},
}

const ciOIDCJSON = `[{"name":"github-acme","provider":"github","issuer":"https://token.actions.githubusercontent.com",` +
	`"audience":"https://dexaflow.acme.example","tenant":"acme","max_lifetime_seconds":600,"rules":[{"claims":` +
	`{"repository":"acme/dags","ref":"refs/heads/main","https://example.com/claims.team":"data"},"role":"deployer",` +
	`"scopes":["dexaflow:deploy","dexaflow:run"]}]}]`

func writeConfigFile(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "server.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// TestCIOIDCIsOffByDefault: with nothing set there is no issuer, and the
// session lifetime is one hour.
func TestCIOIDCIsOffByDefault(t *testing.T) {
	c := validServerConfig(t)
	if len(c.Auth.CIOIDC.Issuers) != 0 || c.Auth.CIOIDC.Enabled() {
		t.Errorf("issuers = %+v, want none", c.Auth.CIOIDC.Issuers)
	}
	if c.Auth.CIOIDC.SessionTTLSeconds != 3600 {
		t.Errorf("session_ttl_seconds = %d, want 3600", c.Auth.CIOIDC.SessionTTLSeconds)
	}
}

// TestCIOIDCFromTheConfigFile reads the issuers from YAML, a dotted claim
// name included (viper would split it).
func TestCIOIDCFromTheConfigFile(t *testing.T) {
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("x", 40))
	c, err := LoadServer(writeConfigFile(t, ciOIDCYAML), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Auth.CIOIDC.CIIssuers(); !reflect.DeepEqual(got, []cioidc.Issuer{wantCIOIDCIssuer}) {
		t.Errorf("issuers = %+v\nwant %+v", got, wantCIOIDCIssuer)
	}
	if c.Auth.CIOIDC.SessionTTLSeconds != 1800 {
		t.Errorf("session_ttl_seconds = %d, want 1800", c.Auth.CIOIDC.SessionTTLSeconds)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// TestCIOIDCFromTheEnvironment reads the issuers as JSON from
// DEXAFLOW_AUTH_CI_OIDC_ISSUERS (the chart's route), LEOFLOW_ as the
// fallback, the new name winning, and the variable over the file.
func TestCIOIDCFromTheEnvironment(t *testing.T) {
	for _, prefix := range []string{"DEXAFLOW", "LEOFLOW"} {
		t.Run(prefix, func(t *testing.T) {
			t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("x", 40))
			t.Setenv(prefix+"_AUTH_CI_OIDC_ISSUERS", ciOIDCJSON)
			t.Setenv(prefix+"_AUTH_CI_OIDC_SESSION_TTL_SECONDS", "900")
			c, err := LoadServer("", nil)
			if err != nil {
				t.Fatal(err)
			}
			if got := c.Auth.CIOIDC.CIIssuers(); !reflect.DeepEqual(got, []cioidc.Issuer{wantCIOIDCIssuer}) {
				t.Errorf("issuers = %+v, want %+v", got, wantCIOIDCIssuer)
			}
			if c.Auth.CIOIDC.SessionTTLSeconds != 900 {
				t.Errorf("session_ttl_seconds = %d, want 900", c.Auth.CIOIDC.SessionTTLSeconds)
			}
		})
	}

	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("x", 40))
	t.Setenv("LEOFLOW_AUTH_CI_OIDC_ISSUERS", `[]`)
	t.Setenv("DEXAFLOW_AUTH_CI_OIDC_ISSUERS", ciOIDCJSON)
	c, err := LoadServer(writeConfigFile(t, "auth:\n  ci_oidc:\n    issuers: []\n"), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Auth.CIOIDC.Issuers) != 1 {
		t.Errorf("DEXAFLOW_ over LEOFLOW_ and over the file: %d issuers, want 1", len(c.Auth.CIOIDC.Issuers))
	}
}

func TestCIOIDCEnvironmentThatIsNotJSONFailsToLoad(t *testing.T) {
	t.Setenv("DEXAFLOW_AUTH_CI_OIDC_ISSUERS", "github-acme")
	if _, err := LoadServer("", nil); err == nil || !strings.Contains(err.Error(), "DEXAFLOW_AUTH_CI_OIDC_ISSUERS") {
		t.Errorf("err = %v, want one naming DEXAFLOW_AUTH_CI_OIDC_ISSUERS", err)
	}
}

// TestValidateCIOIDC: a bad entry or lifetime fails boot naming its key.
func TestValidateCIOIDC(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*CIOIDCSection)
		want   string
	}{
		{"session too short", func(s *CIOIDCSection) { s.SessionTTLSeconds = 299 }, "auth.ci_oidc.session_ttl_seconds"},
		{"session too long", func(s *CIOIDCSection) { s.SessionTTLSeconds = 43201 }, "auth.ci_oidc.session_ttl_seconds"},
		{"no audience", func(s *CIOIDCSection) { s.Issuers[0].Audience = "" }, "auth.ci_oidc.issuers[0].audience"},
		{"admin rule", func(s *CIOIDCSection) { s.Issuers[0].Rules[0].Role = "admin" }, "auth.ci_oidc.issuers[0].rules[0].role"},
		{"lifetime over a day", func(s *CIOIDCSection) { s.Issuers[0].MaxLifetimeSeconds = 86401 }, "auth.ci_oidc.issuers[0].max_lifetime_seconds"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEXAFLOW_AUTH_CI_OIDC_ISSUERS", ciOIDCJSON)
			c := validServerConfig(t)
			tc.mutate(&c.Auth.CIOIDC)

			err := c.Validate()

			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want one naming %s", err, tc.want)
			}
		})
	}
}

// TestCIOIDCRefusesUnknownFields: a misspelled key fails boot instead of
// leaving the entry without it.
func TestCIOIDCRefusesUnknownFields(t *testing.T) {
	t.Setenv("DEXAFLOW_AUTH_JWT_SECRET", strings.Repeat("x", 40))
	if _, err := LoadServer(writeConfigFile(t, strings.Replace(ciOIDCYAML, "tenant: acme", "tenants: acme", 1)), nil); err == nil {
		t.Error("a file entry with tenants: loaded, want an error")
	}
	t.Setenv("DEXAFLOW_AUTH_CI_OIDC_ISSUERS", strings.Replace(ciOIDCJSON, `"tenant"`, `"tenants"`, 1))
	if _, err := LoadServer("", nil); err == nil {
		t.Error("a variable entry with tenants loaded, want an error")
	}
}
