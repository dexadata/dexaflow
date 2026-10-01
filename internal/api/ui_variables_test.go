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

type fakeVariableStore struct {
	vars     map[string]domain.Variable
	setCalls []domain.Variable
}

func (f *fakeVariableStore) ListVariables(_ context.Context, _ string, _, _ int) ([]domain.Variable, int, error) {
	out := make([]domain.Variable, 0, len(f.vars))
	for _, v := range f.vars {
		out = append(out, v)
	}
	return out, len(out), nil
}

func (f *fakeVariableStore) GetVariable(_ context.Context, _, key string) (domain.Variable, error) {
	if v, ok := f.vars[key]; ok {
		return v, nil
	}
	return domain.Variable{}, ErrNotFound
}

// SetVariablePatch applies the tri-state patch to the in-memory store, mirroring
// the repository's COALESCE upsert: a nil field preserves the stored value, a
// non-nil field overwrites it (an empty string clears).
func (f *fakeVariableStore) SetVariablePatch(_ context.Context, _ string, p domain.VariablePatch) error {
	if f.vars == nil {
		f.vars = map[string]domain.Variable{}
	}
	cur := f.vars[p.Key]
	cur.Key = p.Key
	if p.Value != nil {
		cur.Value = *p.Value
	}
	if p.Description != nil {
		cur.Description = *p.Description
	}
	f.vars[p.Key] = cur
	f.setCalls = append(f.setCalls, cur)
	return nil
}

func (f *fakeVariableStore) DeleteVariable(_ context.Context, _, key string) error {
	if _, ok := f.vars[key]; !ok {
		return ErrNotFound
	}
	delete(f.vars, key)
	return nil
}

func variablesServer(store VariableStore) *gin.Engine {
	return NewServer(Dependencies{
		Logger:        discardLogger(),
		Authenticator: &fakeAuthn{user: &auth.User{ID: "u1", TenantID: "default", Roles: []string{"admin"}}},
		RateLimiter:   auth.NewRateLimiter(100, time.Minute),
		CORSOrigins:   []string{"*"},
		Variables:     store,
	})
}

