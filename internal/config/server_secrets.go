package config

import (
	"fmt"
)

// Variable encryption modes (variable_encryption, #507).
const (
	// VariableEncryptionOff stores Variable values in plaintext (the default).
	VariableEncryptionOff = "off"
	// VariableEncryptionOn seals Variable values with the secret key.
	VariableEncryptionOn = "on"
	// VariableEncryptionDecrypt restores sealed values to plaintext at boot.
	VariableEncryptionDecrypt = "decrypt"
)

// SecretsSection configures the external secrets backend (ADR 0060). When Backend
// is set, a Connection/Variable a DAG declares can be resolved pod-side from the
// provider store under the pod's keyless identity instead of the leoflow vault.
// Empty (the default) keeps the vault as the only source, byte-identical to
// pre-0060. This is operator-only config: it is delivered to the pod as
// LEOFLOW_SECRETS_* env, which an author's task env can never set (#828).
type SecretsSection struct {
	// Backend is the provider secrets-backend class the in-pod resolver drives
	// (e.g. the Airflow AWS SecretsManagerBackend). Empty disables external secrets.
	Backend string `mapstructure:"backend"`
	// BackendKwargs is the provider kwargs as a JSON object string (connections_prefix,
	// variables_prefix, region_name, …); a kind is served iff its `*_prefix` kwarg is
	// present. A JSON string (not a map) so it is settable via a single
	// LEOFLOW_SECRETS_BACKEND_KWARGS env var, matching the env-only control-plane
	// chart. Empty is treated as `{}`. Delivered verbatim to the pod.
	BackendKwargs string `mapstructure:"backend_kwargs"`
}

// Secret policy allowlists (ADR 0055). auth.secret_scoping and
// auth.secret_liveness_mode are validated against these; an unknown value fails
// boot closed. Empty is valid, serverDefaults sets the safe default for each.
const (
	// SecretScopingPermissive delivers the whole tenant vault (today's behavior),
	// scoping only where a DAG declared; the default.
	SecretScopingPermissive = "permissive"
	// SecretScopingEnforce delivers only the declared subset.
	SecretScopingEnforce = "enforce"
	// SecretScopingOff disables scope-by-declaration entirely.
	SecretScopingOff = "off"
	// SecretLivenessObserve logs a would-have-denied without denying; the default.
	SecretLivenessObserve = "observe"
	// SecretLivenessEnforce denies secret delivery when the caller's TI is not live.
	SecretLivenessEnforce = "enforce"
	// AgentTokenTransportEnvVar sets the agent token as a plaintext env var on the
	// pod spec (today's behavior); the default.
	AgentTokenTransportEnvVar = "envvar"
	// AgentTokenTransportExchange mounts a projected ServiceAccount token the agent
	// exchanges (via TokenReview) for a task-scoped JWT, so no bearer sits in
	// plaintext on the Pod object (ADR 0055 Fix #3). Pro/Kubernetes-executor-only.
	AgentTokenTransportExchange = "exchange"
)

// validateSecretPolicies rejects an unknown auth.secret_scoping or
// auth.secret_liveness_mode, failing closed at boot rather than letting main.go
// wire an unrecognized policy (ADR 0055). Empty is valid: serverDefaults sets
// the safe default (permissive / observe) for each.
func (c *ServerConfig) validateSecretPolicies() error {
	switch c.Auth.SecretScoping {
	case "", SecretScopingPermissive, SecretScopingEnforce, SecretScopingOff:
	default:
		return fmt.Errorf("invalid auth.secret_scoping %q: must be %q, %q or %q",
			c.Auth.SecretScoping, SecretScopingPermissive, SecretScopingEnforce, SecretScopingOff)
	}
	switch c.Auth.SecretLivenessMode {
	case "", SecretLivenessObserve, SecretLivenessEnforce:
	default:
		return fmt.Errorf("invalid auth.secret_liveness_mode %q: must be %q or %q",
			c.Auth.SecretLivenessMode, SecretLivenessObserve, SecretLivenessEnforce)
	}
	switch c.Auth.AgentTokenTransport {
	case "", AgentTokenTransportEnvVar, AgentTokenTransportExchange:
	default:
		return fmt.Errorf("invalid auth.agent_token_transport %q: must be %q or %q",
			c.Auth.AgentTokenTransport, AgentTokenTransportEnvVar, AgentTokenTransportExchange)
	}
	switch c.VariableEncryption {
	case "", VariableEncryptionOff, VariableEncryptionOn, VariableEncryptionDecrypt:
	default:
		return fmt.Errorf("invalid variable_encryption %q: must be %q, %q or %q",
			c.VariableEncryption, VariableEncryptionOff, VariableEncryptionOn, VariableEncryptionDecrypt)
	}
	return nil
}
