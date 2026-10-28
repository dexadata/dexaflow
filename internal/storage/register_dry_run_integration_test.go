//go:build integration

package storage_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// dryRegister runs a dry run of spec with the is_paused override paused.
func dryRegister(ctx context.Context, repo *storage.Repository, spec domain.DAGSpec, paused *bool) (domain.DagRegistered, error) {
	hash, err := spec.CanonicalHash()
	if err != nil {
		return domain.DagRegistered{}, fmt.Errorf("hashing: %w", err)
	}
	return repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{
		Spec: spec, SpecHash: hash, IsPaused: paused, DryRun: true,
	})
}

// auditCount is the number of audit entries on dagID, of every action.
func auditCount(t *testing.T, repo *storage.Repository, dagID string) int {
	t.Helper()
	_, total, err := repo.ListAuditLogs(t.Context(), "default", dagID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	return total
}

// TestRegisterDryRunOfANewDagWritesNothing: a dry run of a DAG that does not
// exist says the register would create it, paused per the server setting,
// and leaves no DAG, version or audit entry behind (#1705).
func TestRegisterDryRunOfANewDagWritesNothing(t *testing.T) {
	repo, _, ctx := openRepo(t)
	repo.Configure(storage.WithDagsArePausedAtCreation(true))
	dagID := fmt.Sprintf("dry_new_%d", time.Now().UnixNano())
	spec := registerTxSpec(dagID, "v1", "a", 3, false)
	got, err := dryRegister(ctx, repo, spec, nil)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !got.Created || !got.DagCreated || !got.IsPaused {
		t.Errorf("dry run = %+v, want created, dag created, paused", got)
	}
	if _, gerr := repo.GetDag(ctx, "default", dagID); !errors.Is(gerr, domain.ErrNotFound) {
		t.Errorf("after a dry run GetDag err = %v, want not found", gerr)
	}
	if n := auditCount(t, repo, dagID); n != 0 {
		t.Errorf("a dry run wrote %d audit entries", n)
	}
	if real := registerAs(t, ctx, repo, "", spec, nil); !real.Created || !real.DagCreated {
		t.Errorf("the register after a dry run = %+v, want it to create the DAG", real)
	}
}

// TestRegisterDryRunOfARedeployWritesNothing: a dry run of a new version
// with an is_paused override says what the register would do, and the DAG
// keeps its current version, its state, its versions and its audit trail.
func TestRegisterDryRunOfARedeployWritesNothing(t *testing.T) {
	yes := true
	repo, _, ctx := openRepo(t)
	repo.Configure(storage.WithDagsArePausedAtCreation(false))
	dagID := fmt.Sprintf("dry_redeploy_%d", time.Now().UnixNano())
	registerAs(t, ctx, repo, "", registerTxSpec(dagID, "v1", "a", 3, false), nil)
	audits := auditCount(t, repo, dagID)

	got, err := dryRegister(ctx, repo, registerTxSpec(dagID, "v2", "bb", 5, true), &yes)
	if err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if !got.Created || got.DagCreated || !got.IsPaused {
		t.Errorf("dry run = %+v, want a new version of an existing DAG, paused", got)
	}
	versions, err := repo.ListDagVersions(ctx, "default", dagID)
	if err != nil {
		t.Fatal(err)
	}
	if len(versions) != 1 || versions[0].Version != "v1" || !versions[0].IsCurrent {
		t.Errorf("versions after a dry run = %+v, want only v1, current", versions)
	}
	if isPaused(t, repo, ctx, dagID) {
		t.Error("a dry run paused the DAG")
	}
	d, err := repo.GetDag(ctx, "default", dagID)
	if err != nil {
		t.Fatal(err)
	}
	if d.MaxActiveRuns != 3 {
		t.Errorf("max_active_runs = %d after a dry run, want v1's 3", d.MaxActiveRuns)
	}
	if n := auditCount(t, repo, dagID); n != audits {
		t.Errorf("audit entries %d -> %d after a dry run", audits, n)
	}
	again, err := dryRegister(ctx, repo, registerTxSpec(dagID, "v1", "a", 3, false), nil)
	if err != nil || again.Created {
		t.Errorf("dry run of registered content = %+v, %v; want not created", again, err)
	}
}

// TestRegisterDryRunRefusesAsTheRegisterWould: a label registered with other
// content and an undeclared connection give a dry run the error the register
// gets.
func TestRegisterDryRunRefusesAsTheRegisterWould(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("dry_refused_%d", time.Now().UnixNano())
	registerAs(t, ctx, repo, "", registerTxSpec(dagID, "v1", "a", 3, false), nil)
	conflict := registerTxSpec(dagID, "v1", "bb", 3, false)
	unknownConn := registerTxSpec(dagID, "v3", "a", 3, false)
	unknownConn.Connections = []string{fmt.Sprintf("missing_%d", time.Now().UnixNano())}
	for name, tc := range map[string]struct {
		spec domain.DAGSpec
		want error
	}{
		"version conflict":   {conflict, domain.ErrConflict},
		"unknown connection": {unknownConn, domain.ErrValidation},
	} {
		t.Run(name, func(t *testing.T) {
			_, dryErr := dryRegister(ctx, repo, tc.spec, nil)
			hash, herr := tc.spec.CanonicalHash()
			if herr != nil {
				t.Fatal(herr)
			}
			_, realErr := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{Spec: tc.spec, SpecHash: hash})
			if !errors.Is(realErr, tc.want) {
				t.Fatalf("register err = %v, want %v", realErr, tc.want)
			}
			if !errors.Is(dryErr, tc.want) || dryErr.Error() != realErr.Error() {
				t.Errorf("dry run err = %v, want the register's %v", dryErr, realErr)
			}
		})
	}
}
