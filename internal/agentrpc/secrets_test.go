package agentrpc

import (
	"context"
	"testing"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

type fakeSecrets struct {
	vars        map[string]string
	conns       map[string]string
	gotVarTn    string
	gotVarNames []string // the declared set the scoped variables path received
	scopedVar   bool     // whether the scoped variables path was taken
	scopedConn  bool     // whether the scoped connections path was taken
}

func (f *fakeSecrets) SecretVariables(_ context.Context, tenant string) (map[string]string, error) {
	f.gotVarTn = tenant
	return f.vars, nil
}
func (f *fakeSecrets) SecretConnectionURIs(_ context.Context, _ string) (map[string]string, error) {
	return f.conns, nil
}
func (f *fakeSecrets) SecretVariablesScoped(_ context.Context, tenant string, names []string) (map[string]string, error) {
	f.gotVarTn = tenant
	f.gotVarNames = names
	f.scopedVar = true
	return fakeSubset(f.vars, names), nil
}
func (f *fakeSecrets) SecretConnectionURIsScoped(_ context.Context, _ string, names []string) (map[string]string, error) {
	f.scopedConn = true
	return fakeSubset(f.conns, names), nil
}

// fakeSubset mirrors the real store's scoped query: it returns only the named
// subset of the tenant set (an empty name set returns nothing), so handler-level
// scoping is exercised exactly as the SQL would filter it.
func fakeSubset(all map[string]string, names []string) map[string]string {
	out := map[string]string{}
	for _, n := range names {
		if v, ok := all[n]; ok {
			out[n] = v
		}
	}
	return out
}

func TestGetVariablesAndConnections(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	sec := &fakeSecrets{
		vars:  map[string]string{"FOO": "bar"},
		conns: map[string]string{"pg": "postgres://u:p@h:5432/db"},
	}
	srv.SetSecrets(sec, true) // dev: allow over the insecure test channel
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if vresp.Variables["FOO"] != "bar" {
		t.Errorf("variables = %v", vresp.Variables)
	}
	if sec.gotVarTn != "acme" { // scoped to the token's tenant
		t.Errorf("tenant = %q, want acme", sec.gotVarTn)
	}
	cresp, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if cresp.ConnectionUris["pg"] != "postgres://u:p@h:5432/db" {
		t.Errorf("connections = %v", cresp.ConnectionUris)
	}
}

func TestSecretsFailClosedOnInsecureChannel(t *testing.T) {
	srv, a := newServer(&fakeStore{})
	srv.SetSecrets(&fakeSecrets{vars: map[string]string{"X": "1"}}, false) // require TLS
	ctx := ctxWithToken(t, a)                                              // no TLS peer in the test context

	if _, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("GetVariables over insecure channel = %v, want PermissionDenied", err)
	}
	if _, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); status.Code(err) != codes.PermissionDenied {
		t.Errorf("GetConnections over insecure channel = %v, want PermissionDenied", err)
	}
}

func TestSecretsRejectMissingToken(t *testing.T) {
	srv, _ := newServer(&fakeStore{})
	srv.SetSecrets(&fakeSecrets{}, true)
	if _, err := srv.GetVariables(context.Background(), &agentv1.GetVariablesRequest{}); status.Code(err) != codes.Unauthenticated {
		t.Errorf("missing token = %v, want Unauthenticated", err)
	}
}

// scopeWarnEvent captures one RecordSecretScopeWarning call so a test can assert
// what the warn phase recorded — counts only, never secret names or values.
type scopeWarnEvent struct {
	tenant, dagID, runID, taskID, kind string
	declared, total                    int
}

type fakeScopeAuditor struct {
	events []scopeWarnEvent
	err    error
}

func (f *fakeScopeAuditor) RecordSecretScopeWarning(_ context.Context, tenant, dagID, runID, taskID, kind string, declared, total int) error {
	f.events = append(f.events, scopeWarnEvent{tenant, dagID, runID, taskID, kind, declared, total})
	return f.err
}

