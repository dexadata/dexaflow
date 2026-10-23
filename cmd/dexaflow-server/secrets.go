package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/agent/secretsource"
	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// secretOptions builds the repository options for connection-secret
// encryption (the AES-256-GCM cipher), Variable encryption (#507) and the
// external-secrets D6 registration relaxation (ADR 0060), and returns the
// Variable encryption mode for convergeSecrets.
//
// "on" and "decrypt" both need the key, and asking for either without one fails
// the boot: encrypting nothing, or leaving sealed rows sealed, while the
// operator believes otherwise is the silent downgrade ADR 0019 rules out.
// "off", the default, needs no key and changes nothing, as before.
func secretOptions(cfg *config.ServerConfig, logger *slog.Logger) ([]storage.Option, storage.VariableEncryption, error) {
	cipher, err := secretCipher(cfg.SecretKey, logger)
	if err != nil {
		return nil, "", err
	}
	mode, err := storage.ParseVariableEncryption(cfg.VariableEncryption)
	if err != nil {
		return nil, "", err
	}
	if mode != storage.VariableEncryptionOff && cipher == nil {
		return nil, "", fmt.Errorf("variable_encryption=%s requires a usable DEXAFLOW_SECRET_KEY (or LEOFLOW_SECRET_KEY); "+
			"set the key, or set variable_encryption=off", mode)
	}
	coverage, err := secretsCoverage(cfg.Secrets)
	if err != nil {
		return nil, "", err
	}
	return append(coverage, storage.WithCipher(cipher), storage.WithVariableEncryption(mode)), mode, nil
}

// convergeSecrets runs the boot sweeps over the stored secrets: the key
// rotation, and the conversion of stored Variables to the configured mode.
//
// The key rotation runs only when SecretKeyReencryptOnBoot is on (the default,
// Pro). `dexaflow lite` switches it off, because a Lite install moves keys only
// through `dexaflow lite migrate-key` (ADR 0065): a sweep at boot would migrate
// a config holding a predecessor implicitly, under no lock, with no
// verification before commit.
//
// The Variable sweep is never fatal, like the key rotation: every row stays
// readable either way, and a write after boot already takes the configured
// form. It runs in Lite too: it only seals plaintext under the current key, or
// opens sealed values, and never moves a value between keys.
func convergeSecrets(ctx context.Context, repo *storage.Repository, cfg *config.ServerConfig, mode storage.VariableEncryption, logger *slog.Logger) {
	if cfg.SecretKeyReencryptOnBoot {
		finishKeyRotation(ctx, repo, logger)
	}
	if mode == storage.VariableEncryptionOff {
		return
	}
	res, serr := repo.ConvergeVariableEncryption(ctx)
	msg, level := variableSweepOutcome(mode, res, serr)
	if msg == "" {
		return
	}
	attrs := []any{"variable_encryption", string(mode), "rewritten", res.Migrated, "skipped", res.Skipped, "unreadable", res.Unreadable}
	if serr != nil {
		attrs = append(attrs, "error", serr)
	}
	logger.Log(ctx, level, msg, attrs...)
}

// variableSweepOutcome is the one line the variable_encryption boot sweep logs,
// or "" when it found nothing to rewrite.
func variableSweepOutcome(mode storage.VariableEncryption, res storage.VariableSweepResult, err error) (string, slog.Level) {
	switch {
	case err != nil || res.Unreadable > 0:
		return "could not convert every stored variable; the ones no configured key opens were left as they are", slog.LevelError
	case res.Skipped > 0:
		return "some variables changed during the variable_encryption sweep and were left to that write; the next restart looks at them again", slog.LevelWarn
	case res.Migrated > 0 && mode == storage.VariableEncryptionDecrypt:
		return "stored variables restored to plaintext (variable_encryption=decrypt)", slog.LevelInfo
	case res.Migrated > 0:
		return "stored variables encrypted with the secret key (variable_encryption=on)", slog.LevelInfo
	default:
		return "", slog.LevelInfo
	}
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

// secretsCoverage relaxes the D6 registration check so a declared name covered
// by the configured external backend registers without a provider call (ADR
// 0060 B1). No option when no backend is configured; fails closed on a
// malformed config rather than silently disabling it.
func secretsCoverage(sec config.SecretsSection) ([]storage.Option, error) {
	cfg, enabled, err := secretsource.ParseBackendConfig(sec.Backend, secretsKwargsJSON(sec))
	if err != nil {
		return nil, fmt.Errorf("secrets backend config: %w", err)
	}
	if !enabled {
		return nil, nil
	}
	return []storage.Option{storage.WithExternalSecretCoverage(backendCoverage{b: cfg.Routing})}, nil
}
