//go:build integration

package storage_test

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// deviceHashes returns a distinct pair of well-formed code hashes.
func deviceHashes() (device, user string) {
	n := time.Now().UnixNano()
	return fmt.Sprintf("%064x", n), fmt.Sprintf("%064x", n+1)
}

// deviceRequest is a start from requester with generous caps.
func deviceRequest(requester string, ttl, interval time.Duration) auth.DeviceAuthorizationRequest {
	deviceHash, userHash := deviceHashes()
	return auth.DeviceAuthorizationRequest{
		DeviceCodeHash: deviceHash, UserCodeHash: userHash,
		RequesterHash: fmt.Sprintf("%x", sha256.Sum256([]byte(requester))), RequesterAddr: requester,
		TTL: ttl, Interval: interval, MaxLive: 100_000, MaxLivePerRequester: 100_000,
	}
}

func newDeviceAuthorization(t *testing.T, ctx context.Context, repo *storage.Repository, ttl, interval time.Duration) (deviceHash, userHash string, d auth.DeviceAuthorization) {
	t.Helper()
	req := deviceRequest("198.51.100.7", ttl, interval)
	deviceHash, userHash = req.DeviceCodeHash, req.UserCodeHash
	d, err := repo.CreateCLIDeviceAuthorization(ctx, req)
	if err != nil {
		t.Fatalf("CreateCLIDeviceAuthorization: %v", err)
	}
	return deviceHash, userHash, d
}

func poll(t *testing.T, ctx context.Context, repo *storage.Repository, deviceHash string) auth.DevicePoll {
	t.Helper()
	p, err := repo.PollCLIDeviceAuthorization(ctx, deviceHash)
	if err != nil {
		t.Fatalf("PollCLIDeviceAuthorization: %v", err)
	}
	return p
}

// TestCLIDeviceApprovedOnceRedeemedOnceIntegration: a pending sign-in is
// found by its user code, polls pending until approved, and the first poll
// after the approval redeems it with the approver's tenant, id, email and
// roles; every later poll finds nothing, and a second approval is refused.
func TestCLIDeviceApprovedOnceRedeemedOnceIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	userID, email := apiTokenOwner(t, ctx, repo, "default", "viewer")
	deviceHash, userHash, created := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)

	found, err := repo.FindCLIDeviceByUserCode(ctx, userHash)
	if err != nil || found.ID != created.ID || found.RequesterAddr != "198.51.100.7" || found.CreatedAt.IsZero() {
		t.Fatalf("FindCLIDeviceByUserCode = %+v, %v; want %s started from 198.51.100.7", found, err, created.ID)
	}
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollPending {
		t.Fatalf("poll before approval = %v, want pending", got.Outcome)
	}
	user := auth.User{ID: userID, TenantID: "default", Email: email, Roles: []string{"viewer", "editor"}}
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, true, user); err != nil {
		t.Fatalf("approve: %v", err)
	}
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, true, user); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("second approval err = %v, want ErrNotFound", err)
	}
	if _, err := repo.FindCLIDeviceByUserCode(ctx, userHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("an approved user code is still found: %v", err)
	}
	got := poll(t, ctx, repo, deviceHash)
	if got.Outcome != auth.DevicePollApproved {
		t.Fatalf("poll after approval = %v, want approved", got.Outcome)
	}
	if got.User.ID != userID || got.User.TenantID != "default" || got.User.Email != email ||
		fmt.Sprint(got.User.Roles) != "[viewer editor]" {
		t.Fatalf("redeemed user = %+v, want the approver with the sign-in's roles", got.User)
	}
	if again := poll(t, ctx, repo, deviceHash); again.Outcome != auth.DevicePollUnknown {
		t.Fatalf("poll after redemption = %v, want unknown (single use)", again.Outcome)
	}
}

