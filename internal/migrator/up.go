package migrator

import (
	"context"
	"io"
	"time"
)

// Up applies the migrations embedded in this binary to the database at dsn,
// as the image's `up` does, and returns nil or a *Failure. It is what the
// server runs in-process for migrations.onBoot (ADR 0090 decision 4), so
// self-migration and the hook Job share one code path, one set of failure
// messages and golang-migrate's advisory lock, which serializes them. It
// neither reads nor writes the installation id: only the Job, which owns the
// ConfigMap, records it.
func Up(ctx context.Context, dsn string, log io.Writer) error {
	op, err := open(ctx, options{databaseURL: dsn, connectTimeout: 30 * time.Second, lockTimeout: 15 * time.Second, prefetch: 10}, log)
	if err != nil {
		return err
	}
	defer func() { _, _ = op.m.Close() }() //nolint:errcheck // best-effort close of source and database handles
	return execute(op, command{name: "up"}, log)
}
