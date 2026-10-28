package main

import (
	"testing"

	"github.com/dexadata/dexaflow/internal/config"
	"github.com/dexadata/dexaflow/internal/storage"
)

// The server hands dags_are_paused_at_creation to the repository that
// registers DAGs (#1705): on by default, off when the operator opts out.
func TestRegistrationOptionPassesPausedAtCreation(t *testing.T) {
	for _, tc := range []struct {
		env  string
		want bool
	}{{"", true}, {"false", false}, {"true", true}} {
		if tc.env != "" {
			t.Setenv("DEXAFLOW_DAGS_ARE_PAUSED_AT_CREATION", tc.env)
		}
		cfg, err := config.LoadServer("", nil)
		if err != nil {
			t.Fatal(err)
		}
		repo := storage.NewRepository(&storage.Postgres{},
			storage.WithDagsArePausedAtCreation(!tc.want), registrationOption(cfg))
		if got := repo.DagsArePausedAtCreation(); got != tc.want {
			t.Errorf("env %q: repository pauses new DAGs = %v, want %v", tc.env, got, tc.want)
		}
	}
}
