package main

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/config"
)

// adminBootstrapper is what bootstrapAdmin needs from the user store.
type adminBootstrapper interface {
	AdminUserState(ctx context.Context, tenant, email string) (auth.AdminState, error)
	BootstrapAdminHash(ctx context.Context, tenant, email, hash string) (bool, error)
	BootstrapAdmin(ctx context.Context, tenant, email, password string) (bool, error)
}

// bootstrapAdmin creates or reconciles the bootstrap admin from
// bootstrap.password_hash or bootstrap.password, under bootstrap.email (default
// auth.DefaultAdminEmail). The default address
// resolves to the legacy admin@leoflow.local when that is the install's admin
// (auth.ResolveAdminEmail), so an install created before the rename keeps one
// admin.
func bootstrapAdmin(ctx context.Context, repo adminBootstrapper, cfg config.BootstrapSection, logger *slog.Logger) error {
	if !cfg.Enabled() {
		return nil
	}
	hash, pw := cfg.PasswordHash, cfg.Password
	email, err := resolveBootstrapEmail(ctx, repo, cfg.Email, logger)
	if err != nil {
		return err
	}
	// Prefer a precomputed bcrypt hash (Dexaflow Lite never sends the plaintext to
	// the control plane); fall back to a plaintext bootstrap password.
	var created bool
	if hash != "" {
		created, err = repo.BootstrapAdminHash(ctx, "default", email, hash)
		if err != nil {
			return fmt.Errorf("bootstrap admin (hash): %w", err)
		}
	} else if created, err = repo.BootstrapAdmin(ctx, "default", email, pw); err != nil {
		return fmt.Errorf("bootstrap admin: %w", err)
	}
	if created {
		logger.Info("bootstrapped admin user", "email", email)
	}
	return nil
}

// resolveBootstrapEmail resolves bootstrap.email to the address the bootstrap
// admin lives under, and says so when it is not the address asked for.
func resolveBootstrapEmail(ctx context.Context, repo adminBootstrapper, configured string, logger *slog.Logger) (string, error) {
	res, err := auth.ResolveAdminEmail(ctx, configured,
		func(ctx context.Context, email string) (auth.AdminState, error) {
			return repo.AdminUserState(ctx, "default", email)
		})
	if err != nil {
		return "", fmt.Errorf("bootstrap admin: %w", err)
	}
	if res.KeptLegacy {
		logger.Info("bootstrap admin kept its pre-rename address", "email", res.Email)
	}
	if res.BothExist {
		logger.Warn("users exist under both bootstrap admin addresses; only one is bootstrapped",
			"email", res.Email, "new_email", auth.DefaultAdminEmail, "legacy_email", auth.LegacyAdminEmail)
	}
	return res.Email, nil
}
