package main

import (
	"fmt"
	"log/slog"

	"github.com/neochaotic/leoflow/internal/agent/secretsource"
	"github.com/neochaotic/leoflow/internal/config"
	"github.com/neochaotic/leoflow/internal/storage"
)

// configureSecrets wires connection-secret encryption (the AES-256-GCM cipher)
// and the external-secrets D6 registration relaxation (ADR 0060) onto the repo.
func configureSecrets(repo *storage.Repository, cfg *config.ServerConfig, logger *slog.Logger) error {
	if err := configureSecretCipher(repo, cfg.SecretKey, cfg.SecretKeyFallback, logger); err != nil {
		return err
	}
	return configureSecretsCoverage(cfg.Secrets, repo)
}

// secretsKwargsJSON is the operator's backend kwargs as a JSON string, delivered
// to the pod and parsed for routing. It is already a JSON string in config; empty
// becomes an empty object. A malformed value is caught (fail-closed) by
// ParseBackendConfig, not silently swallowed here.
func secretsKwargsJSON(sec config.SecretsSection) string {
	if sec.BackendKwargs == "" {
		return "{}"
	}
	return sec.BackendKwargs
}

// backendCoverage adapts an operator secretsource.Backend to the storage layer's
// external-secret coverage predicate (ADR 0060 B1/D6). Coverage is kind-level and
// operator-derived; a DAG can never influence it.
type backendCoverage struct{ b secretsource.Backend }

func (c backendCoverage) CoversVariable(string) bool { return c.b.Covers(secretsource.KindVariable) }
func (c backendCoverage) CoversConnection(string) bool {
	return c.b.Covers(secretsource.KindConnection)
}

// configureSecretsCoverage relaxes the D6 registration check so a declared name
// covered by the configured external backend registers without a provider call
// (ADR 0060 B1). No-op when no backend is configured; fails closed on a malformed
// config rather than silently disabling it.
func configureSecretsCoverage(sec config.SecretsSection, repo *storage.Repository) error {
	cfg, enabled, err := secretsource.ParseBackendConfig(sec.Backend, secretsKwargsJSON(sec))
	if err != nil {
		return fmt.Errorf("secrets backend config: %w", err)
	}
	if !enabled {
		return nil
	}
	repo.SetExternalSecretCoverage(backendCoverage{b: cfg.Routing})
	return nil
}
