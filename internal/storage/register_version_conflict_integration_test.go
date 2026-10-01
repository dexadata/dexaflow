//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestRegisterDagVersionDuplicateVersionConflict guards the (dag_id, version)
// unique constraint: pushing the SAME version string with DIFFERENT spec content
// misses the spec-hash dedup and collides on dag_versions_unique. That collision
// must surface as domain.ErrConflict (which the API maps to 409), NOT as a raw
// Postgres 23505 that leaks the constraint name through a 500 (#746).
func TestRegisterDagVersionDuplicateVersionConflict(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("dup_ver_%d", time.Now().UnixNano())

	first := domain.DAGSpec{
		SchemaVersion: "1.0", DagID: dagID, DagVersion: "dev", Image: "img:dev",
		Tasks: []domain.TaskSpec{{TaskID: "a", Type: domain.TaskTypePython}},
	}
	firstHash, err := first.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if created, rerr := repo.RegisterDagVersion(ctx, "default", first, firstHash); rerr != nil || !created {
		t.Fatalf("first register: created=%v err=%v", created, rerr)
	}

	// Same version string "dev", different content -> different spec hash, so the
	// hash dedup does not short-circuit; the insert hits dag_versions_unique.
	second := first
	second.Tasks = []domain.TaskSpec{{TaskID: "b", Type: domain.TaskTypePython}}
	secondHash, err := second.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if secondHash == firstHash {
		t.Fatalf("test setup: spec hashes must differ to bypass the dedup")
	}

	_, rerr := repo.RegisterDagVersion(ctx, "default", second, secondHash)
	if rerr == nil {
		t.Fatal("re-pushing version \"dev\" with new content must error, got nil")
	}
	if !errors.Is(rerr, domain.ErrConflict) {
		t.Fatalf("want errors.Is(err, domain.ErrConflict), got %v", rerr)
	}
	// The raw SQLSTATE / constraint name must not leak into the surfaced error.
	if msg := rerr.Error(); strings.Contains(msg, "23505") || strings.Contains(msg, "dag_versions_unique") {
		t.Errorf("conflict error leaks the raw pg constraint: %q", msg)
	}
}
