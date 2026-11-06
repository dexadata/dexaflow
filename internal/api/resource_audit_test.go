package api

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/dexadata/dexaflow/internal/auth"
	"github.com/dexadata/dexaflow/internal/domain"
)

// resourceEvent is one RecordResourceAudit call.
type resourceEvent struct {
	tenant, userID, action, resourceType, resourceID string
	metadata                                         map[string]string
}

// fakeResourceAudit records the admin writes of #2228 next to the DAG and run
// entries fakeAuditWriter already records.
type fakeResourceAudit struct {
	fakeAuditWriter
	events []resourceEvent
}

func (f *fakeResourceAudit) RecordResourceAudit(_ context.Context, tenant, userID, action, resourceType, resourceID string, metadata map[string]string) error {
	f.events = append(f.events, resourceEvent{tenant, userID, action, resourceType, resourceID, metadata})
	return f.err
}

// last returns the newest resource event, failing the test when there is none.
func (f *fakeResourceAudit) last(t *testing.T) resourceEvent {
	t.Helper()
	if len(f.events) == 0 {
		t.Fatal("no resource audit entry was recorded")
	}
	return f.events[len(f.events)-1]
}

func resourceAuditServer(audit *fakeResourceAudit, vars *fakeVariableStore, conns *fakeConnStore, pools *fakePoolStore, dags *fakeDagRepo, runs *fakeRunRepo) *gin.Engine {
	return newTestServer(Dependencies{
		Server: ServerDeps{Pools: true},
		Auth: AuthDeps{
			Authenticator: &fakeAuthn{user: &auth.User{ID: "admin-1", TenantID: "acme", Roles: []string{"admin"}}},
			RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
			TokenTTLSecs:  900,
		},
		Stores: StoreDeps{
			Variables:   vars,
			Connections: conns,
			Pools:       pools,
			Dags:        dags,
			DagRuns:     runs,
			Audit:       audit,
		},
	})
}

// raExpect sends one request and fails the test unless it answers want.
func raExpect(t *testing.T, srv *gin.Engine, method, path, body string, want int) {
	t.Helper()
	if rec := authGet(srv, method, path, body); rec.Code != want {
		t.Fatalf("%s %s = %d, want %d (%s)", method, path, rec.Code, want, rec.Body.String())
	}
}

func wantEvent(t *testing.T, got resourceEvent, action, resourceType, resourceID string) {
	t.Helper()
	if got.action != action || got.resourceType != resourceType || got.resourceID != resourceID {
		t.Errorf("audit entry = %s %s/%s, want %s %s/%s", got.action, got.resourceType, got.resourceID, action, resourceType, resourceID)
	}
	if got.tenant != "acme" || got.userID != "admin-1" {
		t.Errorf("audit entry actor = %s/%s, want acme/admin-1", got.tenant, got.userID)
	}
}

// noSecret fails when any metadata value of any entry carries secret.
func noSecret(t *testing.T, events []resourceEvent, secret string) {
	t.Helper()
	for _, e := range events {
		for k, v := range e.metadata {
			if strings.Contains(v, secret) {
				t.Errorf("%s metadata %s = %q carries a secret value", e.action, k, v)
			}
		}
	}
}

