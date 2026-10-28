//go:build integration

package storage_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/storage"
)

func isPaused(t *testing.T, repo *storage.Repository, ctx context.Context, dagID string) bool {
	t.Helper()
	d, err := repo.GetDag(ctx, "default", dagID)
	if err != nil {
		t.Fatalf("get dag %s: %v", dagID, err)
	}
	return d.IsPaused
}

// TestRegisterNewDagPausedAtCreation: a DAG registered for the first time is
// paused when the server's dags_are_paused_at_creation is on, active when it is
// off, and the DAG's own is_paused_upon_creation wins over the setting in both
// directions (#1705).
func TestRegisterNewDagPausedAtCreation(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name    string
		setting bool
		dagSays *bool
		want    bool
	}{
		{"setting on", true, nil, true},
		{"setting off", false, nil, false},
		{"dag active overrides setting on", true, &no, false},
		{"dag paused overrides setting off", false, &yes, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, ctx := openRepo(t)
			repo.Configure(storage.WithDagsArePausedAtCreation(tc.setting))
			dagID := fmt.Sprintf("paused_create_%d", time.Now().UnixNano())
			spec := registerTxSpec(dagID, "v1", "a", 3, false)
			spec.IsPausedUponCreation = tc.dagSays
			if !registerTx(t, ctx, repo, spec) {
				t.Fatal("want created=true")
			}
			if got := isPaused(t, repo, ctx, dagID); got != tc.want {
				t.Fatalf("is_paused = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestRegisterRedeployNeverChangesPaused: a new version of an existing DAG
// keeps is_paused as the operator left it, whatever the setting and the new
// spec's is_paused_upon_creation say (#1705).
func TestRegisterRedeployNeverChangesPaused(t *testing.T) {
	yes, no := true, false
	for _, tc := range []struct {
		name      string
		unpause   bool
		setting   bool
		nextSays  *bool
		wantFinal bool
	}{
		{"unpaused DAG stays active", true, true, &yes, false},
		{"paused DAG stays paused", false, false, &no, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, _, ctx := openRepo(t)
			repo.Configure(storage.WithDagsArePausedAtCreation(true))
			dagID := fmt.Sprintf("paused_redeploy_%d", time.Now().UnixNano())
			registerTx(t, ctx, repo, registerTxSpec(dagID, "v1", "a", 3, false))
			if !isPaused(t, repo, ctx, dagID) {
				t.Fatal("setup: the new DAG must start paused")
			}
			if tc.unpause {
				if _, err := repo.SetPaused(ctx, "default", dagID, false); err != nil {
					t.Fatal(err)
				}
			}
			repo.Configure(storage.WithDagsArePausedAtCreation(tc.setting))
			next := registerTxSpec(dagID, "v2", "bb", 7, true)
			next.IsPausedUponCreation = tc.nextSays
			if !registerTx(t, ctx, repo, next) {
				t.Fatal("redeploy: want created=true")
			}
			if got := isPaused(t, repo, ctx, dagID); got != tc.wantFinal {
				t.Fatalf("is_paused after redeploy = %v, want %v", got, tc.wantFinal)
			}
		})
	}
}
