package config

import (
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/spf13/viper"
	"golang.org/x/crypto/bcrypt"
)

// bcryptMaxPasswordBytes is the longest password bcrypt hashes. A longer one
// fails auth.HashPassword, so the bootstrap would fail at boot anyway.
const bcryptMaxPasswordBytes = 72

// BootstrapSection configures the admin user the server creates or reconciles
// at boot in tenant `default`. Nothing happens unless Password or PasswordHash
// is set.
type BootstrapSection struct {
	// Email (DEXAFLOW_BOOTSTRAP_EMAIL) is the address of the bootstrap admin.
	// Empty (the default) means admin@dexaflow.local, resolved to the legacy
	// admin@leoflow.local on an install whose admin already has that address
	// (auth.ResolveAdminEmail).
	Email string `mapstructure:"email"`
	// Password (DEXAFLOW_BOOTSTRAP_PASSWORD) is the plaintext password of the
	// bootstrap admin, used only when PasswordHash is empty. Never logged.
	Password string `mapstructure:"password"`
	// PasswordHash (DEXAFLOW_BOOTSTRAP_PASSWORD_HASH) is a bcrypt hash of the
	// bootstrap admin's password. It wins over Password. Never logged.
	PasswordHash string `mapstructure:"password_hash"`
}

// Enabled reports whether a bootstrap credential is set.
func (b BootstrapSection) Enabled() bool {
	return b.Password != "" || b.PasswordHash != ""
}

// String keeps the credentials out of anything that formats the section, the
// whole ServerConfig included.
func (b BootstrapSection) String() string {
	return fmt.Sprintf("{Email:%s Password:%s PasswordHash:%s}", b.Email, redacted(b.Password), redacted(b.PasswordHash))
}

// GoString is String for the %#v verb.
func (b BootstrapSection) GoString() string { return b.String() }

// LogValue keeps the credentials out of structured logs.
func (b BootstrapSection) LogValue() slog.Value {
	return slog.GroupValue(
		slog.String("email", b.Email),
		slog.String("password", redacted(b.Password)),
		slog.String("password_hash", redacted(b.PasswordHash)),
	)
}

// redacted says whether a secret is set without showing it.
func redacted(s string) string {
	if s == "" {
		return ""
	}
	return "[redacted]"
}

// AgentSection configures what the server allows on the agent gRPC channel.
type AgentSection struct {
	// AllowInsecureSecrets (DEXAFLOW_AGENT_ALLOW_INSECURE_SECRETS, default
	// false) lets the server deliver secrets to agents over a plaintext gRPC
	// channel. Dev only: `dexaflow lite` sets it for its loopback agents, and
	// the Pro edition refuses to boot with it.
	AllowInsecureSecrets bool `mapstructure:"allow_insecure_secrets"`
}

// allowInsecureSecretsKey is the key whose raw value readInsecureSecretsAsBefore
// reads.
const allowInsecureSecretsKey = "agent.allow_insecure_secrets"

// readInsecureSecretsAsBefore keeps the meaning agent.allow_insecure_secrets had
// before it was a config key: the server read the variable as `== "true"`, so
// only the exact string true turned the escape hatch on and "1", "TRUE" or
// "yes" left it off. Decoded as a bool, "1" and "TRUE" would turn it on, and
// "yes" would fail boot. A string value is therefore resolved here, before
// viper decodes it, and a value other than true or false is logged, as it
// still means off. A YAML bool from the config file is left as it is.
func readInsecureSecretsAsBefore(v *viper.Viper) {
	s, ok := v.Get(allowInsecureSecretsKey).(string)
	if !ok {
		return
	}
	if s != "" && s != "true" && s != "false" {
		slog.Warn("agent.allow_insecure_secrets is neither true nor false; reading it as false, as earlier releases did",
			"value", s)
	}
	v.Set(allowInsecureSecretsKey, s == "true")
}

// validateBootstrap refuses bootstrap credentials that could never log in. It
// checks nothing while no credential is set, since the server then reads none
// of the bootstrap settings.
func (c *ServerConfig) validateBootstrap() error {
	b := c.Bootstrap
	if !b.Enabled() {
		return nil
	}
	if strings.IndexFunc(b.Email, unicode.IsSpace) >= 0 || strings.IndexFunc(b.Email, unicode.IsControl) >= 0 {
		return fmt.Errorf("bootstrap.email (DEXAFLOW_BOOTSTRAP_EMAIL) must not contain spaces or control characters, got %q", b.Email)
	}
	if b.PasswordHash != "" {
		if _, err := bcrypt.Cost([]byte(b.PasswordHash)); err != nil {
			return fmt.Errorf("bootstrap.password_hash (DEXAFLOW_BOOTSTRAP_PASSWORD_HASH) is not a bcrypt hash: %w", err)
		}
		return nil
	}
	if len(b.Password) > bcryptMaxPasswordBytes {
		return errors.New("bootstrap.password (DEXAFLOW_BOOTSTRAP_PASSWORD) is longer than the 72 bytes bcrypt accepts; use a shorter password or set bootstrap.password_hash")
	}
	return nil
}
