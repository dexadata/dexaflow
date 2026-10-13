package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// MaxLiveAPITokensPerUser is how many unrevoked, unexpired API tokens one
// user may hold (ADR 0074). It bounds the table and what a leaked session can
// mint.
const MaxLiveAPITokensPerUser = 50

// CreateAPIToken stores a new API token for userID, a user of tenant, under
// name, keeping only hash (ADR 0074). It runs in one transaction that locks
// the user row, so the MaxLiveAPITokensPerUser cap holds under concurrent
// creates, and that first revokes the user's expired tokens of the same name,
// so an expired token does not hold its name. A user outside the tenant, or
// one at the cap, is domain.ErrValidation; a live token of the same owner and
// name is domain.ErrConflict.
func (r *Repository) CreateAPIToken(ctx context.Context, tenant, userID, name, hash string, scopes []string, expiresAt time.Time) (auth.APIToken, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return auth.APIToken{}, err
	}
	uid, err := parseUUID(userID)
	if err != nil {
		return auth.APIToken{}, domain.Safef(domain.ErrValidation, "this account cannot own an API token")
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.APIToken{}, fmt.Errorf("beginning api token tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	q := r.q.WithTx(tx)
	if cerr := checkAPITokenOwner(ctx, q, tid, uid, name); cerr != nil {
		return auth.APIToken{}, cerr
	}
	row, err := q.InsertAPIToken(ctx, queries.InsertAPITokenParams{
		TenantID: tid, UserID: uid, Name: name, TokenHash: hash, Scopes: scopes,
		ExpiresAt: pgtype.Timestamptz{Time: expiresAt, Valid: true},
	})
	if err != nil {
		return auth.APIToken{}, fmt.Errorf("inserting api token: %w", mapConflict(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.APIToken{}, fmt.Errorf("committing api token: %w", err)
	}
	return auth.APIToken{
		ID: uuidToString(row.ID), TenantID: tenant, UserID: userID, Name: name,
		Scopes: scopes, CreatedAt: row.CreatedAt.Time, ExpiresAt: expiresAt,
	}, nil
}

// checkAPITokenOwner runs inside the create transaction: it locks the owner's
// row (refusing a user outside the tenant), revokes the owner's expired
// tokens named name, and refuses an owner at the live-token cap.
func checkAPITokenOwner(ctx context.Context, q *queries.Queries, tid, uid pgtype.UUID, name string) error {
	if _, err := q.LockAPITokenOwner(ctx, queries.LockAPITokenOwnerParams{ID: uid, TenantID: tid}); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return domain.Safef(domain.ErrValidation, "this account cannot own an API token")
		}
		return fmt.Errorf("locking api token owner: %w", err)
	}
	if err := q.RetireExpiredAPITokenName(ctx, queries.RetireExpiredAPITokenNameParams{UserID: uid, Name: name}); err != nil {
		return fmt.Errorf("retiring expired api token name: %w", err)
	}
	n, err := q.CountLiveAPITokens(ctx, queries.CountLiveAPITokensParams{UserID: uid, TenantID: tid})
	if err != nil {
		return fmt.Errorf("counting live api tokens: %w", err)
	}
	if n >= MaxLiveAPITokensPerUser {
		return domain.Safef(domain.ErrValidation, "an account holds at most %d live API tokens; revoke one first", MaxLiveAPITokensPerUser)
	}
	return nil
}

// FindAPITokenByHash returns the token whose secret hashes to hash, revoked
// or expired included, or auth.ErrAPITokenNotFound. It implements
// auth.APITokenStore.
func (r *Repository) FindAPITokenByHash(ctx context.Context, hash string) (*auth.APIToken, error) {
	row, err := r.q.GetAPITokenByHash(ctx, hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, auth.ErrAPITokenNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("loading api token: %w", err)
	}
	return &auth.APIToken{
		ID: uuidToString(row.ID), TenantID: row.Tenant, UserID: uuidToString(row.UserID),
		Name: row.Name, Scopes: row.Scopes, CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time,
		LastUsedAt: timePtr(row.LastUsedAt), RevokedAt: timePtr(row.RevokedAt),
	}, nil
}

// TouchAPIToken records that the token was used at at, unless its last use
// is less than five minutes before at, and reports whether it wrote.
// The throttle is the UPDATE's own predicate, so concurrent requests write
// once, and an older time never moves the last use back. It implements
// auth.APITokenStore.
func (r *Repository) TouchAPIToken(ctx context.Context, id string, at time.Time) (bool, error) {
	uid, err := parseUUID(id)
	if err != nil {
		return false, fmt.Errorf("parsing api token id: %w", err)
	}
	n, err := r.q.TouchAPIToken(ctx, queries.TouchAPITokenParams{ID: uid, At: pgtype.Timestamptz{Time: at, Valid: true}})
	if err != nil {
		return false, fmt.Errorf("recording api token use: %w", err)
	}
	return n > 0, nil
}

// ListAPITokens returns a page of the tenant's unrevoked tokens, newest
// first, and their total. ownerID limits them to one user; empty lists every
// owner's.
func (r *Repository) ListAPITokens(ctx context.Context, tenant, ownerID string, limit, offset int) ([]auth.APIToken, int, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return nil, 0, err
	}
	owner, ok := ownerFilter(ownerID)
	if !ok {
		return []auth.APIToken{}, 0, nil
	}
	rows, err := r.q.ListAPITokens(ctx, queries.ListAPITokensParams{
		TenantID: tid, OwnerID: owner, RowLimit: toInt32(limit), RowOffset: toInt32(offset),
	})
	if err != nil {
		return nil, 0, fmt.Errorf("listing api tokens: %w", err)
	}
	total, err := r.q.CountAPITokens(ctx, queries.CountAPITokensParams{TenantID: tid, OwnerID: owner})
	if err != nil {
		return nil, 0, fmt.Errorf("counting api tokens: %w", err)
	}
	out := make([]auth.APIToken, 0, len(rows))
	for _, row := range rows {
		out = append(out, auth.APIToken{
			ID: uuidToString(row.ID), TenantID: tenant, UserID: uuidToString(row.UserID), UserEmail: row.UserEmail,
			Name: row.Name, Scopes: row.Scopes, CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time,
			LastUsedAt: timePtr(row.LastUsedAt),
		})
	}
	return out, int(total), nil
}

