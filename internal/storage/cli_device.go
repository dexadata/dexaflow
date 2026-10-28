package storage

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// cliDeviceStartLockTimeout bounds how long a start waits for its turn
// among concurrent starts (lock_timeout syntax).
const cliDeviceStartLockTimeout = "2s"

// cliDeviceSlowDownStep is what a slow_down adds to the interval (RFC 8628
// section 3.5), and cliDeviceMaxInterval the longest interval it grows to.
const (
	cliDeviceSlowDownStep = 5
	cliDeviceMaxInterval  = 600
)

// CreateCLIDeviceAuthorization starts a device sign-in of the CLI (ADR 0081)
// that expires req.TTL from now on the database's clock and may be polled
// every req.Interval. It stores only the hashes and the requester's address.
// In one transaction serialized with every other start, it forgets sign-ins
// that expired a while ago and refuses with auth.ErrDeviceSignInsFull when
// req.MaxLive are live, or auth.ErrDeviceSignInsFromRequester when the
// requester holds req.MaxLivePerRequester, so the caps hold exactly. A start
// that cannot take its turn within cliDeviceStartLockTimeout is
// auth.ErrDeviceStartsBusy. A user code hash already in use is
// domain.ErrConflict: the caller draws another.
func (r *Repository) CreateCLIDeviceAuthorization(ctx context.Context, req auth.DeviceAuthorizationRequest) (auth.DeviceAuthorization, error) {
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("beginning device start tx: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }() //nolint:errcheck // best-effort; the commit path returns the meaningful error
	q := r.q.WithTx(tx)
	// A start waits for the start lock at most cliDeviceStartLockTimeout:
	// past it, the start is refused as busy instead of queueing connections
	// behind a slow one.
	if _, err = tx.Exec(ctx, "SET LOCAL lock_timeout = '"+cliDeviceStartLockTimeout+"'"); err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("bounding the device start lock wait: %w", err)
	}
	if err = q.LockCLIDeviceStarts(ctx); err != nil {
		if pgErr := (*pgconn.PgError)(nil); errors.As(err, &pgErr) && pgErr.Code == pgLockNotAvailable {
			return auth.DeviceAuthorization{}, auth.ErrDeviceStartsBusy
		}
		return auth.DeviceAuthorization{}, fmt.Errorf("serializing device starts: %w", err)
	}
	if _, err = q.DeleteExpiredCLIDevices(ctx); err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("deleting expired device sign-ins: %w", err)
	}
	live, err := q.CountLiveCLIDevices(ctx, req.RequesterHash)
	if err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("counting live device sign-ins: %w", err)
	}
	switch {
	case live.Live >= int64(req.MaxLive):
		return auth.DeviceAuthorization{}, auth.ErrDeviceSignInsFull
	case live.FromRequester >= int64(req.MaxLivePerRequester):
		return auth.DeviceAuthorization{}, auth.ErrDeviceSignInsFromRequester
	}
	row, err := q.InsertCLIDevice(ctx, queries.InsertCLIDeviceParams{
		DeviceCodeHash:      req.DeviceCodeHash,
		UserCodeHash:        req.UserCodeHash,
		TtlSeconds:          req.TTL.Seconds(),
		PollIntervalSeconds: int32(req.Interval / time.Second), //nolint:gosec // G115: the caller passes seconds well within int32
		RequesterHash:       req.RequesterHash,
		RequesterAddr:       req.RequesterAddr,
	})
	if err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("inserting device sign-in: %w", mapConflict(err))
	}
	if err := tx.Commit(ctx); err != nil {
		return auth.DeviceAuthorization{}, fmt.Errorf("committing device start: %w", err)
	}
	return auth.DeviceAuthorization{ID: uuidToString(row.ID), CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time, RequesterAddr: req.RequesterAddr}, nil
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
	return auth.DeviceAuthorization{ID: uuidToString(row.ID), CreatedAt: row.CreatedAt.Time, ExpiresAt: row.ExpiresAt.Time, RequesterAddr: row.RequesterAddr}, nil
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

// DeleteExpiredCLIDeviceAuthorizations forgets the device sign-ins that
// expired more than ten minutes ago, with the addresses they were started
// from, and reports how many. The server runs it every minute
// (cmd/dexaflow-server), so no address outlives its sign-in by much more
// than ten minutes even when nobody starts another.
func (r *Repository) DeleteExpiredCLIDeviceAuthorizations(ctx context.Context) (int64, error) {
	n, err := r.q.DeleteExpiredCLIDevices(ctx)
	if err != nil {
		return 0, fmt.Errorf("deleting expired device sign-ins: %w", err)
	}
	return n, nil
}
