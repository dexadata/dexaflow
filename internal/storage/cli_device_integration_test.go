//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
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

func newDeviceAuthorization(t *testing.T, ctx context.Context, repo *storage.Repository, ttl, interval time.Duration) (deviceHash, userHash string, d auth.DeviceAuthorization) {
	t.Helper()
	deviceHash, userHash = deviceHashes()
	d, err := repo.CreateCLIDeviceAuthorization(ctx, deviceHash, userHash, ttl, interval, 10_000)
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
	if err != nil || found.ID != created.ID {
		t.Fatalf("FindCLIDeviceByUserCode = %+v, %v; want %s", found, err, created.ID)
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
// live sign-ins is ErrLimitExceeded, and a user code already live is
// ErrConflict.
func TestCLIDeviceCreateIsCappedAndUniqueIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	d, u := deviceHashes()
	if _, err := repo.CreateCLIDeviceAuthorization(ctx, d, u, time.Minute, time.Second, 0); !errors.Is(err, domain.ErrLimitExceeded) {
		t.Fatalf("create over the cap err = %v, want ErrLimitExceeded", err)
	}
	_, userHash, _ := newDeviceAuthorization(t, ctx, repo, time.Minute, time.Second)
	d2, _ := deviceHashes()
	if _, err := repo.CreateCLIDeviceAuthorization(ctx, d2, userHash, time.Minute, time.Second, 10_000); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("create with a live user code err = %v, want ErrConflict", err)
	}
}