// TestVariableWritesAreAudited: creating, changing and deleting a variable
// each record an entry naming the key and the fields set, never the value.
func TestVariableWritesAreAudited(t *testing.T) {
	audit := &fakeResourceAudit{}
	srv := resourceAuditServer(audit, &fakeVariableStore{}, nil, nil, nil, nil)

	raExpect(t, srv, http.MethodPost, "/api/v2/variables", `{"key":"api_key","value":"s3cr3t-v1"}`, http.StatusCreated)
	got := audit.last(t)
	wantEvent(t, got, "variable.create", "variable", "api_key")
	if got.metadata["fields"] != "value" {
		t.Errorf("create fields = %q, want value", got.metadata["fields"])
	}

	raExpect(t, srv, http.MethodPost, "/api/v2/variables", `{"key":"api_key","value":"s3cr3t-v2"}`, http.StatusCreated)
	wantEvent(t, audit.last(t), "variable.update", "variable", "api_key")

	raExpect(t, srv, http.MethodPatch, "/api/v2/variables/api_key", `{"value":"s3cr3t-v3","description":"rotated"}`, http.StatusOK)
	got = audit.last(t)
	wantEvent(t, got, "variable.update", "variable", "api_key")
	if got.metadata["fields"] != "description,value" {
		t.Errorf("patch fields = %q, want description,value", got.metadata["fields"])
	}

	raExpect(t, srv, http.MethodDelete, "/api/v2/variables/api_key", "", http.StatusNoContent)
	wantEvent(t, audit.last(t), "variable.delete", "variable", "api_key")

	before := len(audit.events)
	raExpect(t, srv, http.MethodDelete, "/api/v2/variables/api_key", "", http.StatusNotFound)
	if len(audit.events) != before {
		t.Errorf("a refused delete recorded %+v", audit.events[before:])
	}
	if len(audit.events) != 4 {
		t.Errorf("recorded %d entries, want 4", len(audit.events))
	}
	noSecret(t, audit.events, "s3cr3t")
}

// TestConnectionWritesAreAudited: the entry names the fields set, the
// password included by name, and never carries the password or extra.
func TestConnectionWritesAreAudited(t *testing.T) {
	audit := &fakeResourceAudit{}
	srv := resourceAuditServer(audit, nil, &fakeConnStore{}, nil, nil, nil)

	raExpect(t, srv, http.MethodPost, "/api/v2/connections",
		`{"connection_id":"wh","conn_type":"postgres","host":"db","password":"pw-s3cr3t","extra":"{\"token\":\"x-s3cr3t\"}"}`, http.StatusCreated) // gitleaks:allow: test fixture, not a credential
	got := audit.last(t)
	wantEvent(t, got, "connection.create", "connection", "wh")
	if got.metadata["fields"] != "conn_type,extra,host,password" {
		t.Errorf("create fields = %q, want conn_type,extra,host,password", got.metadata["fields"])
	}

	raExpect(t, srv, http.MethodPatch, "/api/v2/connections/wh", `{"conn_type":"postgres","password":"pw2-s3cr3t"}`, http.StatusOK) // gitleaks:allow: test fixture, not a credential
	got = audit.last(t)
	wantEvent(t, got, "connection.update", "connection", "wh")
	if got.metadata["fields"] != "conn_type,password" {
		t.Errorf("patch fields = %q, want conn_type,password", got.metadata["fields"])
	}

	raExpect(t, srv, http.MethodDelete, "/api/v2/connections/wh", "", http.StatusNoContent)
	wantEvent(t, audit.last(t), "connection.delete", "connection", "wh")
	noSecret(t, audit.events, "s3cr3t")
}

// TestPoolWritesAreAudited: a resize records the slots before and after.
func TestPoolWritesAreAudited(t *testing.T) {
	audit := &fakeResourceAudit{}
	srv := resourceAuditServer(audit, nil, nil, &fakePoolStore{pools: map[string]domain.Pool{}}, nil, nil)

	raExpect(t, srv, http.MethodPost, "/api/v2/pools", `{"name":"gpu","slots":2}`, http.StatusCreated)
	got := audit.last(t)
	wantEvent(t, got, "pool.create", "pool", "gpu")
	if got.metadata["slots"] != "2" {
		t.Errorf("create slots = %q, want 2", got.metadata["slots"])
	}

	raExpect(t, srv, http.MethodPatch, "/api/v2/pools/gpu", `{"slots":5,"description":"more"}`, http.StatusOK)
	got = audit.last(t)
	wantEvent(t, got, "pool.update", "pool", "gpu")
	if got.metadata["slots"] != "5" || got.metadata["previous_slots"] != "2" || got.metadata["fields"] != "description,slots" {
		t.Errorf("resize metadata = %v, want slots 5, previous_slots 2, fields description,slots", got.metadata)
	}

	raExpect(t, srv, http.MethodDelete, "/api/v2/pools/gpu", "", http.StatusNoContent)
	wantEvent(t, audit.last(t), "pool.delete", "pool", "gpu")
}

