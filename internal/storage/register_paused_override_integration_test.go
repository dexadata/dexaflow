//go:build integration

package storage_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// registerAs registers spec with the is_paused override paused, failing the
// test on error.
func registerAs(t *testing.T, ctx context.Context, repo *storage.Repository, actor string, spec domain.DAGSpec, paused *bool) domain.DagRegistered {
	t.Helper()
	hash, err := spec.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{
		Spec: spec, SpecHash: hash, ActorUserID: actor, IsPaused: paused,
	})
	if err != nil {
		t.Fatalf("register %s@%s: %v", spec.DagID, spec.DagVersion, err)
	}
	return got
}

// countAudits counts the audit entries of action on dagID.
func countAudits(t *testing.T, repo *storage.Repository, dagID, action string) int {
	t.Helper()
	entries, _, err := repo.ListAuditLogs(t.Context(), "default", dagID, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if e.Action == action {
			n++
		}
	}
	return n
}

// TestRegisterPausedOverrideOnCreation: an explicit is_paused wins over the
// server setting and the spec's is_paused_upon_creation for a DAG the
// register creates, and the register's audit entry records it (#1705).
func TestRegisterPausedOverrideOnCreation(t *testing.T) {
	yes, no := true, false
	repo, _, ctx := openRepo(t)
	repo.Configure(storage.WithDagsArePausedAtCreation(true))
	dagID := fmt.Sprintf("override_create_%d", time.Now().UnixNano())
	spec := registerTxSpec(dagID, "v1", "a", 3, false)
	spec.IsPausedUponCreation = &yes
	got := registerAs(t, ctx, repo, "", spec, &no)
	if !got.Created || got.IsPaused {
		t.Fatalf("result = %+v, want created and active", got)
	}
	if isPaused(t, repo, ctx, dagID) {
		t.Fatal("is_paused = true, want the override (false)")
	}
	var meta map[string]any
	if err := json.Unmarshal([]byte(latestAudit(t, repo, dagID, "dag.version.register").Extra), &meta); err != nil {
		t.Fatal(err)
	}
	if meta["is_paused"] != false {
		t.Errorf("register audit metadata %v, want is_paused false", meta)
	}
}

// TestRegisterPausedOverrideOnRedeploy: an explicit is_paused changes an
// existing DAG, with a new version or with content already registered, and
// each change is audited with the acting user; an override equal to the
// current state writes nothing (#1705).
func TestRegisterPausedOverrideOnRedeploy(t *testing.T) {
	no := false
	repo, _, ctx := openRepo(t)
	repo.Configure(storage.WithDagsArePausedAtCreation(true))
	user, err := repo.CreateUser(ctx, "default", fmt.Sprintf("pauser-%d@example.com", time.Now().UnixNano()), "pw-123456789", nil)
	if err != nil {
		t.Fatal(err)
	}
	dagID := fmt.Sprintf("override_redeploy_%d", time.Now().UnixNano())
	v1 := registerTxSpec(dagID, "v1", "a", 3, false)
	if got := registerAs(t, ctx, repo, user.ID, v1, nil); !got.Created || !got.IsPaused {
		t.Fatalf("first register = %+v, want created and paused by the setting", got)
	}

	// A new version that unpauses.
	v2 := registerTxSpec(dagID, "v2", "bb", 3, false)
	if got := registerAs(t, ctx, repo, user.ID, v2, &no); !got.Created || got.IsPaused {
		t.Fatalf("v2 register = %+v, want created and active", got)
	}
	if isPaused(t, repo, ctx, dagID) {
		t.Fatal("is_paused after --active redeploy = true")
	}
	entry := latestAudit(t, repo, dagID, "dag.unpause")
	if entry.Owner != user.Email {
		t.Errorf("unpause audit owner = %q, want %q", entry.Owner, user.Email)
	}

	pauseKnownContent(t, ctx, repo, user.ID, v2)

	// Without an override the state is reported and left alone.
	v3 := registerTxSpec(dagID, "v3", "ccc", 3, false)
	if got := registerAs(t, ctx, repo, user.ID, v3, nil); !got.Created || !got.IsPaused {
		t.Fatalf("v3 register = %+v, want created and still paused", got)
	}
}

