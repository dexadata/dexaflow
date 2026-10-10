//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/dexadata/dexaflow/internal/auth"
)

// TestBootstrapAdminConcurrentIntegration reproduces the split-topology race
// (ADR 0049): the api and scheduler roles — and every active-active api replica —
// run bootstrapAdmin on startup, so several processes create the same admin at
// once. Before the fix the loser of the INSERT crashed the process with a raw
// unique-constraint 23505 ("duplicate key value violates ..."), taking down the
// scheduler in the split e2e. BootstrapAdminHash must instead treat a concurrent
// create as success (reconcile + report not-newly-created) so NO caller errors.
func TestBootstrapAdminConcurrentIntegration(t *testing.T) {
	repo, _, ctx := openRepo(t)
	hash, err := auth.HashPassword("concurrent-pw-1")
	if err != nil {
		t.Fatal(err)
	}

	// The TOCTOU window (reconcile-check → insert) is tiny, so a single small
	// fan-out reproduces the create-create race only intermittently. Run several
	// rounds, each with a FRESH email (so every caller races the CREATE path, not
	// the reconcile path) and a wide barrier-released fan-out, to hit the window
	// reliably. Without the fix at least one round errors with a raw 23505.
	const rounds, n = 12, 24
	for r := 0; r < rounds; r++ {
		email := fmt.Sprintf("concurrent-bootstrap-%d-%d@leoflow.local", time.Now().UnixNano(), r)
		t.Cleanup(func() { deleteUserByEmail(t, email) })

		var wg sync.WaitGroup
		errs := make([]error, n)
		created := make([]bool, n)
		start := make(chan struct{})
		for i := 0; i < n; i++ {
			wg.Add(1)
			go func(i int) {
				defer wg.Done()
				<-start // release all at once to maximize the create-create overlap
				created[i], errs[i] = repo.BootstrapAdminHash(ctx, "default", email, hash)
			}(i)
		}
		close(start)
		wg.Wait()

		nCreated := 0
		for i := 0; i < n; i++ {
			if errs[i] != nil {
				t.Fatalf("round %d goroutine %d: BootstrapAdminHash errored on the concurrent race (must be idempotent): %v", r, i, errs[i])
			}
			if created[i] {
				nCreated++
			}
		}
		if nCreated != 1 {
			t.Errorf("round %d: exactly one caller should report the admin as newly created, got %d", r, nCreated)
		}
		_, stored, ferr := repo.FindUserByLogin(ctx, "default", email)
		if ferr != nil {
			t.Fatalf("round %d: admin not found after concurrent bootstrap: %v", r, ferr)
		}
		if !auth.VerifyPassword(stored, "concurrent-pw-1") {
			t.Errorf("round %d: admin password does not verify after concurrent bootstrap", r)
		}
	}
}

// deleteUserByEmail removes a planted test admin (and its role links) so the
// concurrent-bootstrap test leaves no orphan behind.
func deleteUserByEmail(t *testing.T, email string) {
	t.Helper()
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Logf("cleanup: pool: %v", err)
		return
	}
	defer pool.Close()
	if _, err := pool.Exec(context.Background(),
		`DELETE FROM user_roles WHERE user_id IN (SELECT id FROM users WHERE email=$1)`, email); err != nil {
		t.Logf("cleanup user_roles: %v", err)
	}
	if _, err := pool.Exec(context.Background(), `DELETE FROM users WHERE email=$1`, email); err != nil {
		t.Logf("cleanup users: %v", err)
	}
}
