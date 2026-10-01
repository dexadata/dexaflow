package api

import (
	"net/http"
	"testing"

	"github.com/dexadata/dexaflow/internal/domain"
)

func TestCreateVariableRejectsBadBody(t *testing.T) {
	srv := variablesServer(&fakeVariableStore{vars: map[string]domain.Variable{}})
	if r := authGet(srv, http.MethodPost, "/api/v2/variables", `{not valid json`); r.Code != http.StatusBadRequest {
		t.Errorf("malformed body should be 400, got %d", r.Code)
	}
}

func TestCreateVariableRequiresKey(t *testing.T) {
	srv := variablesServer(&fakeVariableStore{vars: map[string]domain.Variable{}})
	if r := authGet(srv, http.MethodPost, "/api/v2/variables", `{"value":"x"}`); r.Code != http.StatusBadRequest {
		t.Errorf("missing key should be 400, got %d", r.Code)
	}
}

func TestUpdateVariableMissingIs404(t *testing.T) {
	srv := variablesServer(&fakeVariableStore{vars: map[string]domain.Variable{}})
	if r := authGet(srv, http.MethodPatch, "/api/v2/variables/ghost", `{"value":"x"}`); r.Code != http.StatusNotFound {
		t.Errorf("updating an absent variable should be 404, got %d", r.Code)
	}
}

func TestUpdateVariableRejectsBadBody(t *testing.T) {
	store := &fakeVariableStore{vars: map[string]domain.Variable{"region": {Key: "region", Value: "us"}}}
	srv := variablesServer(store)
	if r := authGet(srv, http.MethodPatch, "/api/v2/variables/region", `{bad`); r.Code != http.StatusBadRequest {
		t.Errorf("malformed update body should be 400, got %d", r.Code)
	}
}
