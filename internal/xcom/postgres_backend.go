package xcom

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// pgxQuerier is the subset of *pgxpool.Pool the Postgres XCom backend needs,
// declared here (near the consumer) so the backend is decoupled from storage
// and trivially faked in tests.
type pgxQuerier interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// PostgresBackend is the XCom Backend used by Dexaflow Lite: it stores entries in
// the xcom_store table with an expires_at TTL, so Lite needs no Redis at all
// (scheduler locks already use Postgres advisory locks). Production keeps the
// RedisBackend per ADR 0006.
type PostgresBackend struct {
	db  pgxQuerier
	now func() time.Time
}

// NewPostgresBackend builds a PostgresBackend over the given pgx pool.
func NewPostgresBackend(db pgxQuerier) *PostgresBackend {
	return &PostgresBackend{db: db, now: time.Now}
}

// Push upserts the entry under key, expiring it ttl from now.
func (b *PostgresBackend) Push(ctx context.Context, key string, entry Entry, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	_, err := b.db.Exec(ctx,
		`INSERT INTO xcom_store (xcom_key, value, content_type, size_bytes, created_at, expires_at)
		 VALUES ($1, $2, $3, $4, $5, $6)
		 ON CONFLICT (xcom_key) DO UPDATE SET
		   value = EXCLUDED.value, content_type = EXCLUDED.content_type,
		   size_bytes = EXCLUDED.size_bytes, created_at = EXCLUDED.created_at,
		   expires_at = EXCLUDED.expires_at`,
		key, entry.Value, entry.ContentType, entry.SizeBytes, entry.CreatedAt, b.now().Add(ttl))
	if err != nil {
		return fmt.Errorf("writing xcom to postgres: %w", err)
	}
	return nil
}

// Fetch returns the entry at key, or ErrNotFound when it is absent or expired.
func (b *PostgresBackend) Fetch(ctx context.Context, key string) (Entry, error) {
	var e Entry
	err := b.db.QueryRow(ctx,
		`SELECT value, content_type, size_bytes, created_at
		   FROM xcom_store WHERE xcom_key = $1 AND expires_at > now()`, key).
		Scan(&e.Value, &e.ContentType, &e.SizeBytes, &e.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Entry{}, ErrNotFound
	}
	if err != nil {
		return Entry{}, fmt.Errorf("reading xcom from postgres: %w", err)
	}
	return e, nil
}

// Delete removes the entry at key.
func (b *PostgresBackend) Delete(ctx context.Context, key string) error {
	if _, err := b.db.Exec(ctx, `DELETE FROM xcom_store WHERE xcom_key = $1`, key); err != nil {
		return fmt.Errorf("deleting xcom from postgres: %w", err)
	}
	return nil
}

// List returns the unexpired keys matching the Redis-glob pattern.
func (b *PostgresBackend) List(ctx context.Context, pattern string) ([]string, error) {
	rows, err := b.db.Query(ctx,
		`SELECT xcom_key FROM xcom_store WHERE xcom_key LIKE $1 AND expires_at > now()`, globToLike(pattern))
	if err != nil {
		return nil, fmt.Errorf("listing xcom keys: %w", err)
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if serr := rows.Scan(&k); serr != nil {
			return nil, fmt.Errorf("scanning xcom key: %w", serr)
		}
		keys = append(keys, k)
	}
	if rerr := rows.Err(); rerr != nil {
		return nil, fmt.Errorf("iterating xcom keys: %w", rerr)
	}
	return keys, nil
}

// DeleteExpired removes rows past their TTL, returning how many were deleted. It
// is the periodic sweep that replaces Redis's native key expiry.
func (b *PostgresBackend) DeleteExpired(ctx context.Context) (int64, error) {
	tag, err := b.db.Exec(ctx, `DELETE FROM xcom_store WHERE expires_at <= now()`)
	if err != nil {
		return 0, fmt.Errorf("sweeping expired xcom: %w", err)
	}
	return tag.RowsAffected(), nil
}

// globToLike converts a Redis glob ("*", "?") to a SQL LIKE pattern, escaping
// the LIKE metacharacters ("%", "_", "\\") that are literal in a glob so they
// match literally.
func globToLike(pattern string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(pattern)
	return strings.NewReplacer(`*`, `%`, `?`, `_`).Replace(escaped)
}