// TestCLIDeviceRedeemedByOnePollIntegration: concurrent polls of an approved
// sign-in, as from two replicas, redeem it exactly once. A transaction holds
// the row while the polls start, so they all read it before any can redeem
// it: the overlap is forced, not left to timing.
func TestCLIDeviceRedeemedByOnePollIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	userID, email := apiTokenOwner(t, ctx, repo, "default", "viewer")
	deviceHash, _, created := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, true, auth.User{ID: userID, TenantID: "default", Email: email, Roles: []string{"viewer"}}); err != nil {
		t.Fatal(err)
	}
	hold, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = hold.Rollback(ctx) }()
	if _, err := hold.Exec(ctx, `SELECT 1 FROM cli_device_authorizations WHERE id = $1 FOR UPDATE`, created.ID); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	approved := 0
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p, err := repo.PollCLIDeviceAuthorization(ctx, deviceHash)
			if err != nil {
				t.Errorf("poll: %v", err)
				return
			}
			if p.Outcome == auth.DevicePollApproved {
				mu.Lock()
				approved++
				mu.Unlock()
			}
		}()
	}
	time.Sleep(300 * time.Millisecond)
	if err := hold.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	if approved != 1 {
		t.Fatalf("%d polls redeemed the sign-in, want exactly 1", approved)
	}
}

// TestCLIDeviceDeniedAnsweredOnceIntegration: a canceled sign-in answers
// denied once, then nothing.
func TestCLIDeviceDeniedAnsweredOnceIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	deviceHash, _, created := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, false, auth.User{}); err != nil {
		t.Fatalf("deny: %v", err)
	}
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollDenied {
		t.Fatalf("poll after cancel = %v, want denied", got.Outcome)
	}
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollUnknown {
		t.Fatalf("second poll after cancel = %v, want unknown", got.Outcome)
	}
}

// TestCLIDevicePollingFasterThanTheIntervalSlowsDownIntegration: a poll
// sooner than the interval is slow_down, and one after it is pending again.
func TestCLIDevicePollingFasterThanTheIntervalSlowsDownIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	deviceHash, _, _ := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollPending {
		t.Fatalf("first poll = %v, want pending", got.Outcome)
	}
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollSlowDown {
		t.Fatalf("immediate second poll = %v, want slow_down", got.Outcome)
	}
	// slow_down added five seconds to the one-second interval.
	time.Sleep(1100 * time.Millisecond)
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollSlowDown {
		t.Fatalf("poll after the old interval = %v, want slow_down (the interval grew)", got.Outcome)
	}

	other, _, _ := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	poll(t, ctx, repo, other)
	time.Sleep(1100 * time.Millisecond)
	if got := poll(t, ctx, repo, other); got.Outcome != auth.DevicePollPending {
		t.Fatalf("poll after the interval = %v, want pending", got.Outcome)
	}
}

// TestCLIDeviceExpiresIntegration: past its expiry a sign-in is not found by
// its user code, cannot be approved, and polls expired.
func TestCLIDeviceExpiresIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	userID, email := apiTokenOwner(t, ctx, repo, "default", "viewer")
	deviceHash, userHash, created := newDeviceAuthorization(t, ctx, repo, time.Second, time.Second)
	time.Sleep(1100 * time.Millisecond)
	if _, err := repo.FindCLIDeviceByUserCode(ctx, userHash); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("expired user code found: %v", err)
	}
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, true, auth.User{ID: userID, TenantID: "default", Email: email}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("approving an expired sign-in err = %v, want ErrNotFound", err)
	}
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollExpired {
		t.Fatalf("poll after expiry = %v, want expired", got.Outcome)
	}
}

// TestCLIDeviceApprovalCarriesItsTenantIntegration: an approval names the
// approver's tenant and is refused when the user is not in it, so a sign-in
// can never be redeemed for another tenant's account.
func TestCLIDeviceApprovalCarriesItsTenantIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	other := secondTenant(t, ctx, repo)
	userID, email := apiTokenOwner(t, ctx, repo, "default", "viewer")
	deviceHash, _, created := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, true, auth.User{ID: userID, TenantID: other, Email: email}); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("approval naming another tenant err = %v, want ErrNotFound", err)
	}
	if got := poll(t, ctx, repo, deviceHash); got.Outcome != auth.DevicePollPending {
		t.Fatalf("poll after the refused approval = %v, want still pending", got.Outcome)
	}
}