// RevokeAPIToken revokes the tenant's live token id and returns it with its
// owner. ownerID limits the revoke to that user's tokens; empty allows any
// owner's. A token that is not found under those limits, or is already
// revoked, is domain.ErrNotFound.
func (r *Repository) RevokeAPIToken(ctx context.Context, tenant, id, ownerID string) (auth.APIToken, error) {
	tid, err := r.tenantID(ctx, tenant)
	if err != nil {
		return auth.APIToken{}, err
	}
	tokenID, err := parseUUID(id)
	if err != nil {
		return auth.APIToken{}, domain.ErrNotFound
	}
	owner, ok := ownerFilter(ownerID)
	if !ok {
		return auth.APIToken{}, domain.ErrNotFound
	}
	row, err := r.q.RevokeAPIToken(ctx, queries.RevokeAPITokenParams{ID: tokenID, TenantID: tid, OwnerID: owner})
	if err != nil {
		return auth.APIToken{}, mapNotFound(err)
	}
	return auth.APIToken{ID: uuidToString(row.ID), TenantID: tenant, UserID: uuidToString(row.UserID), Name: row.Name}, nil
}

// ownerFilter turns an owner id into the query's filter: empty is no filter,
// and an id that is not a uuid matches no owner (ok false).
func ownerFilter(ownerID string) (pgtype.UUID, bool) {
	if ownerID == "" {
		return pgtype.UUID{}, true
	}
	uid, err := parseUUID(ownerID)
	return uid, err == nil
}
