package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/version"
)

// capabilityLiteKeyLock is advertised in the version output by a server that
// honors DEXAFLOW_SECRET_KEY_MIGRATION_LOCK. `dexaflow lite` refuses to run a
// server binary that does not advertise it (ADR 0065 section 3, "no lock, no
// boot"): an older binary would ignore the setting and run unprotected.
const capabilityLiteKeyLock = "lite-key-lock"

// serverCapabilities lists every capability the version output advertises.
var serverCapabilities = []string{capabilityLiteKeyLock}

// versionOutput is what `dexaflow-server version` prints: the version line every
// existing parser reads ("<name> <version> ..."), then the capabilities. It is
// answered before any config is loaded, so probing a binary never boots it.
func versionOutput() string {
	out := version.Get().String() + "\ncapabilities:"
	for _, c := range serverCapabilities {
		out += " " + c
	}
	return out + "\n"
}

// errKeyLockLost cancels the server when its key-migration lock session is gone.
var errKeyLockLost = errors.New("lost the key-migration lock session; a key migration may have run since, so this server stops rather than keep writing under keys it can no longer trust")

// errKeyMigrationInProgress refuses a boot while `dexaflow lite migrate-key`
// holds the lock.
var errKeyMigrationInProgress = errors.New("a key migration is in progress (`dexaflow lite migrate-key`); start Lite again once it has finished")

// keyLockCheckInterval is how often the server checks it still holds the lock.
const keyLockCheckInterval = 2 * time.Second

// acquireKeyLock takes the key-migration lock SHARED on a dedicated,
// never-recycled session and returns the pool holding it. It refuses when a
// migration holds the lock. Only a Lite server (SecretKeyMigrationLock) calls it.
func acquireKeyLock(ctx context.Context, db config.DatabaseSection) (*pgxpool.Pool, error) {
	pool, err := storage.NewLeaderPool(ctx, db)
	if err != nil {
		return nil, fmt.Errorf("opening the key-migration lock session: %w", err)
	}
	ok, err := storage.TryKeyMigrationLockShared(ctx, pool)
	if err != nil {
		pool.Close()
		return nil, err
	}
	if !ok {
		pool.Close()
		return nil, errKeyMigrationInProgress
	}
	return pool, nil
}

// watchKeyLock checks every interval that the lock is still held, and cancels
// the server with errKeyLockLost the first time it is not, or the check fails.
//
// Losing the session is fatal, not something to reacquire: a session lock dies
// with its connection (a Docker Postgres restart, ADR 0009), and a migration may
// have run and dropped the key this server reads with in between.
func watchKeyLock(ctx context.Context, cancel context.CancelCauseFunc, holds func(context.Context) (bool, error), interval time.Duration) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			held, err := holds(ctx)
			if ctx.Err() != nil {
				return
			}
			if err != nil || !held {
				cancel(errKeyLockLost)
				return
			}
		}
	}
}

// holdKeyLock takes the lock when the config asks for it and starts watching
// it. It returns the context the server should run under (canceled with
// errKeyLockLost on loss) and a release func.
func holdKeyLock(ctx context.Context, cfg *config.ServerConfig, logger *slog.Logger) (context.Context, func(), error) {
	if !cfg.SecretKeyMigrationLock {
		return ctx, func() {}, nil
	}
	pool, err := acquireKeyLock(ctx, cfg.Database)
	if err != nil {
		return ctx, func() {}, err
	}
	logger.Info("holding the Lite key-migration lock for this server's lifetime")
	wctx, cancel := context.WithCancelCause(ctx)
	go watchKeyLock(wctx, cancel, func(c context.Context) (bool, error) {
		return storage.HoldsKeyMigrationLock(c, pool)
	}, keyLockCheckInterval)
	return wctx, func() { cancel(nil); pool.Close() }, nil
}
