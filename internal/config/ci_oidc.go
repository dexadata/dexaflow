package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/viper"
	"gopkg.in/yaml.v3"

	"github.com/dexadata/dexaflow/internal/cioidc"
)

// Bounds of auth.ci_oidc.session_ttl_seconds: a deploy that builds images
// needs minutes, and a session should not outlive a working day.
const (
	minCISessionTTLSeconds = 300
	maxCISessionTTLSeconds = 43200
)

// ciOIDCIssuersKey is the key, and with the DEXAFLOW_ or LEOFLOW_ prefix the
// variable, that lists the CI OIDC issuers.
const ciOIDCIssuersKey = "auth.ci_oidc.issuers"

// CIOIDCSection configures the CI OIDC issuers whose job tokens the control
// plane exchanges for a deploy session (ADR 0092, #1882). With no issuer the
// exchange route is not registered and nothing changes.
type CIOIDCSection struct {
	// SessionTTLSeconds is how long a session from the exchange lives, 300 to
	// 43200; 3600 by default. It is not renewed: a later job exchanges again.
	SessionTTLSeconds int `mapstructure:"session_ttl_seconds"`
	// Issuers are the trusted CI issuers, each with its audience, its one
	// tenant and its rules. Configure them in the server configuration file
	// or, as a JSON array of the same objects, in
	// DEXAFLOW_AUTH_CI_OIDC_ISSUERS. Decoded out of band (mapstructure:"-"),
	// because a claim name may contain dots that viper would split.
	Issuers []CIOIDCIssuerSection `mapstructure:"-" yaml:"issuers"`
}

// CIOIDCIssuerSection is one entry of auth.ci_oidc.issuers.
type CIOIDCIssuerSection struct {
	// Name identifies the entry in logs, metrics, sessions and `ci init`.
	Name string `yaml:"name" json:"name"`
	// Provider is github, gitlab, cloudbuild or other.
	Provider string `yaml:"provider" json:"provider"`
	// Issuer is the exact iss of the provider's tokens.
	Issuer string `yaml:"issuer" json:"issuer"`
	// JWKSURL is where the provider's keys are; empty discovers them.
	JWKSURL string `yaml:"jwks_url" json:"jwks_url"`
	// Audience is the aud the tokens must carry.
	Audience string `yaml:"audience" json:"audience"`
	// Tenant is the only tenant the entry's tokens act in.
	Tenant string `yaml:"tenant" json:"tenant"`
	// MaxLifetimeSeconds caps exp - iat; 0 means 3600, at most 86400.
	MaxLifetimeSeconds int `yaml:"max_lifetime_seconds" json:"max_lifetime_seconds"`
	// Rules map claims to a role; the first that matches decides.
	Rules []cioidc.Rule `yaml:"rules" json:"rules"`
}

// Enabled reports whether any CI OIDC issuer is configured.
func (s CIOIDCSection) Enabled() bool { return len(s.Issuers) > 0 }

// SessionTTL is SessionTTLSeconds as a duration; zero (a section built in
// code rather than loaded) is the default hour.
func (s CIOIDCSection) SessionTTL() time.Duration {
	if s.SessionTTLSeconds <= 0 {
		return time.Hour
	}
	return time.Duration(s.SessionTTLSeconds) * time.Second
}

// CIIssuers returns the entries as the verifier takes them.
func (s CIOIDCSection) CIIssuers() []cioidc.Issuer {
	out := make([]cioidc.Issuer, 0, len(s.Issuers))
	for _, is := range s.Issuers {
		out = append(out, cioidc.Issuer{
			Name: is.Name, Provider: is.Provider, Issuer: is.Issuer, JWKSURL: is.JWKSURL,
			Audience: is.Audience, Tenant: is.Tenant, Rules: is.Rules,
			MaxLifetime: time.Duration(is.MaxLifetimeSeconds) * time.Second,
		})
	}
	return out
}

// validateCIOIDC checks auth.ci_oidc when an issuer is configured (ADR 0092).
func (c *ServerConfig) validateCIOIDC() error {
	s := c.Auth.CIOIDC
	if !s.Enabled() {
		return nil
	}
	if s.SessionTTLSeconds < minCISessionTTLSeconds || s.SessionTTLSeconds > maxCISessionTTLSeconds {
		return fmt.Errorf("auth.ci_oidc.session_ttl_seconds is %d; want %d to %d", s.SessionTTLSeconds, minCISessionTTLSeconds, maxCISessionTTLSeconds)
	}
	for i, is := range s.Issuers {
		if is.MaxLifetimeSeconds < 0 || is.MaxLifetimeSeconds > int(cioidc.MaxLifetimeCeiling/time.Second) {
			return fmt.Errorf("auth.ci_oidc.issuers[%d].max_lifetime_seconds is %d; want 0 (one hour) to 86400", i, is.MaxLifetimeSeconds)
		}
	}
	if err := cioidc.Validate(s.CIIssuers()); err != nil {
		return fmt.Errorf("auth.ci_oidc.%w", err)
	}
	return nil
}

// decodeCIOIDCIssuers fills c.Auth.CIOIDC.Issuers from the variable (JSON),
// which wins, or else from the configuration file (YAML). Unknown fields
// are refused in both, so a misspelled key fails boot instead of being
// ignored.
func decodeCIOIDCIssuers(v *viper.Viper, configFile string, c *ServerConfig) error {
	if raw := strings.TrimSpace(v.GetString(ciOIDCIssuersKey)); raw != "" {
		dec := json.NewDecoder(strings.NewReader(raw))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&c.Auth.CIOIDC.Issuers); err != nil {
			return fmt.Errorf("DEXAFLOW_AUTH_CI_OIDC_ISSUERS (or LEOFLOW_AUTH_CI_OIDC_ISSUERS) is not a JSON array of auth.ci_oidc.issuers entries: %w", err)
		}
		return nil
	}
	if configFile == "" {
		return nil
	}
	switch strings.ToLower(filepath.Ext(configFile)) {
	case ".yaml", ".yml", ".json", "":
	default:
		return nil
	}
	data, err := os.ReadFile(configFile) //nolint:gosec // the operator's own configuration file
	if err != nil {
		return fmt.Errorf("reading config file %q for auth.ci_oidc.issuers: %w", configFile, err)
	}
	var raw struct {
		Auth struct {
			CIOIDC struct {
				Issuers yaml.Node `yaml:"issuers"`
			} `yaml:"ci_oidc"`
		} `yaml:"auth"`
	}
	if err = yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("decoding auth.ci_oidc.issuers from %q: %w", configFile, err)
	}
	if raw.Auth.CIOIDC.Issuers.IsZero() {
		return nil
	}
	// Re-encode the list alone, so a strict decoder can refuse unknown
	// fields without refusing the rest of the file.
	list, err := yaml.Marshal(&raw.Auth.CIOIDC.Issuers)
	if err != nil {
		return fmt.Errorf("re-encoding auth.ci_oidc.issuers: %w", err)
	}
	dec := yaml.NewDecoder(bytes.NewReader(list))
	dec.KnownFields(true)
	if err := dec.Decode(&c.Auth.CIOIDC.Issuers); err != nil {
		return fmt.Errorf("decoding auth.ci_oidc.issuers from %q: %w", configFile, err)
	}
	return nil
}
