package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// cliDeviceSlowDownStep is what a slow_down adds to the interval (RFC 8628
// section 3.5), and cliDeviceMaxInterval the longest interval it grows to.
const (
	cliDeviceSlowDownStep = 5
	cliDeviceMaxInterval  = 600
)

// CreateCLIDeviceAuthorization starts a device sign-in of the CLI (ADR 0081)
// that expires ttl from now on the database's clock and may be polled every
// interval. It stores only the two hashes. It first forgets sign-ins that
// expired a while ago, then refuses with domain.ErrLimitExceeded when maxLive
// sign-ins are already live, so an unauthenticated caller cannot fill the
// table. A user code hash already in use is domain.ErrConflict: the caller
// draws another code.
func (r *Repository) CreateCLIDeviceAuthorization(ctx context.Context, deviceCodeHash, userCodeHash string, ttl, interval time.Duration, maxLive int) (auth.DeviceAuthorization, error) {
	if err := r.q.DeleteExpiredCLIDevices(ctx); err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("deleting expired device sign-ins: %w", err)
	}
	live, err := r.q.CountLiveCLIDevices(ctx)
	if err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("counting live device sign-ins: %w", err)
	}
	if live >= int64(maxLive) {
		return auth.DeviceAuthorization{}, domain.Safef(domain.ErrLimitExceeded, "too many device sign-ins are in progress; try again in a few minutes")
	}
	row, err := r.q.InsertCLIDevice(ctx, queries.InsertCLIDeviceParams{
		DeviceCodeHash:      deviceCodeHash,
		UserCodeHash:        userCodeHash,
		TtlSeconds:          ttl.Seconds(),
		PollIntervalSeconds: int32(interval / time.Second), //nolint:gosec // G115: the caller passes seconds well within int32
	})
	if err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("inserting device sign-in: %w", mapConflict(err))
	}
	return auth.DeviceAuthorization{ID: uuidToString(row.ID), CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time}, nil
}

// FindCLIDeviceByUserCode returns the pending, unexpired device sign-in whose
// user code hashes to userCodeHash, or domain.ErrNotFound.
func (r *Repository) FindCLIDeviceByUserCode(ctx context.Context, userCodeHash string) (auth.DeviceAuthorization, error) {
	row, err := r.q.FindPendingCLIDeviceByUserCode(ctx, userCodeHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.DeviceAuthorization{}, domain.ErrNotFound
	}
	if err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("finding device sign-in: %w", err)
	}
	return auth.DeviceAuthorization{ID: uuidToString(row.ID), CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time}, nil
}

// DecideCLIDeviceAuthorization records the person's answer to a pending,
// unexpired device sign-in: approved as user (its tenant, id, email and the
// roles the sign-in resolved), or canceled. A sign-in that is not pending,
// has expired, or, for an approval, a user who is not in user.TenantID, is
// domain.ErrNotFound and changes nothing, so a sign-in is decided once.
func (r *Repository) DecideCLIDeviceAuthorization(ctx context.Context, id string, approve bool, user auth.User) error {
	did, err := parseUUID(id)
	if err != nil {
		return domain.ErrNotFound
	}
	var n int64
	if approve {
		uid, uerr := parseUUID(user.ID)
		if uerr != nil {
			return domain.ErrNotFound
		}
		email := user.Email
		roles := user.Roles
		if roles == nil {
			roles = []string{}
		}
		n, err = r.q.ApproveCLIDevice(ctx, queries.ApproveCLIDeviceParams{
			ID: did, UserID: uid, Tenant: user.TenantID, Email: &email, Roles: roles,
		})
	} else {
		n, err = r.q.DenyCLIDevice(ctx, did)
	}
	if err != nil {
		return fmt.Errorf("deciding device sign-in: %w", err)
	}
	if n == 0 {
		return domain.ErrNotFound
	}
	return nil
}

// PollCLIDeviceAuthorization answers one poll of the device sign-in whose
// device code hashes to deviceCodeHash, in one transaction that locks its
// row, so concurrent polls on any replica see one another: an approved
// sign-in is redeemed, and deleted, by exactly one poll. A poll sooner than
// the interval is a slow_down that lengthens it; a pending one records the
// poll. A canceled sign-in answers denied once and is deleted.
func (r *Repository) PollCLIDeviceAuthorization(ctx context.Context, deviceCodeHash string) (auth.DevicePoll, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.DevicePoll{}, fmt.Errorf("beginning device poll tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	q := r.q.WithTx(tx)
	row, err := q.LockCLIDeviceForPoll(ctx, deviceCodeHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return auth.DevicePoll{Outcome: auth.DevicePollUnknown}, nil
	}
	if err != nil {
		return auth.DevicePoll{}, fmt.Errorf("locking device sign-in: %w", err)
	}
	poll, err := answerDevicePoll(ctx, q, row)
	if err != nil {
		return auth.DevicePoll{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.DevicePoll{}, fmt.Errorf("committing device poll: %w", err)
	}
	return poll, nil
}

// answerDevicePoll decides a poll of the locked row and writes what it
// changes, inside the poll's transaction.
func answerDevicePoll(ctx context.Context, q *queries.Queries, row queries.LockCLIDeviceForPollRow) (auth.DevicePoll, error) {
	switch {
	case row.Expired:
		return auth.DevicePoll{Outcome: auth.DevicePollExpired}, nil
	case row.Status == "approved" || row.Status == "denied":
		n, err := q.DeleteCLIDevice(ctx, row.ID)
		if err != nil {
			return auth.DevicePoll{}, fmt.Errorf("ending device sign-in: %w", err)
		}
		if n == 0 {
			return auth.DevicePoll{Outcome: auth.DevicePollUnknown}, nil
		}
		if row.Status == "denied" {
			return auth.DevicePoll{Outcome: auth.DevicePollDenied}, nil
		}
		return auth.DevicePoll{Outcome: auth.DevicePollApproved, User: approvedDeviceUser(row)}, nil
	}
	outcome, interval := auth.DevicePollPending, row.PollIntervalSeconds
	if row.TooFast != nil && *row.TooFast {
		outcome, interval = auth.DevicePollSlowDown, min(interval+cliDeviceSlowDownStep, cliDeviceMaxInterval)
	}
	if err := q.MarkCLIDevicePolled(ctx, queries.MarkCLIDevicePolledParams{ID: row.ID, PollIntervalSeconds: interval}); err != nil {
		return auth.DevicePoll{}, fmt.Errorf("recording device poll: %w", err)
	}
	return auth.DevicePoll{Outcome: outcome}, nil
}

// approvedDeviceUser is the user an approved row names.
func approvedDeviceUser(row queries.LockCLIDeviceForPollRow) auth.User {
	return auth.User{
		ID:       uuidToString(row.UserID),
		TenantID: derefString(row.Tenant),
		Email:    derefString(row.Email),
		Roles:    row.Roles,
	}
}

// derefString is *s, or "" for nil.
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
