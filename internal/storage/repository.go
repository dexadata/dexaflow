package storage

import (
	"context"
	"errors"
	"math"
	"strings"
	"sync"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/secrets"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

const defaultMaxActiveRuns = 16

// txBeginner opens a transaction. *pgxpool.Pool satisfies it; tests inject a
// fake so the transactional paths can be exercised without a database.
type txBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// Repository implements the API resource and auth user-store interfaces over
// Postgres using the sqlc-generated query set.
type Repository struct {
	q      *queries.Queries
	pool   txBeginner
	cipher secrets.Cipher
	// varEncryption is how Variable values are stored (#507); the zero value
	// is off, the plaintext form every earlier release wrote.
	varEncryption VariableEncryption
	extCoverage   externalSecretCoverage
	// pausedAtCreation is the server's dags_are_paused_at_creation (#1705):
	// whether a DAG registered for the first time starts paused when its spec
	// does not say. The zero value is false (new DAGs registered active), which
	// is what tools and tests that build a Repository directly get; the server
	// sets it from its configuration (default true) through
	// WithDagsArePausedAtCreation.
	pausedAtCreation bool
	xcomValues       XComValueDeleter
	specs            *specCache
	// tenants caches tenant name -> id (see tenantID). Bounded by the number
	// of tenants that exist, since only successful lookups are stored.
	tenants *sync.Map
}

// XComValueDeleter deletes a stored XCom value by its backend key. xcom.Backend
// satisfies it (Redis in production, xcom_store under Lite).
type XComValueDeleter interface {
	Delete(ctx context.Context, key string) error
}

// externalSecretCoverage reports whether a declared name is served by a
// configured external secret backend (operator config, ADR 0060). The D6
// registration check consults it to accept an externally-backed declared name
// without a provider call. It must be operator-sourced: if a DAG could supply
// its own coverage, it could mark any name covered and defeat D6 (ADR 0060 B1).
type externalSecretCoverage interface {
	CoversVariable(name string) bool
	CoversConnection(name string) bool
}

// NewRepository builds a Repository backed by the given Postgres connection,
// configured by opts. Every option is optional here: tools and tests that need
// none of them get the zero behavior each option documents. The server builds
// its repository through NewServerRepository, which also checks that the
// dependencies it must decide were given.
func NewRepository(pg *Postgres, opts ...Option) *Repository {
	var c repoConfig
	for _, o := range opts {
		o(&c)
	}
	r := &Repository{q: pg.Queries, pool: pg.Pool, specs: sharedSpecCache(pg), tenants: &sync.Map{}}
	r.apply(c)
	return r
}

// DagsArePausedAtCreation reports the setting WithDagsArePausedAtCreation gave.
func (r *Repository) DagsArePausedAtCreation() bool { return r.pausedAtCreation }

func toInt32(n int) int32 {
	switch {
	case n < 0:
		return 0
	case n > math.MaxInt32:
		return math.MaxInt32
	default:
		return int32(n)
	}
}

func mapNotFound(err error) error {
	if errors.Is(err, pgx.ErrNoRows) {
		return domain.ErrNotFound
	}
	return err
}

// pgUniqueViolation is the Postgres SQLSTATE for a unique-constraint violation.
const pgUniqueViolation = "23505"

// pgForeignKeyViolation is the Postgres SQLSTATE for a foreign-key constraint violation.
const pgForeignKeyViolation = "23503"

// isTenantFKViolation reports whether err is a Postgres foreign-key violation
// (SQLSTATE 23503) on a tenant_id column, which is how a write with a stale
// cached tenant id fails: Postgres names the referenced table in the detail
// ("is not present in table "tenants"") and every such constraint is named
// <table>_tenant_id_fkey.
func isTenantFKViolation(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) || pgErr.Code != pgForeignKeyViolation {
		return false
	}
	return strings.Contains(pgErr.Detail, `table "tenants"`) ||
		strings.HasSuffix(pgErr.ConstraintName, "tenant_id_fkey")
}

// withTenantWrite runs op with tenant's resolved UUID. If op fails with a
// Postgres foreign key violation against tenants (SQLSTATE 23503), the cached
// tenant mapping is dropped and op is retried once with the re-resolved UUID.
// This handles tenants that were recreated or restored behind a running process.
func (r *Repository) withTenantWrite(ctx context.Context, tenant string, op func(tid pgtype.UUID) error) error {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return err
	}
	err = op(tid)
	if err != nil && isTenantFKViolation(err) {
		r.InvalidateTenant(tenant)
		freshTID, ferr := r.tenantID(ctx, tenant)
		if ferr == nil && freshTID != tid {
			return op(freshTID)
		}
	}
	return err
}

// mapConflict translates a Postgres unique-constraint violation into
// domain.ErrConflict (which the API maps to 409), leaving other errors as is, so
// a duplicate write (e.g. a second dag run for the same logical date) surfaces as
// a clean conflict rather than a 500. What is at stake is the STATUS, not the
// body: since #961 an untranslated error no longer discloses its SQLSTATE to
// the caller either way, but a 500 for an ordinary duplicate would still send
// the caller (and the on-call) hunting a server fault that never happened.
func mapConflict(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation {
		return domain.ErrConflict
	}
	return err
}

// tenantID resolves a tenant name to its id. Nearly every repository method
// starts with it, so a resolved id is cached for the life of the process; a
// miss is not cached, so a tenant created later is found. The mapping changes
// under a running process only from outside it: a tenant recreated behind it,
// or a restored backup whose tenants carry other ids. The writes an operator
// makes right after such a restore (variables, connections, pools) go through
// withTenantWrite, which drops the stale entry on a tenant foreign-key
// violation and retries once (#1614). Other queries keep the old id until a
// restart, which is why the restore guide still says to restart.
func (r *Repository) tenantID(ctx context.Context, name string) (pgtype.UUID, error) {
	if r.tenants != nil {
		if id, ok := r.tenants.Load(name); ok {
			if uid, ok := id.(pgtype.UUID); ok {
				return uid, nil
			}
		}
	}
	t, err := r.q.GetTenantByName(ctx, name)
	if err != nil {
		return pgtype.UUID{}, mapNotFound(err)
	}
	if r.tenants != nil {
		r.tenants.Store(name, t.ID)
	}
	return t.ID, nil
}

// isUniqueViolation reports whether err is a Postgres unique-constraint
// violation (SQLSTATE 23505).
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgUniqueViolation
}
