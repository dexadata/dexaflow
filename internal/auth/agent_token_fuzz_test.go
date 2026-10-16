package auth

import (
	"reflect"
	"testing"
	"time"
)

// FuzzAuthenticateAgent feeds arbitrary bearer strings to the agent RPC's
// credential decoder. The bearer arrives from a task pod, which runs user code,
// so it is untrusted input to every agent RPC. AuthenticateAgent and
// RenewAgentToken must never panic, and an identity they accept must survive a
// re-mint unchanged (the warm-worker scope included, see identityFromClaims).
func FuzzAuthenticateAgent(f *testing.F) {
	clock := time.Date(2026, 10, 12, 12, 0, 0, 0, time.UTC)
	a := NewJWTAuthenticator(nil, "fuzz-secret-0123456789abcdef0123456789", time.Hour)
	a.now = func() time.Time { return clock }
	other := NewJWTAuthenticator(nil, "another-secret-0123456789abcdef012345", time.Hour)
	other.now = a.now

	for _, id := range []AgentIdentity{
		{TaskInstanceID: "ti-1", TenantID: "default", DagID: "etl", RunID: "manual__1", TaskID: "extract", TryNumber: 1},
		{TaskInstanceID: "ti-2", TenantID: "acme", DagID: "d", RunID: "r", TaskID: "t", TryNumber: 3, AttemptEpoch: 2, HasAttemptEpoch: true},
		{TenantID: "default", Scope: ScopeWarmWorker, DagVersionID: "dv-1", WorkerID: "warm-pod-0"},
	} {
		tok, err := a.IssueAgentToken(id, time.Hour)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(tok)
		foreign, err := other.IssueAgentToken(id, time.Hour)
		if err != nil {
			f.Fatal(err)
		}
		f.Add(foreign)
	}
	f.Add("")
	f.Add("a.b.c")
	f.Add("eyJhbGciOiJub25lIn0.eyJ0ZW5hbnRfaWQiOiJ4In0.") // gitleaks:allow: unsigned alg=none fixture, not a credential

	f.Fuzz(func(t *testing.T, token string) {
		id, err := a.AuthenticateAgent(token)
		_, _, _ = a.RenewAgentToken(token, time.Hour, 24*time.Hour) //nolint:errcheck // only the absence of a panic is under test here
		if err != nil {
			if id != nil {
				t.Fatalf("rejected token returned identity %+v", id)
			}
			return
		}
		if _, ferr := other.AuthenticateAgent(token); ferr == nil {
			t.Fatalf("token %q verifies under two different secrets", token)
		}
		again, err := a.IssueAgentToken(*id, time.Hour)
		if err != nil {
			t.Fatalf("re-minting an accepted identity: %v", err)
		}
		id2, err := a.AuthenticateAgent(again)
		if err != nil {
			t.Fatalf("re-minted token rejected: %v", err)
		}
		if !reflect.DeepEqual(*id2, *id) {
			t.Fatalf("re-mint changed the identity:\n got  %+v\n want %+v", *id2, *id)
		}
	})
}
