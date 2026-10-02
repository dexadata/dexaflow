//go:build integration

package storage_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/domain"
)

// TestSettledRunsIntegration: SettledRuns reports exactly the asked runs that
// are success or failed; an active run, an unknown id and a malformed id are
// left out, so the reconciler never collects pods of a run that may still
// dispatch.
func TestSettledRunsIntegration(t *testing.T) {
	repo, sched, exec, ctx := openExec(t)
	ids := map[domain.DagRunState]string{}
	for i, st := range []domain.DagRunState{domain.DagRunStateSuccess, domain.DagRunStateFailed, domain.DagRunStateRunning, domain.DagRunStateQueued} {
		dagID := fmt.Sprintf("settled_runs_%d_%d", time.Now().UnixNano(), i)
		registerSpec(t, repo, ctx, dagID, []domain.TaskSpec{{TaskID: "t", Type: domain.TaskTypePython}})
		if _, err := repo.CreateDagRun(ctx, "default", dagID, domain.DagRun{
			RunID: "r", State: domain.DagRunStateQueued, RunType: "manual", LogicalDate: time.Now().UTC(),
		}); err != nil {
			t.Fatalf("create run: %v", err)
		}
		ids[st] = resolveRunUUID(t, sched, ctx, dagID)
		if err := repo.SetDagRunState(ctx, "default", dagID, "r", string(st)); err != nil {
			t.Fatalf("set state: %v", err)
		}
	}
	got, err := exec.SettledRuns(ctx, []string{
		ids[domain.DagRunStateSuccess], ids[domain.DagRunStateFailed], ids[domain.DagRunStateRunning],
		ids[domain.DagRunStateQueued], "00000000-0000-0000-0000-000000000000", "not-a-uuid",
	})
	if err != nil {
		t.Fatalf("SettledRuns: %v", err)
	}
	if len(got) != 2 || !got[ids[domain.DagRunStateSuccess]] || !got[ids[domain.DagRunStateFailed]] {
		t.Fatalf("SettledRuns = %v, want only the success and failed runs", got)
	}
}
