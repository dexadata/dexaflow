package agentrpc

import (
	"testing"

	agentv1 "github.com/dexadata/dexaflow/proto/agent/v1"
)

// TestSecretScopingDefaultIsPermissiveWholeVault is the SAFE-default contract: a
// server with no scoping policy set behaves EXACTLY as before this shipment —
// the whole tenant vault is delivered and the scoped path is never taken. This
// is the guarantee that a fresh deploy denies nothing and no task loses access.
func TestSecretScopingDefaultIsPermissiveWholeVault(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{DeclaredVariables: []string{"FOO"}}}
	srv, a := newServer(store)
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar", "BAR": "baz"}}
	srv.SetSecrets(sec, true) // no SetSecretScoping — default permissive

	vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 2 {
		t.Errorf("default permissive must deliver the whole vault: got %v", vresp.Variables)
	}
	if sec.scopedVar {
		t.Errorf("default permissive must not take the scoped path")
	}
}

// TestSecretScopingPermissiveDeclaredStillWholeVault pins the chosen permissive
// interpretation: even when a DAG declares a narrow set, permissive delivers the
// WHOLE vault (and warns) — it never subsets. Subsetting is reserved for
// enforce, so no already-declaring pipeline loses access on merge.
func TestSecretScopingPermissiveDeclaredStillWholeVault(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{DeclaredVariables: []string{"FOO"}}}
	srv, a := newServer(store)
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar", "BAR": "baz"}}
	audit := &fakeScopeAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetSecretScopeAuditor(audit)
	srv.SetSecretScoping(ScopingPermissive)

	vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 2 {
		t.Errorf("permissive must deliver the whole vault despite a narrow declaration: got %v", vresp.Variables)
	}
	if sec.scopedVar {
		t.Errorf("permissive must not take the scoped path")
	}
	// The narrowing is still surfaced by the E1b warn.
	if len(audit.events) != 1 || audit.events[0].kind != "variables" {
		t.Errorf("permissive must warn on the narrow declaration: %+v", audit.events)
	}
}

// TestSecretScopingEnforceReturnsOnlyDeclared is the enforce contract: a task
// receives ONLY its declared subset, resolved server-side and filtered in the
// query (the scoped path), not the whole vault.
func TestSecretScopingEnforceReturnsOnlyDeclared(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{
		DeclaredVariables:   []string{"FOO"},
		DeclaredConnections: []string{"pg"},
	}}
	srv, a := newServer(store)
	sec := &fakeSecrets{
		vars:  map[string]string{"FOO": "bar", "BAR": "baz"},
		conns: map[string]string{"pg": "postgres://u:p@h/db", "redis": "redis://h:6379"},
	}
	audit := &fakeScopeAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetSecretScopeAuditor(audit)
	srv.SetSecretScoping(ScopingEnforce)
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 1 || vresp.Variables["FOO"] != "bar" {
		t.Errorf("enforce must deliver only the declared subset {FOO}: got %v", vresp.Variables)
	}
	if !sec.scopedVar {
		t.Errorf("enforce must take the scoped (in-query) path")
	}
	cresp, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if len(cresp.ConnectionUris) != 1 || cresp.ConnectionUris["pg"] == "" {
		t.Errorf("enforce must deliver only the declared subset {pg}: got %v", cresp.ConnectionUris)
	}
	if !sec.scopedConn {
		t.Errorf("enforce must take the scoped (in-query) path for connections")
	}
	// No E1b warn under enforce: it delivered only-declared, not the whole vault.
	if len(audit.events) != 0 {
		t.Errorf("enforce must not emit a full-vault scope warning: %+v", audit.events)
	}
}

// TestSecretScopingEnforceEmptyDeclarationReturnsNone is the load-bearing []
// case: under enforce a task that declared nothing receives NOTHING — not the
// whole vault.
func TestSecretScopingEnforceEmptyDeclarationReturnsNone(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{}} // no declaration
	srv, a := newServer(store)
	sec := &fakeSecrets{
		vars:  map[string]string{"FOO": "bar"},
		conns: map[string]string{"pg": "postgres://u:p@h/db"},
	}
	srv.SetSecrets(sec, true)
	srv.SetSecretScoping(ScopingEnforce)
	ctx := ctxWithToken(t, a)

	vresp, err := srv.GetVariables(ctx, &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 0 {
		t.Errorf("enforce + empty declaration must deliver NO variables: got %v", vresp.Variables)
	}
	cresp, err := srv.GetConnections(ctx, &agentv1.GetConnectionsRequest{})
	if err != nil {
		t.Fatalf("GetConnections: %v", err)
	}
	if len(cresp.ConnectionUris) != 0 {
		t.Errorf("enforce + empty declaration must deliver NO connections: got %v", cresp.ConnectionUris)
	}
}

// TestSecretScopingOffWholeVaultNoWarn is the off contract: scoping disabled
// delivers the whole vault and emits no scope warning even for a narrow
// declaration — the operator explicitly turned scoping off.
func TestSecretScopingOffWholeVaultNoWarn(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{DeclaredVariables: []string{"FOO"}}}
	srv, a := newServer(store)
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar", "BAR": "baz"}}
	audit := &fakeScopeAuditor{}
	srv.SetSecrets(sec, true)
	srv.SetSecretScopeAuditor(audit)
	srv.SetSecretScoping(ScopingOff)

	vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 2 {
		t.Errorf("off must deliver the whole vault: got %v", vresp.Variables)
	}
	if sec.scopedVar {
		t.Errorf("off must not take the scoped path")
	}
	if len(audit.events) != 0 {
		t.Errorf("off must not emit a scope warning: %+v", audit.events)
	}
}

// TestSecretScopingUnknownFallsBackToPermissive proves the setter fails safe: an
// unrecognized policy value is treated as permissive (whole vault), never as a
// silent enforce that would deny.
func TestSecretScopingUnknownFallsBackToPermissive(t *testing.T) {
	store := &fakeStore{spec: TaskSpec{DeclaredVariables: []string{"FOO"}}}
	srv, a := newServer(store)
	sec := &fakeSecrets{vars: map[string]string{"FOO": "bar", "BAR": "baz"}}
	srv.SetSecrets(sec, true)
	srv.SetSecretScoping("bogus")

	vresp, err := srv.GetVariables(ctxWithToken(t, a), &agentv1.GetVariablesRequest{})
	if err != nil {
		t.Fatalf("GetVariables: %v", err)
	}
	if len(vresp.Variables) != 2 || sec.scopedVar {
		t.Errorf("an unknown scoping policy must fall back to permissive whole-vault: got %v scoped=%v", vresp.Variables, sec.scopedVar)
	}
}