func TestVariableCRUD(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{}}
	srv := variablesServer(store)

	// Create.
	rec := authGet(srv, http.MethodPost, "/api/v2/variables", `{"key":"region","value":"us-east","description":"primary"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	// List.
	rec = authGet(srv, http.MethodGet, "/api/v2/variables", "")
	var col variableCollectionDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &col); err != nil {
		t.Fatal(err)
	}
	if col.TotalEntries != 1 || col.Variables[0].Key != "region" || col.Variables[0].Value != "us-east" {
		t.Fatalf("unexpected list: %+v", col)
	}
	if col.Variables[0].IsEncrypted {
		t.Errorf("is_encrypted should be false")
	}
	// Get.
	if r := authGet(srv, http.MethodGet, "/api/v2/variables/region", ""); r.Code != http.StatusOK {
		t.Errorf("get = %d", r.Code)
	}
	// Update.
	rec = authGet(srv, http.MethodPatch, "/api/v2/variables/region", `{"value":"eu-west"}`)
	if rec.Code != http.StatusOK || store.vars["region"].Value != "eu-west" {
		t.Errorf("update = %d, value=%q", rec.Code, store.vars["region"].Value)
	}
	// Delete.
	if r := authGet(srv, http.MethodDelete, "/api/v2/variables/region", ""); r.Code != http.StatusNoContent {
		t.Errorf("delete = %d", r.Code)
	}
	// Missing get/update/delete -> 404.
	if r := authGet(srv, http.MethodGet, "/api/v2/variables/region", ""); r.Code != http.StatusNotFound {
		t.Errorf("get missing = %d, want 404", r.Code)
	}
	if r := authGet(srv, http.MethodDelete, "/api/v2/variables/region", ""); r.Code != http.StatusNotFound {
		t.Errorf("delete missing = %d, want 404", r.Code)
	}
}

func TestVariableSecretMasking(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{
		"db_password": {Key: "db_password", Value: "s3cr3t"},
		"region":      {Key: "region", Value: "us-east"},
	}}
	srv := variablesServer(store)

	// Sensitive key is masked on get.
	rec := authGet(srv, http.MethodGet, "/api/v2/variables/db_password", "")
	var v variableDTO
	if err := json.Unmarshal(rec.Body.Bytes(), &v); err != nil {
		t.Fatal(err)
	}
	if v.Value != "***" {
		t.Errorf("secret value should be masked, got %q", v.Value)
	}
	// Non-sensitive key is shown.
	rec = authGet(srv, http.MethodGet, "/api/v2/variables/region", "")
	_ = json.Unmarshal(rec.Body.Bytes(), &v)
	if v.Value != "us-east" {
		t.Errorf("plain value should not be masked, got %q", v.Value)
	}
}

// TestVariableUpdateTriState locks the #887 write semantics on the variable
// PATCH path: a masked value ("***") for a sensitive key is treated as unchanged
// (the #874 rule for variables), an omitted value preserves, and an explicit ""
// clears.
func TestVariableUpdateTriState(t *testing.T) {
	newStore := func() *fakeVariableStore {
		return &fakeVariableStore{vars: map[string]domain.Variable{
			"api_token": {Key: "api_token", Value: "tok-REAL", Description: "prod"},
			"region":    {Key: "region", Value: "us-east"},
		}}
	}

	t.Run("masked sensitive value preserves the real value", func(t *testing.T) {
		store := newStore()
		srv := variablesServer(store)
		// The UI GET'd api_token (value shown as ***) and re-submitted it verbatim.
		rec := authGet(srv, http.MethodPatch, "/api/v2/variables/api_token", `{"value":"***"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.vars["api_token"].Value; got != "tok-REAL" {
			t.Errorf("masked value overwrote the real secret: %q", got)
		}
	})

	t.Run("masked value for a NON-sensitive key is a literal set", func(t *testing.T) {
		store := newStore()
		srv := variablesServer(store)
		// "region" is not sensitive, so "***" is a real value the user chose.
		rec := authGet(srv, http.MethodPatch, "/api/v2/variables/region", `{"value":"***"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.vars["region"].Value; got != "***" {
			t.Errorf("a non-sensitive key must set the literal value: %q", got)
		}
	})

	t.Run("omitted value preserves, description updates", func(t *testing.T) {
		store := newStore()
		srv := variablesServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/variables/api_token", `{"description":"staging"}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		got := store.vars["api_token"]
		if got.Value != "tok-REAL" {
			t.Errorf("omitted value was clobbered: %q", got.Value)
		}
		if got.Description != "staging" {
			t.Errorf("description not updated: %q", got.Description)
		}
	})

	t.Run("explicit empty value clears", func(t *testing.T) {
		store := newStore()
		srv := variablesServer(store)
		rec := authGet(srv, http.MethodPatch, "/api/v2/variables/region", `{"value":""}`)
		if rec.Code != http.StatusOK {
			t.Fatalf("patch = %d (%s)", rec.Code, rec.Body.String())
		}
		if got := store.vars["region"].Value; got != "" {
			t.Errorf("explicit empty value did not clear: %q", got)
		}
	})
}

// TestVariableCreateMaskedFailsClosed: creating a sensitive-keyed variable whose
// value is the mask has nothing to preserve, so it stores an empty value rather
// than persisting a literal "***" as the real secret.
func TestVariableCreateMaskedFailsClosed(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{}}
	srv := variablesServer(store)
	rec := authGet(srv, http.MethodPost, "/api/v2/variables", `{"key":"api_token","value":"***"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := store.vars["api_token"].Value; got != "" {
		t.Errorf("masked create must not persist the literal mask: %q", got)
	}
}

// TestVariableUpsertPOSTMaskedRoundTrip locks that the POST upsert path (what
// `leoflow variables set` uses) preserves an existing sensitive value when the
// masked value is written back, rather than treating it as a fresh create and
// clearing it.
func TestVariableUpsertPOSTMaskedRoundTrip(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{
		"api_token": {Key: "api_token", Value: "tok-REAL"},
	}}
	srv := variablesServer(store)
	rec := authGet(srv, http.MethodPost, "/api/v2/variables", `{"key":"api_token","value":"***"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("post = %d (%s)", rec.Code, rec.Body.String())
	}
	if got := store.vars["api_token"].Value; got != "tok-REAL" {
		t.Errorf("POST upsert overwrote the real value with the mask: %q", got)
	}
}

func TestVariablesEmptyStubWithoutStore(t *testing.T) {
	rec := authGet(variablesServer(nil), http.MethodGet, "/api/v2/variables", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("nil store = %d", rec.Code)
	}
	var col map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &col)
	if col["total_entries"].(float64) != 0 {
		t.Errorf("nil store should yield empty collection, got %v", col)
	}
}