// TestSecretsWarnOnNarrowDeclaration is the core of the warn phase: a task that
// declared a strict subset of the tenant vault STILL receives the full set
// (delivery is unchanged), and the narrowing is recorded as a scope-warning
// audit event carrying counts only.
func TestSecretsWarnOnNarrowDeclaration(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{
		DeclaredVariables:   []string{"FOO"},
		DeclaredConnections: []string{"pg"},
	}}
	srv, a := newServer(store)
	sec := &fakeSecrets{
		vars:  map[string]string{"FOO": "bar", "BAR": "baz"},
		conns: map[string]string{"pg": "postgres://u:p@h:5432/db", "redis": "redis://h:6379"},
	}
	audit := &fakeScopeAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetSecretScopeAuditor(audit)
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	// Delivery unchanged: the full tenant set is still returned despite the narrow
	// declaration.
	if len(vresp.Variables) != 2 || vresp.Variables["FOO"] != "bar" || vresp.Variables["BAR"] != "baz" {
		t.Errorf("variables = %v, want the full set {FOO,BAR}", vresp.Variables)
	}
	cresp, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if len(cresp.ConnectionUris) != 2 {
		t.Errorf("connections = %v, want the full set {pg,redis}", cresp.ConnectionUris)
	}

	if len(audit.events) != 2 {
		t.Fatalf("recorded %d scope-warning events, want 2 (one per kind): %+v", len(audit.events), audit.events)
	}
	byKind := map[string]scopeWarnEvent{}
	for _, e := range audit.events {
		byKind[e.kind] = e
	}
	v, ok := byKind["variables"]
	if !ok {
		t.Fatalf("no variables scope-warning event: %+v", audit.events)
	}
	if v.declared != 1 || v.total != 2 {
		t.Errorf("variables event declared/total = %d/%d, want 1/2", v.declared, v.total)
	}
	if v.tenant != "acme" || v.dagID != "etl" || v.runID != "run-1" || v.taskID != "extract" {
		t.Errorf("variables event identity = %+v, want tenant=acme dag=etl run=run-1 task=extract", v)
	}
	c, ok := byKind["connections"]
	if !ok {
		t.Fatalf("no connections scope-warning event: %+v", audit.events)
	}
	if c.declared != 1 || c.total != 2 {
		t.Errorf("connections event declared/total = %d/%d, want 1/2", c.declared, c.total)
	}
}

// TestSecretsNoWarnOnEmptyDeclaration is the permissive default: a task that
// declared nothing triggers no warn and receives the full set, exactly as before
// the warn phase existed.
func TestSecretsNoWarnOnEmptyDeclaration(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{}} // no declaration
	srv, a := newServer(store)
	sec := &fakeSecrets{
		vars:  map[string]string{"FOO": "bar", "BAR": "baz"},
		conns: map[string]string{"pg": "postgres://u:p@h:5432/db"},
	}
	audit := &fakeScopeAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetSecretScopeAuditor(audit)
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 2 {
		t.Errorf("variables = %v, want the full set", vresp.Variables)
	}
	if _, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{}); err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if len(audit.events) != 0 {
		t.Errorf("recorded %d scope-warning events for an undeclared task, want 0: %+v", len(audit.events), audit.events)
	}
}

// TestSecretsNoWarnWhenDeclarationCoversFullSet guards the strict-subset rule: a
// task that declared every delivered secret is not narrowing anything, so no warn
// fires even though it has a non-empty declaration.
func TestSecretsNoWarnWhenDeclarationCoversFullSet(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{
		DeclaredVariables: []string{"FOO", "BAR"},
	}}
	srv, a := newServer(store)
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar", "BAR": "baz"}}
	audit := &fakeScopeAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetSecretScopeAuditor(audit)

	if _, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{}); err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(audit.events) != 0 {
		t.Errorf("recorded %d events for a full-coverage declaration, want 0: %+v", len(audit.events), audit.events)
	}
}

// TestSecretsWarnWithoutAuditorStillDelivers proves the WARN log path is
// independent of the audit sink: with no auditor configured, a narrowly-declared
// task still receives the full set and the handler does not panic.
func TestSecretsWarnWithoutAuditorStillDelivers(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{DeclaredVariables: []string{"FOO"}}}
	srv, a := newServer(store)
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar", "BAR": "baz"}}
	srv.SetSecrets(sec, true) // no SetSecretScopeAuditor

	vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 2 {
		t.Errorf("variables = %v, want the full set", vresp.Variables)
	}
}
