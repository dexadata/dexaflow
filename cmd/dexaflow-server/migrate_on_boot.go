package main

import (
	"context"
	"log/slog"
	"strings"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/migrator"
	"github.com/dexadata/dexaflow/internal/storage"
)

// Self-migration on boot is opt-in (ADR 0090 decision 4, migrations.onBoot).
// The hook Job stays the default path and keeps the ordering an upgrade
// needs; onBoot covers what the hook cannot see, such as an install with
// migrations.enabled false or a database.url repointed between upgrades.

// wantsMigrateOnBoot reports whether this process may migrate itself: the
// setting is on and the role is not api, which never migrates whatever its
// DSN can do (Validate refuses that combination too).
func wantsMigrateOnBoot(cfg *config.ServerConfig) bool {
	return cfg.Database.MigrateOnBoot && cfg.Server.EffectiveRole() != config.RoleAPI
}

// migrateOnBoot applies the embedded migrations once, before the wait, when
// the schema is absent or behind. Never when it is ahead (a rollback runs an
// older binary on purpose), dirty (a person must repair it), unreadable, or
// a replaced database (migrating it would bring back an empty, healthy
// control plane). A failure is logged and boot goes on to wait, so the hook
// Job or an operator can still bring the schema current.
func migrateOnBoot(ctx context.Context, probe schemaProber, migrate func(context.Context) error, logger *slog.Logger) {
	verdict, err := probe.SchemaWaitState(ctx)
	if err != nil || (verdict.Class != storage.SchemaReasonAbsent && verdict.Class != storage.SchemaReasonBehind) {
		return
	}
	logger.InfoContext(ctx, "applying the embedded migrations (migrations.onBoot)", "reason", verdict.Reason)
	if merr := migrate(ctx); merr != nil {
		logger.WarnContext(ctx, "applying the migrations on boot failed; waiting for the schema as without migrations.onBoot", "error", merr.Error())
		return
	}
	logger.InfoContext(ctx, "applied the embedded migrations (migrations.onBoot)")
}

// embeddedMigrate runs the migrate wrapper's `up` in-process on the server's
// own DSN, with golang-migrate's progress lines in the server's log.
func embeddedMigrate(dsn string, logger *slog.Logger) func(context.Context) error {
	return func(ctx context.Context) error {
		return migrator.Up(ctx, dsn, logLines{ctx: ctx, logger: logger})
	}
}

// logLines writes each line golang-migrate prints as one log record.
type logLines struct {
	ctx    context.Context //nolint:containedctx // a Writer has no context of its own; this one lives for one migration
	logger *slog.Logger
}

// Write logs p, one record per call (golang-migrate writes one line each).
func (l logLines) Write(p []byte) (int, error) {
	if line := strings.TrimSpace(string(p)); line != "" {
		l.logger.InfoContext(l.ctx, "migration", "line", line)
	}
	return len(p), nil
}
