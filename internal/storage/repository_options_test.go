package storage_test

import (
	"context"
	"errors"
	"testing"

	"github.com/dexadata/dexaflow/internal/storage"
)

type nopXComDeleter struct{}

func (nopXComDeleter) Delete(context.Context, string) error { return nil }

// The server's repository must be told what a clear does with XCom values and
// whether new DAGs start paused: leaving either out silently changes behavior,
// so NewServerRepository refuses to build without them (#1949).
func TestNewServerRepositoryRequiresServerDependencies(t *testing.T) {
	xcom := storage.WithXComBackend(nopXComDeleter{})
	paused := storage.WithDagsArePausedAtCreation(true)
	for _, tc := range []struct {
		name string
		opts []storage.Option
		want error
	}{
		{"no xcom backend", []storage.Option{paused}, storage.ErrNoXComBackend},
		{"nil xcom backend", []storage.Option{storage.WithXComBackend(nil), paused}, storage.ErrNoXComBackend},
		{"no paused at creation", []storage.Option{xcom}, storage.ErrNoPausedAtCreation},
		{"nothing", nil, storage.ErrNoXComBackend},
	} {
		if _, err := storage.NewServerRepository(&storage.Postgres{}, tc.opts...); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}

	// paused_at_creation=false is a decision, not a missing one.
	repo, err := storage.NewServerRepository(&storage.Postgres{}, xcom, storage.WithDagsArePausedAtCreation(false))
	if err != nil {
		t.Fatalf("all required options given: %v", err)
	}
	if repo.DagsArePausedAtCreation() {
		t.Error("WithDagsArePausedAtCreation(false) built a repository that pauses new DAGs")
	}
}

// A bare repository, the one tools and tests build, needs no option and keeps
// the documented defaults.
func TestNewRepositoryDefaults(t *testing.T) {
	repo := storage.NewRepository(&storage.Postgres{})
	if repo.DagsArePausedAtCreation() || repo.HasCipher() {
		t.Errorf("bare repository: paused at creation %v, cipher %v; want neither",
			repo.DagsArePausedAtCreation(), repo.HasCipher())
	}
	repo = storage.NewRepository(&storage.Postgres{}, storage.WithDagsArePausedAtCreation(true))
	if !repo.DagsArePausedAtCreation() {
		t.Error("WithDagsArePausedAtCreation(true) was not applied")
	}
}
