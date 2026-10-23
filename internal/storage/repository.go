package storage

import (
	"context"
	"errors"
	"math"
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
	// SetDagsArePausedAtCreation.
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

// SetXComBackend attaches the XCom value store, so a clear deletes the values of
// the attempts it clears and not only their index rows (#1131). Without it a
// clear removes only the index rows.
func (r *Repository) SetXComBackend(b XComValueDeleter) { r.xcomValues = b }

// externalSecretCoverage reports whether a declared name is served by a
// configured external secret backend (operator config, ADR 0060). The D6
// registration check consults it to accept an externally-backed declared name
// without a provider call. It must be operator-sourced: if a DAG could supply
// its own coverage, it could mark any name covered and defeat D6 (ADR 0060 B1).
type externalSecretCoverage interface {
	CoversVariable(name string) bool
	CoversConnection(name string) bool
}

// NewRepository builds a Repository backed by the given Postgres connection.
func NewRepository(pg *Postgres) *Repository {
	return &Repository{q: pg.Queries, pool: pg.Pool, specs: sharedSpecCache(pg), tenants: &sync.Map{}}
}

// SetDagsArePausedAtCreation sets whether a DAG registered for the first time
// starts paused when its spec has no is_paused_upon_creation (the server's
// dags_are_paused_at_creation, #1705). It never affects a DAG that exists.
func (r *Repository) SetDagsArePausedAtCreation(paused bool) { r.pausedAtCreation = paused }

// DagsArePausedAtCreation reports the setting SetDagsArePausedAtCreation set.
func (r *Repository) DagsArePausedAtCreation() bool { return r.pausedAtCreation }

// SetCipher attaches the encryption cipher used for connection secrets (ADR
// 0019). Without it, connection writes fail rather than storing plaintext.
func (r *Repository) SetCipher(c secrets.Cipher) { r.cipher = c }

// SetExternalSecretCoverage attaches the operator-configured external-backend
// coverage used by the D6 registration check (ADR 0060 B1). Without it, the check
// is the pre-0060 vault-existence check (a declared name must exist in the
// vault). It must be wired from operator/Helm config, never from a DAG.
func (r *Repository) SetExternalSecretCoverage(c externalSecretCoverage) { r.extCoverage = c }

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
// starts with it, so a resolved id is cached for the life of the process: a
// tenant is never renamed or deleted (no query does either), so name -> id
// cannot change. A miss is not cached, so a tenant created later is found.
// The one way the mapping changes under a running process is outside it:
// restoring a backup whose tenants carry different ids. Restart the control
// plane after such a restore, or every query keeps using the old ids.
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
