package api

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

type fakePoolStore struct {
	pools map[string]domain.Pool
	usage map[string]domain.PoolUsage
}

func (f *fakePoolStore) ListPools(_ context.Context, _ string, _, _ int) ([]domain.Pool, int, error) {
	out := make([]domain.Pool, 0, len(f.pools))
	for _, p := range f.pools {
		out = append(out, p)
	}
	return out, len(out), nil
}

func (f *fakePoolStore) GetPool(_ context.Context, _, name string) (domain.Pool, error) {
	if p, ok := f.pools[name]; ok {
		return p, nil
	}
	return domain.Pool{}, ErrNotFound
}

func (f *fakePoolStore) SetPool(_ context.Context, _ string, p domain.Pool) error {
	if f.pools == nil {
		f.pools = map[string]domain.Pool{}
	}
	f.pools[p.Name] = p
	return nil
}

func (f *fakePoolStore) DeletePool(_ context.Context, _, name string) error {
	p, ok := f.pools[name]
	if !ok {
		return domain.ErrNotFound
	}
	if p.IsDefault {
		return domain.ErrConflict
	}
	delete(f.pools, name)
	return nil
}

func (f *fakePoolStore) PoolSlotUsage(_ context.Context, _ string) (map[string]domain.PoolUsage, error) {
	return f.usage, nil
}

func poolServer(store PoolStore, edition string, roles ...string) *gin.Engine {
	if len(roles) == 0 {
		roles = []string{"admin"}
	}
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: roles}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Pools:         store,
		Edition:       edition,
	})
}

// TestPoolCRUDLifecycle exercises create → get → patch → delete against the
// Pro-gated endpoint (ADR 0053 Stage 3).
func TestPoolCRUDLifecycle(t *testing.T) {
	store := &fakePoolStore{pools: map[string]domain.Pool{}}
	srv := poolServer(store, "pro")

	rec := authGet(srv, http.MethodPost, "/api/v2/pools", `{"name":"etl","slots":5,"description":"batch"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	if store.pools["etl"].Slots != 5 {
		t.Errorf("store slots = %d, want 5", store.pools["etl"].Slots)
	}

	rec = authGet(srv, http.MethodGet, "/api/v2/pools/etl", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get = %d", rec.Code)
	}
	var dto poolDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &dto); err != nil {
		t.Fatal(err)
	}
	if dto.Name != "etl" || dto.Slots != 5 {
		t.Errorf("unexpected pool dto: %+v", dto)
	}

	rec = authGet(srv, http.MethodPatch, "/api/v2/pools/etl", `{"slots":9}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
	}
	if store.pools["etl"].Slots != 9 {
		t.Errorf("after patch slots = %d, want 9", store.pools["etl"].Slots)
	}

	if rec := authGet(srv, http.MethodDelete, "/api/v2/pools/etl", ""); rec.Code != http.StatusNoContent {
		t.Errorf("delete = %d", rec.Code)
	}
	if rec := authGet(srv, http.MethodGet, "/api/v2/pools/etl", ""); rec.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", rec.Code)
	}
}

// TestPoolResponseAirflowSlotAccounting: the collection reports occupied/open
// slots the Airflow UI renders — occupied = running+queued, open = slots−occupied.
func TestPoolResponseAirflowSlotAccounting(t *testing.T) {
	store := &fakePoolStore{
		pools: map[string]domain.Pool{"default_pool": {Name: "default_pool", Slots: 4, IsDefault: true}},
		usage: map[string]domain.PoolUsage{"default_pool": {Running: 2, Queued: 1, Scheduled: 3}},
	}
	rec := authGet(poolServer(store, "pro"), http.MethodGet, "/api/v2/pools", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("list = %d", rec.Code)
	}
	var col poolCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if len(col.Pools) != 1 {
		t.Fatalf("got %d pools, want 1", len(col.Pools))
	}
	p := col.Pools[0]
	if p.OccupiedSlots != 3 || p.OpenSlots != 1 || p.RunningSlots != 2 || p.QueuedSlots != 1 || p.ScheduledSlots != 3 {
		t.Errorf("slot accounting wrong: %+v", p)
	}
}

// TestPoolMutationsAreAdminGated: a viewer (no write:pool) cannot create, update,
// or delete pools — the same admin gate connections and variables use.
func TestPoolMutationsAreAdminGated(t *testing.T) {
	store := &fakePoolStore{pools: map[string]domain.Pool{"etl": {Name: "etl", Slots: 1}}}
	srv := poolServer(store, "pro", "viewer")
	cases := []struct{ method, path, body string }{
		{http.MethodPost, "/api/v2/pools", `{"name":"x","slots":1}`},
		{http.MethodPatch, "/api/v2/pools/etl", `{"slots":2}`},
		{http.MethodDelete, "/api/v2/pools/etl", ""},
	}
	for _, tc := range cases {
		if rec := authGet(srv, tc.method, tc.path, tc.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s = %d, want 403 (admin-gated)", tc.method, tc.path, rec.Code)
		}
	}
}

// TestPoolsLiteServesEmptyStub is the Lite-safety lock at the API surface: on a
// non-Pro edition the real CRUD is NOT mounted — GET returns the graceful empty
// collection (so the Pools screen renders) and a write is not routed.
func TestPoolsLiteServesEmptyStub(t *testing.T) {
	store := &fakePoolStore{pools: map[string]domain.Pool{"etl": {Name: "etl", Slots: 1}}}
	srv := poolServer(store, "lite")

	rec := authGet(srv, http.MethodGet, "/api/v2/pools", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("lite list = %d", rec.Code)
	}
	var col poolCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if len(col.Pools) != 0 || col.TotalEntries != 0 {
		t.Errorf("lite pools must be an empty stub, got %+v", col)
	}
	// The real create route is not mounted on Lite.
	if rec := authGet(srv, http.MethodPost, "/api/v2/pools", `{"name":"x","slots":1}`); rec.Code == http.StatusCreated {
		t.Errorf("lite POST /pools must not create (got %d)", rec.Code)
	}
}

// TestPoolDeleteDefaultRejected: the implicit default pool cannot be deleted
// (Airflow parity) — the store's conflict surfaces as 409.
func TestPoolDeleteDefaultRejected(t *testing.T) {
	store := &fakePoolStore{pools: map[string]domain.Pool{
		"default_pool": {Name: "default_pool", Slots: 128, IsDefault: true},
	}}
	rec := authGet(poolServer(store, "pro"), http.MethodDelete, "/api/v2/pools/default_pool", "")
	if rec.Code != http.StatusConflict {
		t.Errorf("delete default_pool = %d, want 409", rec.Code)
	}
}

// TestPoolCreateRequiresNameAndSlots: a create missing required fields is a 400.
func TestPoolCreateRequiresNameAndSlots(t *testing.T) {
	srv := poolServer(&fakePoolStore{pools: map[string]domain.Pool{}}, "pro")
	if rec := authGet(srv, http.MethodPost, "/api/v2/pools", `{"description":"no name or slots"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("create without name/slots = %d, want 400", rec.Code)
	}
}
