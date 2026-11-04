package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type starredFavorites struct {
	fakeFavoriteStore
	starred map[string]bool
	gotUser string
}

func (s *starredFavorites) FavoriteDagIDs(_ context.Context, _, userID string) (map[string]bool, error) {
	s.gotUser = userID
	return s.starred, nil
}

// TestDagDetailsCarryIsFavorite: the DAG page header reads its star from
// details.is_favorite. Without it the star shows "favorite" on a DAG the user
// already starred, and a click favorites it again instead of removing it.
func TestDagDetailsCarryIsFavorite(t *testing.T) {
	favs := &starredFavorites{starred: map[string]bool{"etl": true}}
	srv := newTestServer(Dependencies{
		Observability: ObservabilityDeps{Logger: discardLogger()},
		Server:        ServerDeps{CORSOrigins: []string{"*"}},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		},
		Stores: StoreDeps{
			Dags:      &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}, {DagID: "other"}}},
			Favorites: favs,
		},
	})
	for dag, want := range map[string]bool{"etl": true, "other": false} {
		rec := authGet(srv, http.MethodGet, "/api/v2/dags/"+dag+"/details", "")
		var d map[string]any
		if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
			t.Fatalf("details %s = %d %s", dag, rec.Code, rec.Body.String())
		}
		if got, ok := d["is_favorite"].(bool); !ok || got != want {
			t.Errorf("%s is_favorite = %v, want %v", dag, d["is_favorite"], want)
		}
	}
	if favs.gotUser != "u1" {
		t.Errorf("favorites read for user %q, want u1", favs.gotUser)
	}
}
