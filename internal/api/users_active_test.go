package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// fakeActivatorStore is a UserStore that can also activate and deactivate, and
// records each call.
type fakeActivatorStore struct {
	fakeUserStore
	calls []activeCall
	err   error
}

type activeCall struct {
	tenant, actor, user string
	active              bool
}

func (f *fakeActivatorStore) SetUserActive(_ context.Context, tenant, actorUserID, userID string, active bool) (domain.User, error) {
	f.calls = append(f.calls, activeCall{tenant, actorUserID, userID, active})
	if f.err != nil {
		return domain.User{}, f.err
	}
	return domain.User{ID: userID, Email: "bob@example.com", Roles: []string{"viewer"}, IsActive: active}, nil
}

func TestPatchUserDeactivatesInTheCallersTenant(t *testing.T) {
	store := &fakeActivatorStore{}
	rec := authGet(userServer(store, adminUser()), http.MethodPatch, "/api/v2/users/u-2", `{"is_active":false}`)

	if rec.Code != http.StatusOK {
		t.Fatalf("deactivate = %d (%s)", rec.Code, rec.Body.String())
	}
	want := activeCall{tenant: "default", actor: "admin-1", user: "u-2", active: false}
	if len(store.calls) != 1 || store.calls[0] != want {
		t.Errorf("store calls = %+v, want [%+v]", store.calls, want)
	}
	if !strings.Contains(rec.Body.String(), `"is_active":false`) {
		t.Errorf("response does not show the account inactive: %s", rec.Body.String())
	}
}

func TestPatchUserReactivates(t *testing.T) {
	store := &fakeActivatorStore{}
	rec := authGet(userServer(store, adminUser()), http.MethodPatch, "/api/v2/users/u-2", `{"is_active":true}`)

	if rec.Code != http.StatusOK || len(store.calls) != 1 || !store.calls[0].active {
		t.Fatalf("reactivate = %d, calls %+v (%s)", rec.Code, store.calls, rec.Body.String())
	}
}

func TestPatchUserRefusesToDeactivateTheCaller(t *testing.T) {
	store := &fakeActivatorStore{}
	rec := authGet(userServer(store, adminUser()), http.MethodPatch, "/api/v2/users/admin-1", `{"is_active":false}`)

	if rec.Code != http.StatusConflict {
		t.Errorf("self deactivation = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.calls) != 0 {
		t.Errorf("the store was called: %+v", store.calls)
	}
}

func TestPatchUserRefusesOtherFieldsAndEmptyBodies(t *testing.T) {
	for name, body := range map[string]string{
		"roles":           `{"is_active":true,"roles":["admin"]}`,
		"password":        `{"password":"new-password"}`,
		"no is_active":    `{}`,
		"not a boolean":   `{"is_active":"false"}`,
		"two objects":     `{"is_active":true}{"is_active":false}`,
		"not json at all": `deactivate`,
	} {
		t.Run(name, func(t *testing.T) {
			store := &fakeActivatorStore{}
			rec := authGet(userServer(store, adminUser()), http.MethodPatch, "/api/v2/users/u-2", body)
			if rec.Code != http.StatusBadRequest {
				t.Errorf("PATCH %s = %d, want 400 (%s)", body, rec.Code, rec.Body.String())
			}
			if len(store.calls) != 0 {
				t.Errorf("the store was called: %+v", store.calls)
			}
		})
	}
}

func TestPatchUserUnknownUserIsNotFound(t *testing.T) {
	store := &fakeActivatorStore{err: domain.Safef(domain.ErrNotFound, "user %q not found", "u-9")}
	rec := authGet(userServer(store, adminUser()), http.MethodPatch, "/api/v2/users/u-9", `{"is_active":false}`)

	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown user = %d, want 404 (%s)", rec.Code, rec.Body.String())
	}
}

func TestPatchUserNeedsWriteUser(t *testing.T) {
	store := &fakeActivatorStore{}
	viewer := &auth.User{ID: "v-1", TenantID: "default", Roles: []string{"viewer"}, Permissions: []auth.Permission{{Action: "read", Resource: "user"}}}
	rec := authGet(userServer(store, viewer), http.MethodPatch, "/api/v2/users/u-2", `{"is_active":false}`)

	if rec.Code != http.StatusForbidden {
		t.Errorf("viewer PATCH = %d, want 403 (%s)", rec.Code, rec.Body.String())
	}
	if len(store.calls) != 0 {
		t.Errorf("the store was called: %+v", store.calls)
	}
}

func TestPatchUserIsNotMountedWithoutAnActivator(t *testing.T) {
	rec := authGet(userServer(&fakeUserStore{}, adminUser()), http.MethodPatch, "/api/v2/users/u-2", `{"is_active":false}`)

	if rec.Code == http.StatusOK {
		t.Errorf("PATCH answered 200 with a store that cannot deactivate (%s)", rec.Body.String())
	}
}