// pauseKnownContent registers v2, already registered and current, with
// is_paused true twice: the first pauses and is audited, the second changes
// nothing.
func pauseKnownContent(t *testing.T, ctx context.Context, repo *storage.Repository, actor string, v2 domain.DAGSpec) {
	t.Helper()
	yes, dagID := true, v2.DagID
	// The same content again, now pausing: no version, but the pause commits.
	if got := registerAs(t, ctx, repo, actor, v2, &yes); got.Created || !got.IsPaused {
		t.Fatalf("re-register = %+v, want not created and paused", got)
	}
	if !isPaused(t, repo, ctx, dagID) {
		t.Fatal("is_paused after a paused re-register of known content = false")
	}
	if n := countAudits(t, repo, dagID, "dag.pause"); n != 1 {
		t.Errorf("dag.pause audit entries = %d, want 1", n)
	}

	// The same state again: nothing changes and nothing is audited.
	if got := registerAs(t, ctx, repo, actor, v2, &yes); got.Created || !got.IsPaused {
		t.Fatalf("idempotent re-register = %+v, want not created and paused", got)
	}
	if n := countAudits(t, repo, dagID, "dag.pause"); n != 1 {
		t.Errorf("dag.pause audit entries = %d after a no-op, want still 1", n)
	}
}

// TestRegisterPausedOverrideRolledBackWithARefusedVersion: a register refused
// for a label conflict leaves is_paused as it was.
func TestRegisterPausedOverrideRolledBackWithARefusedVersion(t *testing.T) {
	no := false
	repo, _, ctx := openRepo(t)
	repo.Configure(storage.WithDagsArePausedAtCreation(true))
	dagID := fmt.Sprintf("override_conflict_%d", time.Now().UnixNano())
	registerAs(t, ctx, repo, "", registerTxSpec(dagID, "v1", "a", 3, false), nil)
	clash := registerTxSpec(dagID, "v1", "bb", 3, false)
	hash, err := clash.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := repo.RegisterDagVersionAs(ctx, "default", domain.DagRegistration{Spec: clash, SpecHash: hash, IsPaused: &no}); err == nil {
		t.Fatal("want a conflict for the same label with other content")
	}
	if !isPaused(t, repo, ctx, dagID) {
		t.Fatal("a refused register unpaused the DAG")
	}
	if n := countAudits(t, repo, dagID, "dag.unpause"); n != 0 {
		t.Errorf("dag.unpause audit entries = %d after a refused register, want 0", n)
	}
}

// TestSetPausedByAudits: a pause or unpause through the Airflow PATCH is
// audited with the acting user when it changes the DAG, and a request for the
// state the DAG is already in records nothing (#1705).
func TestSetPausedByAudits(t *testing.T) {
	repo, _, ctx := openRepo(t)
	repo.Configure(storage.WithDagsArePausedAtCreation(true))
	user, err := repo.CreateUser(ctx, "default", fmt.Sprintf("toggler-%d@example.com", time.Now().UnixNano()), "pw-123456789", nil)
	if err != nil {
		t.Fatal(err)
	}
	dagID := fmt.Sprintf("set_paused_by_%d", time.Now().UnixNano())
	registerAs(t, ctx, repo, "", registerTxSpec(dagID, "v1", "a", 3, false), nil)
	d, err := repo.SetPausedBy(ctx, "default", user.ID, dagID, false)
	if err != nil || d.IsPaused {
		t.Fatalf("unpause = %+v, %v", d, err)
	}
	if entry := latestAudit(t, repo, dagID, "dag.unpause"); entry.Owner != user.Email {
		t.Errorf("unpause audit owner = %q, want %q", entry.Owner, user.Email)
	}
	if _, err := repo.SetPausedBy(ctx, "default", user.ID, dagID, false); err != nil {
		t.Fatal(err)
	}
	if n := countAudits(t, repo, dagID, "dag.unpause"); n != 1 {
		t.Errorf("dag.unpause audit entries = %d after a no-op, want 1", n)
	}
	if _, err := repo.SetPausedBy(ctx, "default", user.ID, "no_such_dag_"+dagID, true); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("unknown DAG: err = %v, want not found", err)
	}
}

// TestRegisterReportsWhetherItCreatedTheDag: a new version of an existing DAG
// is created (a version) but did not create the DAG; the deploy summary needs
// the difference to tell a first schedule from a redeploy.
func TestRegisterReportsWhetherItCreatedTheDag(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("dag_created_%d", time.Now().UnixNano())
	first := registerAs(t, ctx, repo, "", registerTxSpec(dagID, "v1", "a", 3, false), nil)
	if !first.Created || !first.DagCreated {
		t.Fatalf("first register = %+v, want a version and a new DAG", first)
	}
	next := registerAs(t, ctx, repo, "", registerTxSpec(dagID, "v2", "bb", 3, false), nil)
	if !next.Created || next.DagCreated {
		t.Fatalf("second register = %+v, want a version of an existing DAG", next)
	}
}
