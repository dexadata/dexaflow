//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
	"github.com/dexadata/dexaflow/internal/storage/queries"
)

// TestRegisterLockDoesNotBlockRunCreation: the row lock a register holds on
// its DAG must not block creating a run of that DAG. A dag_runs insert takes
// KEY SHARE on the DAG row, which FOR UPDATE would wait on and FOR NO KEY
// UPDATE does not (#1702).
func TestRegisterLockDoesNotBlockRunCreation(t *testing.T) {
	url := os.Getenv("DATABASE_URL")
	if url == "" {
		t.Skip("DATABASE_URL must point at a migrated database for integration tests")
	}
	ctx := context.Background()
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: url})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	repo := storage.NewRepository(pg)
	dagID := fmt.Sprintf("reg_lock_runs_%d", time.Now().UnixNano())
	registerTx(t, ctx, repo, registerTxSpec(dagID, "v1", "a", 3, false))

	tx, err := pg.Pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	qtx := queries.New(tx)
	tenant, err := qtx.GetTenantByName(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := qtx.LockDagByDagID(ctx, queries.LockDagByDagIDParams{TenantID: tenant.ID, DagID: dagID}); err != nil {
		t.Fatalf("lock: %v", err)
	}

	runCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if _, err := repo.CreateDagRun(runCtx, "default", dagID, domain.DagRun{
		RunID: "manual_while_registering", State: domain.DagRunStateQueued, RunType: "manual",
		LogicalDate: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("creating a run while a register holds the DAG lock: %v", err)
	}
}
