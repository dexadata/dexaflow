package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/domain"
)

const suspensionPath = "/api/v2/service/tenants/acme/suspension"

// TestServiceSuspendTenantStoresHold: PUT with mode hold suspends the tenant,
// answers with the stored suspension, and is audited with the reason (#2181).
func TestServiceSuspendTenantStoresHold(t *testing.T) {
	// Arrange
	store := &fakeServiceStore{}
	audit := &fakeAuthAudit{}
	h := serviceServerWith(t, store, false, nil, audit)

	// Act
	rec := callService(h, http.MethodPut, suspensionPath, testServiceToken, `{"mode": "hold", "reason": "contract ended"}`)

	// Assert
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (%s)", rec.Code, rec.Body.String())
	}
	if store.gotSuspended != "acme" || store.gotSuspension.Mode != domain.SuspensionHold || store.gotSuspension.Reason != "contract ended" {
		t.Errorf("store got %q %+v, want acme hold with the reason", store.gotSuspended, store.gotSuspension)
	}
	var got suspensionResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if !got.Suspended || got.Mode != "hold" || got.Reason != "contract ended" || got.SuspendedAt == nil {
		t.Errorf("body = %+v, want the stored suspension", got)
	}
	if !audit.has(auditServiceTenantSuspend, "success") || audit.events[0].extra["reason"] != "contract ended" {
		t.Errorf("audit = %+v, want a %s success with the reason", audit.events, auditServiceTenantSuspend)
	}
}

// TestServiceSuspendTenantRefusesBadInput: a mode other than hold, a body that
// is not JSON, a bad tenant name and an unknown tenant never suspend anything.
func TestServiceSuspendTenantRefusesBadInput(t *testing.T) {
	for _, tc := range []struct {
		name, path, body string
		storeErr         error
		code             int
		phrase           string
		reachesStore     bool
	}{
		{name: "drain", path: suspensionPath, body: `{"mode": "drain"}`, code: http.StatusBadRequest, phrase: "not supported yet"},
		{name: "no mode", path: suspensionPath, body: `{}`, code: http.StatusBadRequest, phrase: `use \"hold\"`},
		{name: "long reason", path: suspensionPath, body: `{"mode": "hold", "reason": "` + strings.Repeat("x", domain.MaxSuspensionReasonLength+1) + `"}`, code: http.StatusBadRequest, phrase: "above the limit"},
		{name: "not json", path: suspensionPath, body: `hold`, code: http.StatusBadRequest, phrase: "body must be"},
		{name: "bad tenant", path: "/api/v2/service/tenants/Not_Valid/suspension", body: `{"mode": "hold"}`, code: http.StatusBadRequest, phrase: "lowercase"},
		{name: "unknown tenant", path: suspensionPath, body: `{"mode": "hold"}`, storeErr: domain.ErrNotFound, code: http.StatusNotFound, phrase: "no such tenant", reachesStore: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			store := &fakeServiceStore{suspendErr: tc.storeErr}
			h := serviceServer(t, store, false)

			// Act
			rec := callService(h, http.MethodPut, tc.path, testServiceToken, tc.body)

			// Assert
			if rec.Code != tc.code || !strings.Contains(rec.Body.String(), tc.phrase) {
				t.Errorf("status = %d body %s; want %d naming %q", rec.Code, rec.Body.String(), tc.code, tc.phrase)
			}
			if reached := store.gotSuspended != ""; reached != tc.reachesStore {
				t.Errorf("store reached = %v, want %v", reached, tc.reachesStore)
			}
		})
	}
}

// TestServiceReadAndResumeSuspension: GET shows the suspension, DELETE lifts it
// with a 204 and an audit event, and GET then shows none.
func TestServiceReadAndResumeSuspension(t *testing.T) {
	// Arrange
	store := &fakeServiceStore{suspension: domain.TenantSuspension{Mode: domain.SuspensionHold, Reason: "abuse"}}
	audit := &fakeAuthAudit{}
	h := serviceServerWith(t, store, false, nil, audit)

	// Act
	before := callService(h, http.MethodGet, suspensionPath, testServiceToken, "")
	resumed := callService(h, http.MethodDelete, suspensionPath, testServiceToken, "")
	after := callService(h, http.MethodGet, suspensionPath, testServiceToken, "")

	// Assert
	if before.Code != http.StatusOK || !strings.Contains(before.Body.String(), `"suspended":true`) || !strings.Contains(before.Body.String(), `"reason":"abuse"`) {
		t.Errorf("GET before = %d %s, want the suspension", before.Code, before.Body.String())
	}
	if resumed.Code != http.StatusNoContent || store.gotResumed != "acme" {
		t.Errorf("DELETE = %d (store got %q), want 204 for acme", resumed.Code, store.gotResumed)
	}
	if !audit.has(auditServiceTenantResume, "success") {
		t.Errorf("audit = %+v, want a %s success", audit.events, auditServiceTenantResume)
	}
	if after.Code != http.StatusOK || !strings.Contains(after.Body.String(), `"suspended":false`) {
		t.Errorf("GET after = %d %s, want no suspension", after.Code, after.Body.String())
	}
}

// TestServiceSuspensionRoutesNeedTheServiceToken: a user session or no token
// never reaches the store.
func TestServiceSuspensionRoutesNeedTheServiceToken(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodGet, http.MethodDelete} {
		t.Run(method, func(t *testing.T) {
			// Arrange
			store := &fakeServiceStore{}
			h := serviceServer(t, store, false)

			// Act
			rec := callService(h, method, suspensionPath, "a.b.c", `{"mode": "hold"}`)

			// Assert
			if rec.Code != http.StatusUnauthorized || store.gotSuspended != "" || store.gotResumed != "" {
				t.Errorf("status = %d, store suspended %q resumed %q; want 401 and no call", rec.Code, store.gotSuspended, store.gotResumed)
			}
		})
	}
}

// TestTenantSuspendedIsConflict: a run refused because the tenant is
// suspended is a 409 that carries the operator's reason.
func TestTenantSuspendedIsConflict(t *testing.T) {
	// Arrange
	err := domain.TenantSuspension{Mode: domain.SuspensionHold, Reason: "contract ended"}.RefusalError()
	gin.SetMode(gin.TestMode)
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(context.Background(), http.MethodPost, "/x", http.NoBody)

	// Act
	handleRepoError(c, err)

	// Assert
	if rec.Code != http.StatusConflict {
		t.Fatalf("status = %d, want 409 (%s)", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "the tenant is suspended: contract ended") {
		t.Errorf("body does not carry the reason: %s", rec.Body.String())
	}
}
