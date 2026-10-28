//go:build integration

package storage_test

import (
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/domain"
	"github.com/dexadata/dexaflow/internal/storage"
)

// TestGetDagVersionSpecServesEachVersionsOwnSpec: after a rollback, the spec
// of a version is the one registered with it, not the current one; version 0
// names the newest version, as Airflow's dagSources does without
// version_number; a version that does not exist is not found; and a tenant
// reads only the versions of its own DAG of that id.
func TestGetDagVersionSpecServesEachVersionsOwnSpec(t *testing.T) {
	repo, _, ctx := openRepo(t)
	dagID := fmt.Sprintf("verspec_%d", time.Now().UnixNano())
	v1 := registerTxSpec(dagID, "v1", "a", 3, false)
	v1.Source = "# source of v1\n"
	v2 := registerTxSpec(dagID, "v2", "bb", 7, true)
	v2.Source = "# source of v2\n"
	registerTx(t, ctx, repo, v1)
	registerTx(t, ctx, repo, v2)
	if _, err := repo.SetCurrentDagVersion(ctx, "default", dagID, domain.DagVersionSelector{Number: 1}, "", admitAll); err != nil {
		t.Fatalf("roll back to v1: %v", err)
	}

	cases := []struct {
		number      int
		wantNumber  int
		wantLabel   string
		wantSource  string
		wantCurrent bool
	}{
		{number: 0, wantNumber: 2, wantLabel: "v2", wantSource: v2.Source},
		{number: 1, wantNumber: 1, wantLabel: "v1", wantSource: v1.Source, wantCurrent: true},
		{number: 2, wantNumber: 2, wantLabel: "v2", wantSource: v2.Source},
	}
	for _, tc := range cases {
		version, spec, err := repo.GetDagVersionSpec(ctx, "default", dagID, tc.number)
		if err != nil {
			t.Fatalf("version %d: %v", tc.number, err)
		}
		if version.VersionNumber != tc.wantNumber || version.Version != tc.wantLabel || version.IsCurrent != tc.wantCurrent {
			t.Errorf("version %d = %+v, want number %d, label %s, current %v", tc.number, version, tc.wantNumber, tc.wantLabel, tc.wantCurrent)
		}
		if version.ImageReference != "img:"+tc.wantLabel || version.SpecHash == "" || version.ID == "" {
			t.Errorf("version %d row is incomplete: %+v", tc.number, version)
		}
		if spec.Source != tc.wantSource || spec.DagVersion != tc.wantLabel {
			t.Errorf("version %d spec = %s with source %q, want %s with %q", tc.number, spec.DagVersion, spec.Source, tc.wantLabel, tc.wantSource)
		}
	}

	for _, n := range []int{3, -1} {
		if _, _, err := repo.GetDagVersionSpec(ctx, "default", dagID, n); !errors.Is(err, domain.ErrNotFound) {
			t.Errorf("version %d: err = %v, want not found", n, err)
		}
	}
	if _, _, err := repo.GetDagVersionSpec(ctx, "default", dagID+"_missing", 0); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("missing DAG: err = %v, want not found", err)
	}
	// A second tenant with a DAG of the same id and a single version reads its
	// own version only.
	other := uniqueTenant("verspec-b")
	pg, err := storage.NewPostgres(ctx, config.DatabaseSection{URL: os.Getenv("DATABASE_URL")})
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	t.Cleanup(pg.Close)
	if _, err = pg.Pool.Exec(ctx, `INSERT INTO tenants (name, display_name) VALUES ($1, $1)`, other); err != nil {
		t.Fatalf("inserting tenant %s: %v", other, err)
	}
	foreign := registerTxSpec(dagID, "foreign", "ccc", 1, false)
	foreign.Source = "# source of the other tenant\n"
	hash, err := foreign.CanonicalHash()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = repo.RegisterDagVersion(ctx, other, foreign, hash); err != nil {
		t.Fatalf("register in %s: %v", other, err)
	}
	if _, _, err = repo.GetDagVersionSpec(ctx, other, dagID, 2); !errors.Is(err, domain.ErrNotFound) {
		t.Errorf("version 2 as tenant %s: err = %v, want not found", other, err)
	}
	for _, n := range []int{0, 1} {
		_, spec, gerr := repo.GetDagVersionSpec(ctx, other, dagID, n)
		if gerr != nil || spec.Source != foreign.Source {
			t.Errorf("version %d as tenant %s = %q, %v; want its own source", n, other, spec.Source, gerr)
		}
	}
}