// TestCLIDeviceCreateIsCappedAndUniqueIntegration: a create over the cap of
// live sign-ins, or over the cap of one requester, is refused with the
// matching error, and a user code already live is ErrConflict.
func TestCLIDeviceCreateIsCappedAndUniqueIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	full := deviceRequest("198.51.100.7", time.Minute, time.Second)
	full.MaxLive = 0
	if _, err := repo.CreateCLIDeviceAuthorization(ctx, full); !errors.Is(err, auth.ErrDeviceSignInsFull) {
		t.Fatalf("create over the cap err = %v, want ErrDeviceSignInsFull", err)
	}
	requester := fmt.Sprintf("2001:db8:%x::1", time.Now().UnixNano()&0xffff)
	for i := range 3 {
		req := deviceRequest(requester, time.Minute, time.Second)
		req.RequesterHash = strings.Repeat("a", 64)
		req.MaxLivePerRequester = 2
		_, err := repo.CreateCLIDeviceAuthorization(ctx, req)
		switch {
		case i < 2 && err != nil:
			t.Fatalf("create %d from one requester: %v", i+1, err)
		case i == 2 && !errors.Is(err, auth.ErrDeviceSignInsFromRequester):
			t.Fatalf("third create from one requester err = %v, want ErrDeviceSignInsFromRequester", err)
		}
	}
	other := deviceRequest(requester, time.Minute, time.Second)
	other.RequesterHash, other.MaxLivePerRequester = strings.Repeat("b", 64), 2
	if _, err := repo.CreateCLIDeviceAuthorization(ctx, other); err != nil {
		t.Fatalf("create from another requester: %v", err)
	}
	_, userHash, _ := newDeviceAuthorization(t, ctx, repo, time.Minute, time.Second)
	dup := deviceRequest("198.51.100.7", time.Minute, time.Second)
	dup.UserCodeHash = userHash
	if _, err := repo.CreateCLIDeviceAuthorization(ctx, dup); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("create with a live user code err = %v, want ErrConflict", err)
	}
	pg := openPG(t)
	if _, err := pg.Pool.Exec(ctx, `DELETE FROM cli_device_authorizations WHERE requester_hash IN ($1, $2)`, strings.Repeat("a", 64), strings.Repeat("b", 64)); err != nil {
		t.Fatal(err)
	}
}

// TestCLIDeviceCapHoldsUnderConcurrentStartsIntegration: concurrent starts
// never overshoot the cap of live sign-ins.
func TestCLIDeviceCapHoldsUnderConcurrentStartsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	var live int
	if err := pg.Pool.QueryRow(ctx, `SELECT count(*) FROM cli_device_authorizations WHERE expires_at > now()`).Scan(&live); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	var mu sync.Mutex
	created := 0
	start := make(chan struct{})
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := deviceRequest("198.51.100.9", time.Minute, time.Second)
			req.DeviceCodeHash = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("d-%d-%d", time.Now().UnixNano(), i))))
			req.UserCodeHash = fmt.Sprintf("%x", sha256.Sum256([]byte(fmt.Sprintf("u-%d-%d", time.Now().UnixNano(), i))))
			req.MaxLive = live + 5
			<-start
			if _, err := repo.CreateCLIDeviceAuthorization(ctx, req); err == nil {
				mu.Lock()
				created++
				mu.Unlock()
			} else if !errors.Is(err, auth.ErrDeviceSignInsFull) {
				t.Errorf("create: %v", err)
			}
		}()
	}
	close(start)
	wg.Wait()
	if created != 5 {
		t.Fatalf("%d concurrent starts were created under a cap with room for 5", created)
	}
}