// TestDagAndRunDeletesAreAudited: deleting a DAG, clearing its history and
// deleting one run each record an entry on the DAG.
func TestDagAndRunDeletesAreAudited(t *testing.T) {
	audit := &fakeResourceAudit{}
	dags := &fakeDagRepo{dags: []domain.DAG{{DagID: "etl"}, {DagID: "old"}}}
	runs := &fakeRunRepo{runs: []domain.DagRun{{DagID: "etl", RunID: "r1"}}}
	srv := resourceAuditServer(audit, nil, nil, nil, dags, runs)

	raExpect(t, srv, http.MethodDelete, "/api/v2/dags/etl/dagRuns/r1", "", http.StatusNoContent)
	raExpect(t, srv, http.MethodDelete, "/api/v2/dags/etl", "", http.StatusNoContent)
	raExpect(t, srv, http.MethodDelete, "/api/v2/dags/old?deregister=true", "", http.StatusNoContent)

	want := []recordedAudit{
		{tenant: "acme", userID: "admin-1", action: "dagrun.delete", dagID: "etl", runID: "r1"},
		{tenant: "acme", userID: "admin-1", action: "dag.history.clear", dagID: "etl"},
		{tenant: "acme", userID: "admin-1", action: "dag.delete", dagID: "old"},
	}
	if len(audit.calls) != len(want) {
		t.Fatalf("recorded %+v, want %+v", audit.calls, want)
	}
	for i, w := range want {
		g := audit.calls[i]
		if g.tenant != w.tenant || g.userID != w.userID || g.action != w.action || g.dagID != w.dagID || g.runID != w.runID {
			t.Errorf("entry %d = %+v, want %+v", i, g, w)
		}
	}
}

// TestPasswordSignInIsAuditedWithoutOIDC: with OIDC off, a password sign-in
// records auth.login with its outcome, and the password never reaches it.
func TestPasswordSignInIsAuditedWithoutOIDC(t *testing.T) {
	audit := &fakeAuthAudit{}
	authn := &fakeAuthn{token: "tok"}
	srv := newTestServer(Dependencies{
		Auth: AuthDeps{
			Authenticator: authn,
			RateLimiter:   auth.NewRateLimiter(1000, time.Minute),
			TokenTTLSecs:  900,
			AuthAudit:     audit,
		},
	})
	raExpect(t, srv, http.MethodPost, "/auth/token", `{"username":"ana@example.com","password":"pw-s3cr3t","tenant":"acme"}`, http.StatusOK) // gitleaks:allow: test fixture, not a credential
	authn.issueErr = auth.ErrInvalidCredentials
	raExpect(t, srv, http.MethodPost, "/auth/token", `{"username":"ana@example.com","password":"wrong-s3cr3t","tenant":"acme"}`, http.StatusUnauthorized) // gitleaks:allow: test fixture, not a credential

	if len(audit.events) != 2 {
		t.Fatalf("recorded %+v, want two auth.login entries", audit.events)
	}
	for i, outcome := range []string{"success", "bad_credentials"} {
		e := audit.events[i]
		if e.action != "auth.login" || e.outcome != outcome || e.email != "ana@example.com" || e.tenant != "acme" {
			t.Errorf("entry %d = %+v, want auth.login %s for ana@example.com in acme", i, e, outcome)
		}
		for k, v := range e.extra {
			if strings.Contains(v, "s3cr3t") {
				t.Errorf("entry %d extra %s carries the password", i, k)
			}
		}
	}
}
