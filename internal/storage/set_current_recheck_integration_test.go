//go:build integration

package storage

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestSetCurrentDagVersionTxRefusesAChangedTarget: the checks run before the
// transaction, so under the lock the target must still be the version that was
// checked. A target that resolves to another version (the DAG was deleted and
// registered again in between) is a conflict, and nothing is written.
func TestSetCurrentDagVersionTxRefusesAChangedTarget(t *testing.T) {
	repo, _, ctx := countingRepo(t)
	dagID := fmt.Sprintf("setcur_recheck_%d", time.Now().UnixNano())
	for _, v := range []string{"v1", "v2"} {
		spec := domain.DAGSpec{SchemaVersion: "1.0", DagID: dagID, DagVersion: v, Image: "img:" + v,
			Tasks: []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}}}
		hash, err := spec.CanonicalHash()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := repo.RegisterDagVersion(ctx, "default", spec, hash); err != nil {
			t.Fatal(err)
		}
	}
	tid, err := repo.tenantID(ctx, "default")
	if err != nil {
		t.Fatal(err)
	}
	checked, err := repo.checkSetCurrentTarget(ctx, tid, dagID, domain.DagVersionSelector{Version: "v1"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	for name, stale := range map[string]domain.DagVersion{
		"other id":   {ID: "00000000-0000-4000-8000-000000000002", SpecHash: checked.To.SpecHash},
		"other hash": {ID: checked.To.ID, SpecHash: "not-the-hash"},
	} {
		tx, err := repo.pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		_, err = setCurrentDagVersionTx(ctx, repo.q.WithTx(tx), tid, domain.DagSetCurrent{DagID: dagID, Selector: domain.DagVersionSelector{Version: "v1"}}, stale)
		_ = tx.Rollback(ctx) //nolint:errcheck // test cleanup
		if !errors.Is(err, domain.ErrConflict) {
			t.Errorf("%s: err = %v, want ErrConflict", name, err)
		}
	}
	if spec, err := repo.GetCurrentSpec(ctx, "default", dagID); err != nil || spec.DagVersion != "v2" {
		t.Errorf("current = %s, %v; want v2 unchanged", spec.DagVersion, err)
	}
}