// TestCLIDeviceApprovedInASecondTenantIntegration: a sign-in approved by a
// user of another tenant redeems for that user and tenant.
func TestCLIDeviceApprovedInASecondTenantIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	other := secondTenant(t, ctx, repo)
	userID, email := apiTokenOwner(t, ctx, repo, other, "viewer")
	deviceHash, _, created := newDeviceAuthorization(t, ctx, repo, 10*time.Minute, time.Second)
	if err := repo.DecideCLIDeviceAuthorization(ctx, created.ID, true, auth.User{ID: userID, TenantID: other, Email: email, Roles: []string{"viewer"}}); err != nil {
		t.Fatalf("approve in %s: %v", other, err)
	}
	got := poll(t, ctx, repo, deviceHash)
	if got.Outcome != auth.DevicePollApproved || got.User.TenantID != other || got.User.ID != userID {
		t.Fatalf("poll = %+v, want approved for %s in %s", got, userID, other)
	}
}

// TestCLIDeviceStartGivesUpOnABusyLockIntegration: a start that cannot take
// the start lock within the lock timeout gives up with ErrDeviceStartsBusy
// instead of queueing behind it.
func TestCLIDeviceStartGivesUpOnABusyLockIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	holder, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = holder.Rollback(ctx) }()
	if _, err := holder.Exec(ctx, `SELECT pg_advisory_xact_lock(1704081)`); err != nil {
		t.Fatal(err)
	}
	began := time.Now()
	bounded, cancel := context.WithTimeout(ctx, 10*time.Second) // a start with no lock timeout would wait forever
	defer cancel()
	_, err = repo.CreateCLIDeviceAuthorization(bounded, deviceRequest("198.51.100.10", time.Minute, time.Second))
	waited := time.Since(began)
	if !errors.Is(err, auth.ErrDeviceStartsBusy) {
		t.Fatalf("start behind a held lock = %v, want ErrDeviceStartsBusy", err)
	}
	if waited > 5*time.Second {
		t.Errorf("the start waited %v for the lock, want about the 2s lock timeout", waited)
	}
	if err := holder.Rollback(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.CreateCLIDeviceAuthorization(ctx, deviceRequest("198.51.100.10", time.Minute, time.Second)); err != nil {
		t.Fatalf("start once the lock is free: %v", err)
	}
}

// TestCLIDeviceSweepForgetsExpiredSignInsIntegration: the sweep deletes
// sign-ins ten minutes past their expiry, with the address they were
// started from, and keeps the rest.
func TestCLIDeviceSweepForgetsExpiredSignInsIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	pg := openPG(t)
	_, _, old := newDeviceAuthorization(t, ctx, repo, time.Minute, time.Second)
	_, _, recent := newDeviceAuthorization(t, ctx, repo, time.Minute, time.Second)
	_, _, live := newDeviceAuthorization(t, ctx, repo, time.Minute, time.Second)
	if _, err := pg.Pool.Exec(ctx, `UPDATE cli_device_authorizations SET created_at = now() - interval '25 minutes', expires_at = now() - interval '11 minutes' WHERE id = $1`, old.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := pg.Pool.Exec(ctx, `UPDATE cli_device_authorizations SET created_at = now() - interval '15 minutes', expires_at = now() - interval '5 minutes' WHERE id = $1`, recent.ID); err != nil {
		t.Fatal(err)
	}
	n, err := repo.DeleteExpiredCLIDeviceAuthorizations(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Errorf("swept %d rows, want at least the one past its grace", n)
	}
	for id, want := range map[string]bool{old.ID: false, recent.ID: true, live.ID: true} {
		var exists bool
		if err := pg.Pool.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM cli_device_authorizations WHERE id = $1)`, id).Scan(&exists); err != nil {
			t.Fatal(err)
		}
		if exists != want {
			t.Errorf("row %s exists = %v after the sweep, want %v", id, exists, want)
		}
	}
}
