package api

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
)

type fakeFavoriteStore struct {
	mu      sync.Mutex
	added   []string
	removed []string
	err     error
}

func (f *fakeFavoriteStore) AddFavorite(_ context.Context, _, _, dagID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.added = append(f.added, dagID)
	return nil
}

func (f *fakeFavoriteStore) RemoveFavorite(_ context.Context, _, _, dagID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.removed = append(f.removed, dagID)
	return nil
}

func (f *fakeFavoriteStore) FavoriteDagIDs(context.Context, string, string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func favoritesServer(store FavoriteStore) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Favorites:     store,
	})
}

func TestFavoriteToggle(t *testing.T) {
	store := &fakeFavoriteStore{}
	srv := favoritesServer(store)

	if r := authGet(srv, http.MethodPost, "/api/v2/dags/etl/favorite", ""); r.Code != http.StatusNoContent {
		t.Fatalf("favorite = %d, want 204", r.Code)
	}
	if len(store.added) != 1 || store.added[0] != "etl" {
		t.Errorf("AddFavorite should be called with dag_id, got %v", store.added)
	}
	if r := authGet(srv, http.MethodPost, "/api/v2/dags/etl/unfavorite", ""); r.Code != http.StatusNoContent {
		t.Fatalf("unfavorite = %d, want 204", r.Code)
	}
	if len(store.removed) != 1 || store.removed[0] != "etl" {
		t.Errorf("RemoveFavorite should be called with dag_id, got %v", store.removed)
	}
}

// TestFavoriteStoreErrorIs500 breaks the store: a write failure must surface as
// 500, not a silent 204.
func TestFavoriteStoreErrorIs500(t *testing.T) {
	srv := favoritesServer(&fakeFavoriteStore{err: errors.New("db down")})
	if r := authGet(srv, http.MethodPost, "/api/v2/dags/etl/favorite", ""); r.Code != http.StatusInternalServerError {
		t.Errorf("store error should be 500, got %d", r.Code)
	}
}

// TestFavoriteDisabledWithoutStore: no store configured → routes absent (404).
func TestFavoriteDisabledWithoutStore(t *testing.T) {
	srv := favoritesServer(nil)
	if r := authGet(srv, http.MethodPost, "/api/v2/dags/etl/favorite", ""); r.Code != http.StatusNotFound {
		t.Errorf("without a store the route should be absent (404), got %d", r.Code)
	}
}

func TestFavoriteUserIDFallbacks(t *testing.T) {
	mk := func(u *auth.User) *gin.Context {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodGet, "/", http.NoBody)
		if u != nil {
			c.Set(contextKeyUser, u)
		}
		return c
	}
	if got := favoriteUserID(mk(&auth.User{ID: "u1", Email: "e@x"})); got != "u1" {
		t.Errorf("ID present → use ID, got %q", got)
	}
	if got := favoriteUserID(mk(&auth.User{Email: "e@x"})); got != "e@x" {
		t.Errorf("only email → use email, got %q", got)
	}
	if got := favoriteUserID(mk(&auth.User{})); got != "anonymous" {
		t.Errorf("empty user → anonymous, got %q", got)
	}
	if got := favoriteUserID(mk(nil)); got != "anonymous" {
		t.Errorf("no user → anonymous, got %q", got)
	}
}
