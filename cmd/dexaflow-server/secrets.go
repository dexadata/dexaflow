package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// configureSecrets wires connection-secret encryption (the AES-256-GCM cipher)
// and the external-secrets D6 registration relaxation (ADR 0060) onto the repo.
//
// The boot sweep runs only when SecretKeyReencryptOnBoot is on (the default,
// Pro). `dexaflow lite` switches it off, because a Lite install moves keys only
// through `dexaflow lite migrate-key` (ADR 0065): a sweep at boot would migrate
// a config holding a predecessor implicitly, under no lock, with no
// verification before commit.
func configureSecrets(ctx context.Context, repo *storage.Repository, cfg *config.ServerConfig, logger *slog.Logger) error {
	if err := configureSecretCipher(repo, cfg.SecretKey, logger); err != nil {
		return err
	}
	if cfg.SecretKeyReencryptOnBoot {
		finishKeyRotation(ctx, repo, logger)
	}
	return configureSecretsCoverage(cfg.Secrets, repo)
}

// finishKeyRotation moves stored secrets onto the current key when
// LEOFLOW_SECRET_KEY carries a predecessor, so the rotation ends instead of
// living forever as a second key in the read set (#486).
//
// Never fatal. The rows are readable either way, since a predecessor that opens
// them is configured; failing to boot over a migration would take a working
// control plane down to finish a housekeeping task. A row no configured key can
// open is reported and left untouched, because its ciphertext is the only copy
// of that credential.
func finishKeyRotation(ctx context.Context, repo *storage.Repository, logger *slog.Logger) {
	res, err := repo.ReencryptSecrets(ctx)
	msg, level := rotationOutcome(res, err)
	if msg == "" {
		return
	}
	attrs := []any{"re_encrypted", res.Migrated, "skipped", res.Skipped, "unreadable", res.Unreadable}
	if err != nil {
		attrs = append(attrs, "error", err)
	}
	logger.Log(ctx, level, msg, attrs...)
}

// rotationOutcome is the one line the boot sweep logs about what it did, or ""
// when there was nothing to say. It calls the rotation complete only after a
// clean pass that moved something: a row skipped by the optimistic guard, or
// one no key opens, still needs the previous key, and a log that says
// otherwise is how an operator comes to delete it (ADR 0065, attempt 1).
func rotationOutcome(res storage.ReencryptResult, err error) (string, slog.Level) {
	switch {
	case err != nil || res.Unreadable > 0:
		return "could not finish the secret key rotation; the previous key is still required", slog.LevelError
	case res.Skipped > 0:
		return "the secret key rotation is not finished: some connections changed during the pass and were not moved; " +
			"the previous key is still required, and the next restart retries them", slog.LevelWarn
	case res.Migrated > 0:
		return "secret key rotation complete for the stored connections; " +
			"remove the previous key from DEXAFLOW_SECRET_KEY once no other replica needs it", slog.LevelInfo
	default:
		return "", slog.LevelInfo
	}
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
